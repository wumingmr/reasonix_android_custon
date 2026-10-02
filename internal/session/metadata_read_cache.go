package session

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reasonix/internal/historywork"
)

type sessionMetadataRead struct {
	fingerprint [32]byte
	info        SessionInfo
	used        uint64
}

type sessionMetadataReads struct {
	mu      sync.Mutex
	clock   uint64
	entries map[string]sessionMetadataRead
}

// Keep list metadata separate from execution state. Validation reads actual
// metadata bytes and the selected log revision, including external writers;
// no body replay, TTL staleness or writer ownership is involved.
func (p *FilesystemPersistence) cachedSessionInfo(ctx context.Context, id string) (SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return SessionInfo{}, err
	}
	id = strings.TrimSpace(id)
	input, key, err := p.readSessionMetadata(ctx, id)
	if err != nil {
		return SessionInfo{}, err
	}
	c := &p.metadataReads
	c.mu.Lock()
	c.clock++
	if entry, ok := c.entries[id]; ok && entry.fingerprint == key {
		entry.used = c.clock
		c.entries[id] = entry
		c.mu.Unlock()
		return entry.info, nil
	}
	c.mu.Unlock()
	info, err := input.info(id)
	if err != nil {
		return info, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]sessionMetadataRead{}
	}
	if len(c.entries) >= 2048 {
		oldest := ""
		var age uint64
		for name, entry := range c.entries {
			if oldest == "" || entry.used < age {
				oldest, age = name, entry.used
			}
		}
		delete(c.entries, oldest)
	}
	c.clock++
	c.entries[id] = sessionMetadataRead{key, info, c.clock}
	return info, nil
}

type sessionMetadataInput struct {
	dir             string
	manifest        Manifest
	header, catalog []byte
	hasHeader       bool
	revision        logRevision
}

func (p *FilesystemPersistence) readSessionMetadata(ctx context.Context, id string) (sessionMetadataInput, [32]byte, error) {
	var input sessionMetadataInput
	var zero [32]byte
	dir, err := p.sessionDir(id, true)
	if err != nil {
		return input, zero, err
	}
	input.dir = dir
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "root:%s\x00", dir)
	var manifest Manifest
	// Derive cache identity from the confined directory, as the store writer does.
	// Keep protocol input out of filesystem paths after sessionDir resolves it.
	cacheDir := filepath.Join(filepath.Dir(dir), ".query-cache", filepath.Base(dir))
	paths := []string{filepath.Join(dir, "manifest.json"), filepath.Join(dir, sessionHeaderName), catalogMetadataPath(cacheDir)}
	for index, path := range paths {
		body, readErr := readListingMetadata(ctx, path)
		if index == 0 {
			if readErr != nil {
				if os.IsNotExist(readErr) {
					readErr = fmt.Errorf("%w: %s", ErrSessionNotFound, id)
				}
				return input, zero, readErr
			}
			if err := json.Unmarshal(body, &manifest); err != nil {
				return input, zero, err
			}
			if !supportedStoredManifest(manifest) {
				return input, zero, ErrUnsupportedVersion
			}
			if manifest.SessionID != id {
				return input, zero, fmt.Errorf("%w: manifest belongs to another session", ErrDamagedStore)
			}
			input.manifest = manifest
		} else if index == 1 {
			if readErr != nil && !os.IsNotExist(readErr) {
				return input, zero, readErr
			}
			input.header, input.hasHeader = body, readErr == nil
		} else if readErr == nil {
			input.catalog = body
		}
		// Distinguish absent/unreadable fields from a readable empty file.
		_, _ = fmt.Fprintf(h, "%d:%v:", index, readErr)
		_ = binary.Write(h, binary.LittleEndian, uint64(len(body)))
		_, _ = h.Write(body)
	}
	stat, err := os.Stat(logPathForManifest(dir, manifest))
	if err != nil && !os.IsNotExist(err) {
		return input, zero, err
	}
	if stat != nil {
		_, _ = fmt.Fprintf(h, "log:%d:%d", stat.Size(), stat.ModTime().UnixNano())
		input.revision = logRevision{Size: stat.Size(), ModTimeNS: stat.ModTime().UnixNano(), Exists: true}
	}
	copy(zero[:], h.Sum(nil))
	return input, zero, nil
}

// Listing metadata is optional display information. An oversized sidecar must
// not turn discovery into an unbounded read; its caller keeps the source as an
// unknown/degraded entry and explicit session recovery uses its native reader.
func readListingMetadata(ctx context.Context, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(&historywork.Reader{Context: ctx, Source: f}, historywork.ReadChunk+1))
	if err == nil && len(body) > historywork.ReadChunk {
		return nil, fmt.Errorf("listing metadata exceeds read budget")
	}
	return body, err
}

func (in sessionMetadataInput) info(id string) (SessionInfo, error) {
	m := in.manifest
	info := SessionInfo{SessionID: id, Codec: m.Codec, Kind: m.Kind, CreatedAt: m.CreatedAt, UpdatedAt: m.CreatedAt, MetadataStatus: MetadataPending, Path: in.dir}
	if in.revision.Exists && in.revision.ModTimeNS > info.UpdatedAt.UnixNano() {
		info.UpdatedAt = time.Unix(0, in.revision.ModTimeNS)
	}
	if in.hasHeader {
		header, _, err := decodeSessionHeader(in.header, id)
		if err != nil {
			return SessionInfo{}, err
		}
		info.CWD, info.ParentSessionID, info.Origin = header.CWD, header.ParentSessionID, header.Origin
	}
	if metadata, err := decodeCatalogMetadata(in.catalog, m, in.revision); err == nil {
		info.Title, info.TitleSequence = metadata.Title, metadata.TitleSequence
		info.ModelRef, info.ModelIdentity = metadata.ModelRef, metadata.ModelIdentity
		info.Turns, info.Preview, info.MetadataStatus = metadata.Turns, metadata.Preview, MetadataReady
		info.EventSequence, info.ResultSequence = metadata.Sequence, metadata.ResultSequence
	}
	return info, nil
}
