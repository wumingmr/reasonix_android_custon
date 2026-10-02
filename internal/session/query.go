package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"reasonix/internal/historywork"
	"reasonix/internal/provider"
)

// Query is the read model shared by desktop, CLI, Serve, ACP and bots. It uses
// an attached runtime when one exists and otherwise opens only a read handle;
// querying cold history never constructs an Agent or acquires writer ownership.
type Query struct {
	maintenance          *historywork.Coordinator
	readMu               sync.Mutex
	readScopes           map[string]*queryReadScope
	metadataOnlyListings bool
	hostID               string
	persistence          SessionPersistence
	service              *Service
	rebuildMu            sync.Mutex
	rebuilding           map[string]struct{}
	metadataQueue        []metadataRebuildTask
	metadataWorkers      int
	metadataFailures     map[string]error
	generation           map[string]uint64
	rebuildCtx           context.Context
	rebuildStop          context.CancelFunc
	rebuildWG            sync.WaitGroup
	closed               bool
	slots                *rebuildSlots
	indexMu              sync.Mutex
	indexLocks           map[string]*sync.Mutex
	contentMu            sync.Mutex
	contentGrants        map[string]time.Time
	searchMu             sync.Mutex
	searchBuilds         map[string]*searchPreparation
	historyMu            sync.Mutex
	historyBuilds        map[string]*historyPreparation
}

func (s *Service) Query() *Query {
	if s == nil {
		return nil
	}
	return s.query
}

func newQuery(hostID string, persistence SessionPersistence, service *Service) *Query {
	rebuildCtx, rebuildStop := context.WithCancel(context.Background())
	query := &Query{
		hostID: hostID, persistence: persistence, service: service,
		rebuilding: map[string]struct{}{}, generation: map[string]uint64{}, rebuildCtx: rebuildCtx,
		metadataFailures: map[string]error{},
		rebuildStop:      rebuildStop, slots: newRebuildSlots(2),
		indexLocks:    map[string]*sync.Mutex{},
		contentGrants: map[string]time.Time{},
		searchBuilds:  map[string]*searchPreparation{},
		historyBuilds: map[string]*historyPreparation{},
	}
	return query
}

func contentGrantKey(sessionID, storageGeneration, digest string, bytes int64, indexDigest string) string {
	return sessionID + "\x00" + storageGeneration + "\x00" + digest + "\x00" + fmt.Sprint(bytes) + "\x00" + indexDigest
}

func (q *Query) storageGeneration(sessionID string) string {
	filesystem, ok := q.persistence.(*FilesystemPersistence)
	if !ok {
		return ""
	}
	dir := filepath.Join(filesystem.Root, sessionID)
	manifest, err := readStoredManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return ""
	}
	identity, err := readStorageIdentity(dir, manifest)
	if err != nil {
		return ""
	}
	return identity.Generation
}

func (q *Query) authorizeContentForGeneration(sessionID, generation, digest string, bytes int64, indexDigest string) {
	if generation == "" {
		return
	}
	q.contentMu.Lock()
	defer q.contentMu.Unlock()
	now := time.Now()
	for key, expiry := range q.contentGrants {
		if !expiry.After(now) {
			delete(q.contentGrants, key)
		}
	}
	q.contentGrants[contentGrantKey(sessionID, generation, digest, bytes, indexDigest)] = now.Add(15 * time.Minute)
}

func (q *Query) contentAuthorized(sessionID, digest string, bytes int64, indexDigest string) bool {
	generation := q.storageGeneration(sessionID)
	if generation == "" {
		return false
	}
	q.contentMu.Lock()
	defer q.contentMu.Unlock()
	key := contentGrantKey(sessionID, generation, digest, bytes, indexDigest)
	expiry, ok := q.contentGrants[key]
	if !ok || !expiry.After(time.Now()) {
		delete(q.contentGrants, key)
		return false
	}
	return true
}

func (q *Query) projectionLock(kind, sessionID string) *sync.Mutex {
	key := kind + "\x00" + sessionID
	q.indexMu.Lock()
	defer q.indexMu.Unlock()
	lock := q.indexLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		q.indexLocks[key] = lock
	}
	return lock
}

// Close stops catalog work owned by this query. Individual List callers do not
// own shared rebuilds, so cancelling one request never cancels work another
// caller may use; the host query lifetime is the cancellation boundary.
func (q *Query) Close() {
	if q == nil {
		return
	}
	q.rebuildMu.Lock()
	if !q.closed {
		q.closed = true
		if q.rebuildStop != nil {
			q.rebuildStop()
		}
	}
	q.rebuildMu.Unlock()
	q.rebuildWG.Wait()
}

