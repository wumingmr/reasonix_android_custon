package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrRecentSnapshotPreparing = errors.New("session: recent snapshot is preparing")

type PreparationState string

const (
	PreparationReady     PreparationState = "ready"
	PreparationPreparing PreparationState = "preparing"
	PreparationFailed    PreparationState = "failed"
)

type SessionOpenView struct {
	Ref               SessionRef       `json:"session"`
	StorageGeneration string           `json:"storageGeneration,omitempty"`
	SnapshotSequence  uint64           `json:"snapshotSequence"`
	AcceptedSequence  uint64           `json:"acceptedSequence"`
	DurableSequence   uint64           `json:"durableSequence"`
	Recent            RecentSnapshot   `json:"recent"`
	Recovery          PreparationState `json:"recovery"`
	History           PreparationState `json:"history"`
	Search            PreparationState `json:"search"`
	CanExecute        bool             `json:"canExecute"`
}

type SessionInspection struct {
	Ref               SessionRef `json:"session"`
	StorageGeneration string     `json:"storageGeneration,omitempty"`
	Commits           uint64     `json:"commits"`
	Events            uint64     `json:"events"`
	DurableSequence   uint64     `json:"durableSequence"`
}

// Recent returns the published bounded baseline without opening the history
// locator, search database, or writer lease.
func (q *Query) Recent(ctx context.Context, ref SessionRef) (RecentSnapshot, error) {
	if q == nil || q.persistence == nil {
		return RecentSnapshot{}, errors.New("session: nil query")
	}
	if err := ctx.Err(); err != nil {
		return RecentSnapshot{}, err
	}
	if err := ref.validate(q.hostID); err != nil {
		return RecentSnapshot{}, err
	}
	filesystem, ok := q.persistence.(*FilesystemPersistence)
	if !ok {
		if q.service != nil {
			if runtime, live := q.service.Runtime(ref); live {
				snapshot := runtime.Session().RecentSnapshot()
				q.authorizeRecent(ref, snapshot)
				return snapshot, nil
			}
		}
		return RecentSnapshot{}, ErrRecentSnapshotPreparing
	}
	dir := filepath.Join(filesystem.Root, ref.SessionID)
	manifest, err := readStoredManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return RecentSnapshot{}, err
	}
	identity, err := readStorageIdentity(dir, manifest)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, ErrStaleGeneration) {
			return RecentSnapshot{}, ErrRecentSnapshotPreparing
		}
		return RecentSnapshot{}, err
	}
	snapshot, err := readRecentSnapshot(dir, identity)
	if os.IsNotExist(err) {
		if q.service != nil {
			if runtime, live := q.service.Runtime(ref); live {
				snapshot := runtime.Session().RecentSnapshot()
				q.authorizeRecent(ref, snapshot)
				return snapshot, nil
			}
		}
		return RecentSnapshot{}, ErrRecentSnapshotPreparing
	}
	q.authorizeRecent(ref, snapshot)
	return snapshot, err
}

func (q *Query) authorizeRecent(ref SessionRef, snapshot RecentSnapshot) {
	for _, entry := range snapshot.Entries {
		if entry.ContentRef != nil && entry.EventSequence <= snapshot.DurableSequence {
			q.authorizeContentForGeneration(ref.SessionID, snapshot.StorageGeneration, entry.ContentRef.Digest, entry.ContentRef.Bytes, entry.ContentRef.IndexDigest)
		}
	}
}

// OpenSession returns an observation baseline. It never acquires writer
// ownership for a cold session; callers request execution separately.
func (s *Service) OpenSession(ctx context.Context, ref SessionRef) (SessionOpenView, error) {
	if s == nil || s.query == nil {
		return SessionOpenView{}, errors.New("session: nil service")
	}
	return s.query.OpenSession(ctx, ref)
}

