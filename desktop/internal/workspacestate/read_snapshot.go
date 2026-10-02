package workspacestate

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"reasonix/internal/pathidentity"
)

// ReadSnapshot is an immutable, coherent view of verified registry bytes. It
// owns its maps privately; retaining it never observes a later publication.
// Verification is O(file bytes); lookups on a retained snapshot are O(1).
type ReadSnapshot struct {
	state         State
	owners        map[string]string
	conflicts     map[string]bool
	activeTopics  map[string]int
	adoptedTopics map[string]map[string]bool
	headSources   map[string]bool
	versions      *ReadVersions
}

// SessionMetadata deliberately excludes workspace members and organization:
// resolving one address must not copy a project's entire session list.
type SessionMetadata struct {
	Workspace         Workspace
	State             SessionState
	Presentation      Presentation
	Registered        bool
	OwnershipConflict bool
	SharedTopic       bool
}

type snapshotVerification struct {
	done     chan struct{}
	snapshot *ReadSnapshot
	err      error
	readers  int // guarded by Store.verificationMu; includes the initiating reader
}

func (s *Store) publishSnapshotLocked(body []byte, state State) {
	state.sourceIdentities = newSourceIdentityIndex(state)
	r := &ReadSnapshot{state: state, owners: make(map[string]string), conflicts: make(map[string]bool), activeTopics: make(map[string]int), headSources: make(map[string]bool), versions: NewReadVersions(state)}
	r.adoptedTopics = adoptedTopicIndex(state)
	for key, workspace := range state.Workspaces {
		for _, id := range workspace.SessionIDs {
			if _, exists := r.owners[id]; exists {
				r.conflicts[id] = true
			}
			r.owners[id] = key
		}
	}
	for id, p := range state.Presentation {
		if p.TopicID != "" && state.SessionStates[id].Lifecycle == Active {
			r.activeTopics[p.TopicID]++
		}
	}
	for _, mapping := range state.SourceMappings {
		if mapping.HeadID != "" {
			// This is a locator hint only. Exact source/head mappings and the
			// selected head still decide adoption; it never proves content.
			if key, err := sourcePathKey(mapping.Path); err == nil {
				r.headSources[key] = true
			}
		}
	}
	s.readBody = body
	s.readSnapshot.Store(r)
}

// VerifySnapshot checks actual current bytes, including legacy writers that
// preserve generation and timestamps. Execution/mutation admission must use
// this boundary, never PublishedSnapshot alone.
func (s *Store) VerifySnapshot(ctx context.Context) (*ReadSnapshot, error) {
	if s == nil || strings.TrimSpace(s.path) == "" || s.path == "." {
		return nil, errors.New("workspace state path is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.verificationMu.Lock()
	flight := s.verification
	leader := flight == nil
	if leader {
		flight = &snapshotVerification{done: make(chan struct{})}
		s.verification = flight
	}
	flight.readers++
	s.verificationMu.Unlock()
	if leader {
		// A registry read already in flight is shared by overlapping callers.
		// One caller's cancellation cannot poison another's validation. The
		// file read is finite; no task or model lifetime is owned here.
		s.mu.Lock()
		flight.err = s.verifySnapshotLocked(context.WithoutCancel(ctx))
		if flight.err == nil {
			flight.snapshot = s.readSnapshot.Load()
		}
		s.verificationMu.Lock()
		s.verification = nil
		close(flight.done)
		s.verificationMu.Unlock()
		s.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return flight.snapshot, flight.err
	}
}

// PublishedSnapshot returns the last successful verification for display only.
// It performs no I/O and can be stale after an external writer's change.
func (s *Store) PublishedSnapshot() *ReadSnapshot {
	if s == nil {
		return nil
	}
	return s.readSnapshot.Load()
}

func (r *ReadSnapshot) Generation() uint64 { return r.state.Generation }

func (r *ReadSnapshot) WorkspaceMetadata(id string) (Workspace, bool) {
	w, ok := r.state.Workspaces[id]
	w.SessionIDs, w.Organization, w.extra = nil, nil, nil
	w.FormerRoots = slices.Clone(w.FormerRoots)
	return w, ok
}

func (r *ReadSnapshot) Session(id string) SessionMetadata {
	status, registered := r.state.SessionStates[id]
	status.extra = nil
	p := r.state.Presentation[id]
	p.extra = nil
	w, _ := r.WorkspaceMetadata(r.owners[id])
	others := r.activeTopics[p.TopicID]
	if status.Lifecycle == Active && p.TopicID != "" {
		others--
	}
	return SessionMetadata{Workspace: w, State: status, Presentation: p, Registered: registered,
		OwnershipConflict: r.conflicts[id], SharedTopic: p.TopicID != "" && others > 0}
}

func (r *ReadSnapshot) Source(key string) (SourceMapping, bool) {
	mapping, ok, _ := r.state.ResolveSource(key)
	return mapping, ok
}

func (r *ReadSnapshot) ResolveSource(key string) (SourceMapping, bool, error) {
	return r.state.ResolveSource(key)
}

func sourcePathKey(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("source path is required")
	}
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	identity, err := pathidentity.Resolve(abs, pathidentity.Options{FollowLeaf: true})
	return identity.Key, err
}

func (r *ReadSnapshot) HasHeadSource(path string) (bool, error) {
	if len(r.headSources) == 0 {
		return false, nil
	}
	key, err := sourcePathKey(path)
	return r.headSources[key], err
}