func (q *Query) Snapshot(ctx context.Context, ref SessionRef) (Snapshot, error) {
	if q == nil || q.persistence == nil {
		return Snapshot{}, fmt.Errorf("session: nil session query")
	}
	if err := ref.validate(q.hostID); err != nil {
		return Snapshot{}, err
	}
	if q.service != nil {
		if runtime, ok := q.service.Runtime(ref); ok {
			return runtime.Session().Snapshot(), nil
		}
	}
	handle, err := q.persistence.Open(ref.SessionID, ReadOnly)
	if err != nil {
		return Snapshot{}, err
	}
	defer handle.Close(context.WithoutCancel(ctx))
	projection := Projection{}
	var cursor uint64
	for {
		page, readErr := handle.Read(ctx, cursor, 1000)
		if readErr != nil {
			return Snapshot{}, readErr
		}
		for _, commit := range page.Commits {
			if err := applyProjectionCommit(&projection, commit); err != nil {
				return Snapshot{}, err
			}
		}
		if !page.Truncated {
			break
		}
		if page.Next <= cursor {
			return Snapshot{}, fmt.Errorf("%w: cold history cursor did not advance", ErrDamagedStore)
		}
		cursor = page.Next
	}
	sequence := projection.CommittedSequence
	return Snapshot{EventSequence: sequence, DurableSequence: sequence, PersistenceStatus: PersistenceReady, Projection: projection}, nil
}

func (q *Query) History(ctx context.Context, ref SessionRef) ([]provider.Message, error) {
	snapshot, err := q.Snapshot(ctx, ref)
	if err != nil {
		return nil, err
	}
	return append([]provider.Message(nil), snapshot.Projection.Messages...), nil
}