func (q *Query) OpenSession(ctx context.Context, ref SessionRef) (SessionOpenView, error) {
	if q == nil {
		return SessionOpenView{}, errors.New("session: nil query")
	}
	if err := ref.validate(q.hostID); err != nil {
		return SessionOpenView{}, err
	}
	view := SessionOpenView{Ref: ref, History: PreparationPreparing, Search: PreparationPreparing}
	if filesystem, ok := q.persistence.(*FilesystemPersistence); ok {
		if info, statErr := os.Stat(historyIndexPath(filesystem.Root, ref.SessionID)); statErr == nil && info.Mode().IsRegular() {
			view.History = PreparationReady
		}
		if info, statErr := os.Stat(searchIndexPath(filesystem.Root, ref.SessionID)); statErr == nil && info.Mode().IsRegular() {
			view.Search = PreparationReady
		}
	}
	if q.service != nil {
		if runtime, ok := q.service.Runtime(ref); ok {
			snapshot, err := q.Recent(ctx, ref)
			if err != nil {
				return SessionOpenView{}, err
			}
			view.Recent, view.StorageGeneration = snapshot, snapshot.StorageGeneration
			view.SnapshotSequence, view.AcceptedSequence, view.DurableSequence = snapshot.DurableSequence, runtime.Session().EventSequence(), snapshot.DurableSequence
			view.Recovery, view.CanExecute = PreparationReady, true
			return view, nil
		}
	}
	recent, err := q.Recent(ctx, ref)
	if err != nil {
		if errors.Is(err, ErrRecentSnapshotPreparing) {
			view.Recovery = PreparationPreparing
			return view, nil
		}
		return SessionOpenView{}, err
	}
	view.Recent, view.StorageGeneration = recent, recent.StorageGeneration
	view.SnapshotSequence, view.AcceptedSequence, view.DurableSequence = recent.DurableSequence, recent.DurableSequence, recent.DurableSequence
	view.Recovery = PreparationPreparing
	return view, nil
}

// EnsureExecution prepares or reuses the single RuntimeOwner and returns a
// caller-scoped binding. Releasing that binding cannot cancel shared recovery.
func (s *Service) EnsureExecution(ctx context.Context, ref SessionRef) (*ClientBinding, error) {
	return s.Open(ctx, ref)
}

// StreamSession is the explicit complete-history boundary used by export,
// migration and diagnostics. Normal open, paging and search paths never call
// it or construct a cumulative commit slice.
func (q *Query) StreamSession(ctx context.Context, ref SessionRef, visit func(Commit) error) error {
	if q == nil || q.persistence == nil || visit == nil {
		return errors.New("session: stream requires query and visitor")
	}
	if err := ref.validate(q.hostID); err != nil {
		return err
	}
	handle, err := q.persistence.Open(ref.SessionID, ReadOnly)
	if err != nil {
		return err
	}
	defer handle.Close(context.WithoutCancel(ctx))
	var cursor uint64
	for {
		page, err := handle.Read(ctx, cursor, 256)
		if err != nil {
			return err
		}
		for _, commit := range page.Commits {
			if err := visit(commit); err != nil {
				return err
			}
		}
		if !page.Truncated {
			return nil
		}
		if page.Next <= cursor {
			return fmt.Errorf("%w: stream cursor did not advance", ErrDamagedStore)
		}
		cursor = page.Next
	}
}

// InspectSession performs the full durable traversal intentionally omitted
// from OpenSession. A successful result proves every visited transaction and
// externally stored event payload was readable at inspection time.
func (q *Query) InspectSession(ctx context.Context, ref SessionRef) (SessionInspection, error) {
	result := SessionInspection{Ref: ref}
	if filesystem, ok := q.persistence.(*FilesystemPersistence); ok {
		manifest, err := readStoredManifest(filepath.Join(filesystem.Root, ref.SessionID, "manifest.json"))
		if err != nil {
			return SessionInspection{}, err
		}
		identity, err := readStorageIdentity(filepath.Join(filesystem.Root, ref.SessionID), manifest)
		if err != nil {
			return SessionInspection{}, err
		}
		result.StorageGeneration = identity.Generation
	}
	err := q.StreamSession(ctx, ref, func(commit Commit) error {
		result.Commits++
		result.Events += uint64(len(commit.Events))
		result.DurableSequence = commit.LastSequence()
		return nil
	})
	return result, err
}
