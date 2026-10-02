package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"reasonix/internal/fileutil"
)

const (
	sparseIndexCodec    = "reasonix.session.offset-index/v1"
	sparseIndexInterval = 256
	sparseIdentityBytes = 4096
)

type sparseIndex struct {
	Codec        string             `json:"codec"`
	LogSize      int64              `json:"logSize"`
	LogModTimeNS int64              `json:"logModTimeNs"`
	LogIdentity  string             `json:"logIdentity"`
	LastSequence uint64             `json:"lastSequence"`
	CommitCount  uint64             `json:"commitCount"`
	Entries      []sparseIndexEntry `json:"entries"`
	partial      bool
}

type sparseIndexEntry struct {
	FirstSequence uint64 `json:"firstSequence"`
	Offset        int64  `json:"offset"`
}

func sparseIndexPath(cacheDir string) string {
	return filepath.Join(cacheDir, "events.offset-index.json")
}

// loadOrBuildSparseIndex treats the index as an expendable cache. A missing or
// corrupt cache is rebuilt by validating the complete durable log. Failure to
// write the rebuilt cache does not make an otherwise readable session fail.
func loadOrBuildSparseIndex(ctx context.Context, dir, cacheDir string) (sparseIndex, error) {
	if err := ctx.Err(); err != nil {
		return sparseIndex{}, err
	}
	manifest, err := readStoredManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return sparseIndex{}, err
	}
	logPath := logPathForManifest(dir, manifest)
	file, err := os.Open(logPath)
	if os.IsNotExist(err) {
		return sparseIndex{Codec: sparseIndexCodec, Entries: []sparseIndexEntry{}}, nil
	}
	if err != nil {
		return sparseIndex{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return sparseIndex{}, err
	}
	identity, err := sparseLogIdentity(file, info)
	if err != nil {
		return sparseIndex{}, err
	}
	if cached, ok := readValidSparseIndex(cacheDir, info, identity); ok {
		return cached, nil
	}

	rebuilt := sparseIndex{
		Codec:        sparseIndexCodec,
		LogSize:      info.Size(),
		LogModTimeNS: info.ModTime().UnixNano(),
		LogIdentity:  identity,
		Entries:      []sparseIndexEntry{},
	}
	commitIndex := 0
	visit := func(offset int64, commit Commit) bool {
		if ctx.Err() != nil {
			return false
		}
		if commitIndex%sparseIndexInterval == 0 {
			rebuilt.Entries = append(rebuilt.Entries, sparseIndexEntry{FirstSequence: commit.FirstSequence, Offset: offset})
		}
		commitIndex++
		rebuilt.CommitCount++
		rebuilt.LastSequence = commit.LastSequence()
		return true
	}
	if manifest.Codec == Codec {
		err = scanV4CommitFileRefs(ctx, file, 0, 1, contentStoreForSessionDir(dir), nil, visit)
	} else {
		err = scanCommitFileCodec(file, 0, 1, manifest.Codec, nil, visit)
	}
	if err != nil {
		return sparseIndex{}, err
	}
	if err := ctx.Err(); err != nil {
		return sparseIndex{}, err
	}
	writeSparseIndex(cacheDir, rebuilt)
	return rebuilt, nil
}

func readValidSparseIndex(cacheDir string, info os.FileInfo, identity string) (sparseIndex, bool) {
	data, err := os.ReadFile(sparseIndexPath(cacheDir))
	if err != nil {
		return sparseIndex{}, false
	}
	var cached sparseIndex
	if json.Unmarshal(data, &cached) != nil || !cached.validFor(info, identity) {
		return sparseIndex{}, false
	}
	return cached, true
}

func (idx sparseIndex) validFor(info os.FileInfo, identity string) bool {
	if idx.Codec != sparseIndexCodec || idx.LogSize != info.Size() || idx.LogModTimeNS != info.ModTime().UnixNano() || idx.LogIdentity != identity {
		return false
	}
	var previousSequence uint64
	var previousOffset int64 = -1
	for i, entry := range idx.Entries {
		if entry.FirstSequence == 0 || entry.Offset < 0 || entry.Offset >= idx.LogSize || entry.FirstSequence <= previousSequence || entry.Offset <= previousOffset {
			return false
		}
		if i == 0 && (entry.FirstSequence != 1 || entry.Offset != 0) {
			return false
		}
		previousSequence, previousOffset = entry.FirstSequence, entry.Offset
	}
	return (idx.LastSequence == 0) == (len(idx.Entries) == 0) && (idx.CommitCount == 0) == (idx.LastSequence == 0)
}