// Stat returns one header-backed metadata observation without opening event
// bodies. Live projection state overlays the disposable cache, matching List.
func (q *Query) Stat(ctx context.Context, ref SessionRef) (SessionInfo, error) {
	if q == nil || q.persistence == nil {
		return SessionInfo{}, fmt.Errorf("session: nil session query")
	}
	if err := ref.validate(q.hostID); err != nil {
		return SessionInfo{}, err
	}
	info, err := q.persistence.Stat(ctx, ref.SessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	q.enrichInfo(&info)
	return info, nil
}

// ResolveSessionID turns a caller-selected opaque ID into a server-observed
// SessionRef. The requested value is used only for equality; every identity
// returned to filesystem-backed readers comes from the persistence catalog.
func (q *Query) ResolveSessionID(ctx context.Context, requested string) (SessionRef, error) {
	if q == nil || q.persistence == nil {
		return SessionRef{}, fmt.Errorf("session: nil session query")
	}
	candidate := strings.TrimSpace(requested)
	if err := validateSessionID(candidate); err != nil {
		return SessionRef{}, err
	}
	cursor := ""
	for {
		page, err := q.persistence.List(ctx, cursor, 100)
		if err != nil {
			return SessionRef{}, err
		}
		for _, info := range page.Sessions {
			if info.SessionID == candidate {
				return SessionRef{HostID: q.hostID, SessionID: info.SessionID}, nil
			}
		}
		if page.NextCursor == "" {
			return SessionRef{}, fmt.Errorf("%w: %s", ErrSessionNotFound, candidate)
		}
		if page.NextCursor <= cursor {
			return SessionRef{}, fmt.Errorf("%w: catalog cursor did not advance", ErrDamagedStore)
		}
		cursor = page.NextCursor
	}
}

func (q *Query) List(ctx context.Context, cursor string, limit int) (SessionPage, error) {
	if q == nil || q.persistence == nil {
		return SessionPage{}, fmt.Errorf("session: nil session query")
	}
	page, err := q.persistence.List(ctx, cursor, limit)
	if err != nil {
		return SessionPage{}, err
	}
	for i := range page.Sessions {
		q.enrichInfo(&page.Sessions[i])
	}
	return page, nil
}

func (q *Query) enrichInfo(info *SessionInfo) {
	info.Ref = SessionRef{HostID: q.hostID, SessionID: info.SessionID}
	if info.Error != "" {
		return
	}
	if q.service != nil {
		if runtime, ok := q.service.Runtime(info.Ref); ok {
			applyCatalogMetadata(info, runtime.Session().CatalogMetadata())
			return
		}
	}
	if !q.metadataOnlyListings && info.Codec == Codec && info.MetadataStatus != MetadataReady {
		q.rebuildMu.Lock()
		failure := q.metadataFailures[info.SessionID]
		q.rebuildMu.Unlock()
		if failure != nil {
			info.MetadataStatus, info.Error = MetadataFailed, failure.Error()
			return
		}
		q.scheduleMetadataRebuild(info.SessionID)
	}
}

func applyCatalogMetadata(info *SessionInfo, metadata catalogMetadata) {
	info.Title, info.TitleSequence = metadata.Title, metadata.TitleSequence
	info.ModelRef, info.ModelIdentity = metadata.ModelRef, metadata.ModelIdentity
	info.Turns, info.Preview, info.MetadataStatus = metadata.Turns, metadata.Preview, MetadataReady
	info.EventSequence, info.ResultSequence = metadata.Sequence, metadata.ResultSequence
}

func (q *Query) scheduleMetadataRebuild(sessionID string) {
	if _, ok := q.persistence.(*FilesystemPersistence); !ok {
		return
	}
	q.rebuildMu.Lock()
	if q.closed {
		q.rebuildMu.Unlock()
		return
	}
	if _, exists := q.rebuilding[sessionID]; exists {
		q.rebuildMu.Unlock()
		return
	}
	q.rebuilding[sessionID] = struct{}{}
	delete(q.metadataFailures, sessionID)
	generation := q.generation[sessionID]
	q.metadataQueue = append(q.metadataQueue, metadataRebuildTask{sessionID, generation})
	if q.metadataWorkers < 2 {
		q.metadataWorkers++
		q.rebuildWG.Add(1)
		go q.runMetadataRebuilds()
	}
	q.rebuildMu.Unlock()
}

type metadataRebuildTask struct {
	sessionID  string
	generation uint64
}

// Keep a deduplicated ID queue, not one goroutine per session. Workers wait
// behind user history/recovery work and continue without another List call.
func (q *Query) runMetadataRebuilds() {
	defer q.rebuildWG.Done()
	for {
		q.rebuildMu.Lock()
		if q.closed || len(q.metadataQueue) == 0 {
			q.metadataWorkers--
			if q.closed {
				q.metadataQueue = nil
				clear(q.rebuilding)
			}
			q.rebuildMu.Unlock()
			return
		}
		task := q.metadataQueue[0]
		q.metadataQueue[0] = metadataRebuildTask{}
		q.metadataQueue = q.metadataQueue[1:]
		q.rebuildMu.Unlock()
		if err := q.slots.acquire(q.rebuildCtx, rebuildPriorityPrefetch); err != nil {
			continue
		}
		err := q.rebuildCatalogMetadata(task.sessionID, task.generation)
		q.slots.release()
		q.rebuildMu.Lock()
		if err != nil && q.generation[task.sessionID] == task.generation {
			q.metadataFailures[task.sessionID] = err
		}
		delete(q.rebuilding, task.sessionID)
		q.rebuildMu.Unlock()
	}
}

// invalidateCatalog fences every metadata task scheduled for an older session
// incarnation. Service calls it before deleting the directory, so a delayed
// task cannot publish a cache entry that makes the deleted session reappear.
func (q *Query) invalidateCatalog(sessionID string) {
	if q == nil {
		return
	}
	q.rebuildMu.Lock()
	q.generation[sessionID]++
	delete(q.metadataFailures, sessionID)
	q.rebuildMu.Unlock()
}

func (q *Query) rebuildCatalogMetadata(sessionID string, generation uint64, callers ...context.Context) error {
	ctx := q.rebuildCtx
	if len(callers) > 0 {
		ctx = callers[0]
	}
	filesystem, ok := q.persistence.(*FilesystemPersistence)
	if !ok {
		return nil
	}
	handle, err := q.persistence.Open(sessionID, ReadOnly)
	if err != nil {
		return err
	}
	defer handle.Close(context.WithoutCancel(ctx))
	sessionDir := filepath.Join(filesystem.Root, sessionID)
	cacheDir := filepath.Join(filesystem.Root, ".query-cache", filepath.Base(sessionID))
	manifest, err := readManifest(filepath.Join(sessionDir, "manifest.json"))
	if err != nil {
		return err
	}
	metadata, err := reduceCatalogMetadata(ctx, handle, manifest)
	if err != nil {
		return err
	}
	q.rebuildMu.Lock()
	defer q.rebuildMu.Unlock()
	if ctx.Err() != nil || q.rebuildCtx.Err() != nil || q.generation[sessionID] != generation {
		return nil
	}
	// Hold the generation boundary through publication. Deletion invalidates
	// before it moves the directory, so it either wins first or waits until this
	// exact-incarnation cache is completely written and then removes it.
	return writeCatalogMetadataForSession(cacheDir, sessionDir, metadata)
}

// RefreshMetadata is an explicit management-operation fence. Passive list
// reads never call it; imports and archive candidates need an exact committed
// sequence before they can freeze their optimistic mutation preconditions.
func (q *Query) RefreshMetadata(ctx context.Context, ref SessionRef) (SessionInfo, error) {
	if err := ref.validate(q.hostID); err != nil {
		return SessionInfo{}, err
	}
	q.rebuildMu.Lock()
	generation := q.generation[ref.SessionID]
	q.rebuildMu.Unlock()
	if err := q.rebuildCatalogMetadata(ref.SessionID, generation, ctx); err != nil {
		return SessionInfo{}, err
	}
	return q.Stat(ctx, ref)
}
