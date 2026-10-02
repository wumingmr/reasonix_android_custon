package main

import (
	"context"
	"os"

	"reasonix/desktop/internal/workspacestate"
)

// Source fences track identity/lifecycle, not file length, mtime, title or index
// revision. Appending content keeps a result readable; replacing/deleting its
// source or adopting it into another owner revokes it.
type readSourceFence struct {
	app          *App
	files        map[string]os.FileInfo
	bindings     map[string]string
	versions     *workspacestate.ReadVersions
	store        *readSnapshotStore
	snapshot     *readSnapshot
	metadataOnly bool
	indexed      map[string]string
}

func projectionReadVersions(state workspacestate.State, supplied []*workspacestate.ReadVersions) *workspacestate.ReadVersions {
	if len(supplied) > 0 && supplied[0] != nil {
		return supplied[0]
	}
	// Callers constructing their own projection cannot borrow a different
	// registry publication solely because its numeric generation matches.
	return workspacestate.NewReadVersions(state)
}

func (a *App) newReadSourceFence(store *readSnapshotStore, snapshot *readSnapshot) (*readSourceFence, error) {
	state, err := a.workspaceRegistry().VerifySnapshot(a.bootContext())
	if err != nil {
		return nil, err
	}
	return &readSourceFence{app: a, files: map[string]os.FileInfo{}, bindings: map[string]string{}, versions: state.ReadVersions(), store: store, snapshot: snapshot}, nil
}

func (f *readSourceFence) add(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}
	if _, ok := f.files[path]; ok {
		return nil
	}
	var info os.FileInfo
	if !f.metadataOnly {
		var err error
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
	} else if catalog := f.app.sessionCatalog.Load(); catalog != nil {
		record, ok, err := catalog.GetSession(ctx, path)
		if err != nil {
			return err
		}
		if ok {
			if f.indexed == nil {
				f.indexed = map[string]string{}
			}
			f.indexed[path] = snapshotBinding("catalog-source", []any{record.Scope, record.WorkspaceRoot, record.TopicID})
		}
	}
	if err := f.store.reserve(f.snapshot, int64(512+len(path)*2)); err != nil {
		return err
	}
	binding, err := f.versions.Source(path)
	if err != nil {
		return err
	}
	f.files[path] = info
	f.bindings[path] = binding
	return nil
}

func (f *readSourceFence) freeze() func() error {
	return f.validateCurrent
}

func (f *readSourceFence) validateCurrent() error {
	current, err := f.app.workspaceRegistry().VerifySnapshot(f.app.bootContext())
	if err != nil {
		return err
	}
	return f.validateWithCurrent(current)
}

func (f *readSourceFence) validateWithCurrent(current *workspacestate.ReadSnapshot) error {
	for path, original := range f.files {
		if !f.metadataOnly {
			info, err := os.Stat(path)
			if os.IsNotExist(err) {
				return snapshotStale("lifecycle_changed")
			}
			if err != nil {
				return err
			}
			if !os.SameFile(original, info) {
				return snapshotStale("lifecycle_changed")
			}
		} else if expected, ok := f.indexed[path]; ok {
			catalog := f.app.sessionCatalog.Load()
			if catalog == nil {
				return snapshotStale("lifecycle_changed")
			}
			record, found, err := catalog.GetSession(f.app.bootContext(), path)
			if err != nil {
				return err
			}
			if !found || record.MissingSince != 0 || record.Health == "missing" || snapshotBinding("catalog-source", []any{record.Scope, record.WorkspaceRoot, record.TopicID}) != expected {
				return snapshotStale("lifecycle_changed")
			}
		}
		binding, err := current.ReadVersions().Source(path)
		if err != nil {
			return err
		}
		if binding != f.bindings[path] {
			return snapshotStale("lifecycle_changed")
		}
	}
	return nil
}

func (a *App) workspaceReadFence(versions *workspacestate.ReadVersions, workspace workspacestate.Workspace, nodes []ProjectNode) func(*workspacestate.ReadSnapshot) error {
	states := map[string]string{}
	for _, node := range nodes {
		if node.Session != nil {
			states[node.Session.SessionID] = versions.Session(node.Session.SessionID)
		}
	}
	workspace.Organization = nil
	workspace.SessionIDs = nil
	return func(current *workspacestate.ReadSnapshot) error {
		owner, ok := current.WorkspaceMetadata(workspace.ID)
		if !ok || owner.Root != workspace.Root || owner.Visible != workspace.Visible {
			return snapshotStale("lifecycle_changed")
		}
		for id, expected := range states {
			member := current.Session(id)
			if member.Workspace.ID != workspace.ID || member.OwnershipConflict || current.ReadVersions().Session(id) != expected {
				return snapshotStale("lifecycle_changed")
			}
		}
		return nil
	}
}
