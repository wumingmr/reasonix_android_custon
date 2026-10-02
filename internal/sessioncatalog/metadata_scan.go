package sessioncatalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/historywork"
	"reasonix/internal/store"
)

// metadataRecord never opens the transcript, display index, or event log.
// Listing fields without a valid sidecar remain explicitly unknown.
func metadataRecord(ctx context.Context, target DirectoryTarget, path string) (SessionRecord, error) {
	st, err := os.Stat(path)
	if err != nil {
		return SessionRecord{}, err
	}
	if !st.Mode().IsRegular() {
		return SessionRecord{}, fmt.Errorf("not a session file")
	}
	meta, known, metaErr := agent.LoadBranchMetaBounded(ctx, path)
	if err := ctx.Err(); err != nil {
		return SessionRecord{}, err
	}
	order := agent.SessionOrderInfo{Path: path, Scope: target.Scope, WorkspaceRoot: target.WorkspaceRoot,
		CreatedAt: st.ModTime(), LastActivityAt: st.ModTime(), ModTime: st.ModTime()}
	if known && metaErr == nil {
		if strings.TrimSpace(meta.Scope) != "" {
			order.Scope, order.WorkspaceRoot = meta.DefaultScope(), meta.WorkspaceRoot
		}
		order.TopicID, order.TopicTitle, order.CustomTitle = meta.TopicID, meta.TopicTitle, meta.CustomTitle
		if !meta.CreatedAt.IsZero() {
			order.CreatedAt = meta.CreatedAt
		}
		if !meta.UpdatedAt.IsZero() {
			order.LastActivityAt = meta.UpdatedAt
		}
		order.Recovered, order.ParentID = meta.Recovered, meta.ParentID
		order.RecoveryReason, order.RecoveryDigest = meta.RecoveryReason, meta.RecoveryDigest
		order.Turns, order.Preview, order.SchemaVersion = meta.Turns, meta.Preview, meta.SchemaVersion
		order.Revision, order.ContentDigest = meta.Revision, meta.ContentDigest
		order.ListingRevision, order.ListingContentDigest = meta.ListingRevision, meta.ListingContentDigest
	}
	// Every physical session remains reachable without rewriting old sidecars
	// merely to assign a topic. A later authoritative topic assignment replaces it.
	if order.TopicID == "" {
		order.TopicID = agent.LegacySessionTopicID(path)
	}
	if order.TopicTitle == "" {
		order.TopicTitle = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	record := recordFromOrder(target, order) // LogSchema is deliberately unset: no whole head-index load.
	record.LogFormat, record.HeadCount, record.SelectedHeadID = max(1, meta.LogSchema), meta.HeadCount, meta.HeadID
	record.LogicalTopicID, record.OrdinaryVisible = record.TopicID, true
	if metaErr != nil {
		record.Health, record.TurnsState = HealthDegraded, TurnsUnknown
	}
	return record, nil
}

func (c *Catalog) indexMetadataPath(ctx context.Context, target DirectoryTarget, path string, sequence uint64) error {
	path = cleanCatalogAccessPath(path)
	if target.Path == "" {
		target.Path = filepath.Dir(path)
	}
	target.Path = cleanCatalogAccessPath(target.Path)
	target.Scope, target.WorkspaceRoot = normalizeScope(target.Scope, target.WorkspaceRoot)
	lock := c.directoryLock(target.Path)
	lock.Lock()
	defer lock.Unlock()
	record, err := metadataRecord(ctx, target, path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	record.enqueueSequence = sequence
	_, err = c.upsertExactPathSession(ctx, record)
	return err
}

// reconcileMetadata is the explicit, synchronous entry point. The ordinary
// queue drives the same scan one slice at a time, preserving its directory
// iterator while other roots receive their own slices.
func (c *Catalog) reconcileMetadata(ctx context.Context, target DirectoryTarget, sequence uint64) error {
	scan, err := c.startMetadataScan(ctx, target, sequence, false)
	if err != nil {
		return err
	}
	defer func() { scan.close(ctx, err) }()
	for {
		var done bool
		var bytes int64
		done, bytes, err = scan.step(ctx)
		if err != nil || done {
			return err
		}
		if c.opts.Maintenance == nil {
			err = historywork.Pause(ctx, bytes)
		}
		if err != nil {
			return err
		}
	}
}

type metadataScan struct {
	c          *Catalog
	target     DirectoryTarget
	sequence   uint64
	generation int64
	signature  string
	started    int64
	total      int
	file       *os.File
	lock       *sync.Mutex
	scanLock   *sync.Mutex
	yield      bool
}

var errMetadataScanBusy = errors.New("directory scan already active")

func (c *Catalog) startMetadataScan(ctx context.Context, target DirectoryTarget, sequence uint64, try bool) (_ *metadataScan, result error) {
	target.Path = cleanCatalogAccessPath(target.Path)
	target.Scope, target.WorkspaceRoot = normalizeScope(target.Scope, target.WorkspaceRoot)
	value, _ := c.metadataScans.LoadOrStore(c.pathKey(target.Path), &sync.Mutex{})
	scanLock := value.(*sync.Mutex)
	if try {
		if !scanLock.TryLock() {
			return nil, errMetadataScanBusy
		}
	} else {
		scanLock.Lock()
	}
	scan := &metadataScan{c: c, target: target, sequence: sequence, scanLock: scanLock, lock: c.directoryLock(target.Path), yield: try}
	defer func() {
		if result != nil {
			scan.close(ctx, result)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scan.started = c.opts.Now().UnixMilli()
	scan.signature = fmt.Sprintf("metadata:%d:%d", scan.started, sequence)
	scan.lock.Lock()
	generation, _, err := c.beginDirectoryScan(ctx, target, scan.signature, scan.started)
	scan.lock.Unlock()
	if err != nil {
		return nil, err
	}
	scan.generation = generation
	scan.file, err = os.Open(target.Path)
	if os.IsNotExist(err) {
		// Optional legacy roots do not exist on a fresh installation. They are
		// an empty discovery only while the catalog has never retained a row
		// there. An unavailable root with known history may be an unplugged
		// device; never turn that into missing-row confirmation.
		var known bool
		queryErr := c.readDB(ctx).QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM catalog_sessions WHERE directory_key=? LIMIT 1)`, c.pathKey(target.Path)).Scan(&known)
		if queryErr != nil {
			return nil, queryErr
		}
		if !known {
			return scan, nil
		}
	}
	if err != nil {
		return nil, err
	}
	st, err := scan.file.Stat()
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}
	return scan, nil
}

func (s *metadataScan) close(ctx context.Context, result error) {
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	if result != nil && s.generation != 0 {
		s.c.failDirectoryScan(context.WithoutCancel(ctx), s.target.Path, result)
	}
	if s.scanLock != nil {
		s.scanLock.Unlock()
		s.scanLock = nil
	}
}

// step commits only the visited prefix. Only a successful EOF can mark
// missing rows, including when shutdown, cancellation or device loss occurs.
func (s *metadataScan) step(ctx context.Context) (done bool, bytes int64, result error) {
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	c, target := s.c, s.target
	release := func(int64) {}
	if c.opts.Maintenance != nil {
		ctx = c.opts.Maintenance.Context(ctx)
		var err error
		if s.yield {
			release, err = c.opts.Maintenance.BackgroundSliceYielding(ctx, c.isPriorityDirectory(target))
		} else {
			release, err = c.opts.Maintenance.BackgroundSlice(ctx, c.isPriorityDirectory(target))
		}
		if err != nil {
			return false, 0, err
		}
	}
	defer func() { release(bytes) }()
	start := time.Now()
	records := make([]SessionRecord, 0, historywork.BatchEntries)
	s.lock.Lock()
	defer s.lock.Unlock()
	for count := 0; count < historywork.BatchEntries && bytes+historywork.ReadChunk+1 <= historywork.BatchBytes && time.Since(start) < historywork.SliceDuration; count++ {
		if err := ctx.Err(); err != nil {
			return false, bytes, err
		}
		if s.file == nil {
			done = true
			break
		}
		entries, readErr := s.file.ReadDir(1)
		if errors.Is(readErr, io.EOF) {
			done = true
			break
		}
		if readErr != nil {
			return false, bytes, readErr
		}
		entry := entries[0]
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !store.IsSessionTranscriptName(entry.Name()) {
			continue
		}
		record, readErr := metadataRecord(ctx, target, filepath.Join(target.Path, entry.Name()))
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return false, bytes, readErr
		}
		if metaInfo, statErr := os.Stat(agent.BranchMetaPath(record.Path)); statErr == nil {
			bytes += min(metaInfo.Size()+1, int64(historywork.ReadChunk+1))
		}
		if existing, found, loadErr := c.GetSession(ctx, record.Path); loadErr != nil {
			return false, bytes, loadErr
		} else if found && existing.ContentFingerprint == record.ContentFingerprint && existing.MetaFingerprint == record.MetaFingerprint {
			preserveDirectoryProjection(existing, &record)
			if existing.TurnsState != TurnsUnknown {
				record.Turns, record.Preview, record.TurnsState = existing.Turns, existing.Preview, existing.TurnsState
			}
			record.metadataUnchanged = sameMetadataProjection(existing, record)
		}
		record.enqueueSequence = s.sequence
		records = append(records, record)
	}
	if c.testReconcileBatchHook != nil {
		c.testReconcileBatchHook(len(records))
	}
	_, err := c.upsertSessionsWithNotification(ctx, records, nil, "metadata_batch", true, upsertDirectoryProjection)
	if err != nil {
		return false, bytes, err
	}
	s.total += len(records)
	err = c.updateDirectoryScanProgress(ctx, target.Path, s.generation, s.total)
	if err == nil && done {
		err = c.finishDirectoryScan(ctx, target, s.signature, s.generation, s.started, s.total)
	}
	return done, bytes, err
}