func writeSparseIndex(cacheDir string, index sparseIndex) {
	data, err := json.Marshal(index)
	if err != nil || os.MkdirAll(cacheDir, 0o700) != nil {
		return
	}
	_ = fileutil.AtomicWriteFileStrict(sparseIndexPath(cacheDir), append(data, '\n'), 0o600)
}

func (s *Store) recordPersistedIndex(file *os.File, start int64, commits []Commit, lengths []int64) {
	if s == nil || file == nil || len(commits) == 0 || len(commits) != len(lengths) {
		return
	}
	info, err := file.Stat()
	if err != nil {
		return
	}
	offset := start
	for i, commit := range commits {
		if i == len(commits)-1 {
			s.tip = durableTip{LogOffset: offset + int64(lengths[i]), AnchorOffset: offset, AnchorFirst: commit.FirstSequence, AnchorCommitID: commit.ID, AnchorHash: commit.OperationHash}
		}
		offset += int64(lengths[i])
	}
	identity, err := sparseLogIdentity(file, info)
	if err != nil {
		return
	}
	s.indexMu.Lock()
	index := s.index
	if index.partial {
		index.LogSize = info.Size()
		index.LogModTimeNS = info.ModTime().UnixNano()
		index.LogIdentity = identity
		index.LastSequence = commits[len(commits)-1].LastSequence()
		s.index = index
		s.indexMu.Unlock()
		return
	}
	if index.Codec != sparseIndexCodec || index.LogSize != start {
		s.indexMu.Unlock()
		_ = s.rebuildWriterIndex(file)
		return
	}
	offset = start
	for i, commit := range commits {
		if index.CommitCount%sparseIndexInterval == 0 {
			index.Entries = append(index.Entries, sparseIndexEntry{FirstSequence: commit.FirstSequence, Offset: offset})
		}
		index.CommitCount++
		index.LastSequence = commit.LastSequence()
		offset += int64(lengths[i])
	}
	index.LogSize = info.Size()
	index.LogModTimeNS = info.ModTime().UnixNano()
	index.LogIdentity = identity
	s.index = index
	s.indexMu.Unlock()
	writeSparseIndex(s.dir, index)
}

// A reopened writer knows the durable sequence but not the older checkpoints.
// Adopt an already validated cache before appending, so the new commits can
// extend it without scanning history or leaving the disk index stale.
func (s *Store) adoptPersistedIndex(file *os.File, start int64) {
	s.indexMu.Lock()
	partial, lastSequence := s.index.partial, s.index.LastSequence
	s.indexMu.Unlock()
	if !partial {
		return
	}
	info, err := file.Stat()
	if err != nil || info.Size() != start {
		return
	}
	identity, err := sparseLogIdentity(file, info)
	if err != nil {
		return
	}
	cached, ok := readValidSparseIndex(s.dir, info, identity)
	if !ok || cached.LastSequence != lastSequence {
		return
	}
	s.indexMu.Lock()
	if s.index.partial && s.index.LastSequence == lastSequence {
		s.index = cached
	}
	s.indexMu.Unlock()
}

func (s *Store) rebuildWriterIndex(_ *os.File) error {
	index, err := loadOrBuildSparseIndex(context.Background(), s.dir, s.dir)
	if err != nil {
		return err
	}
	s.indexMu.Lock()
	s.index = index
	s.indexMu.Unlock()
	return nil
}

func (idx sparseIndex) checkpoint(offset uint64) sparseIndexEntry {
	if len(idx.Entries) == 0 {
		return sparseIndexEntry{FirstSequence: 1}
	}
	target := offset + 1
	position := max(sort.Search(len(idx.Entries), func(i int) bool { return idx.Entries[i].FirstSequence > target })-1, 0)
	return idx.Entries[position]
}

func sparseLogIdentity(file *os.File, info os.FileInfo) (string, error) {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d:%d:", info.Size(), info.ModTime().UnixNano())
	readChunk := func(offset, length int64) error {
		if length <= 0 {
			return nil
		}
		buf := make([]byte, length)
		n, err := file.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			return err
		}
		_, _ = hash.Write(buf[:n])
		return nil
	}
	first := min(info.Size(), sparseIdentityBytes)
	if err := readChunk(0, first); err != nil {
		return "", err
	}
	if info.Size() > first {
		last := min(info.Size()-first, sparseIdentityBytes)
		if err := readChunk(info.Size()-last, last); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
