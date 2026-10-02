package workspacestate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"reasonix/internal/pathidentity"
)

func (s *Store) EnsureWorkspace(ctx context.Context, workspace Workspace) error {
	_, err := s.EnsureWorkspaceResolved(ctx, workspace)
	return err
}

// ResolveWorkspaceID returns the persisted owner of root's physical directory.
// Path-based projections must use this owner instead of deriving a fresh ID
// from a newer path spelling or identity scheme.
func ResolveWorkspaceID(state State, root string) (string, bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false, nil
	}
	identity, err := pathidentity.Resolve(root, pathidentity.Options{FollowLeaf: true})
	if err != nil {
		return "", false, fmt.Errorf("resolve workspace root: %w", err)
	}
	matches := matchingWorkspaceIDs(state, identity)
	if len(matches) == 0 {
		return "", false, nil
	}
	return canonicalWorkspaceOwner(state, matches), true, nil
}

// matchingWorkspaceIDs names every record whose root resolves to the candidate
// directory right now. A record this host cannot resolve is skipped: a
// directory that cannot be reached is not the one the candidate just resolved,
// and one unreachable record must not fail every other workspace's lookup.
func matchingWorkspaceIDs(state State, candidate pathidentity.Identity) []string {
	matches := make([]string, 0, 1)
	if candidate.Key == "" {
		return matches
	}
	for id, existing := range state.Workspaces {
		if strings.TrimSpace(existing.Root) == "" {
			continue
		}
		identity, err := pathidentity.Resolve(existing.Root, pathidentity.Options{FollowLeaf: true})
		if err == nil && identity.Key == candidate.Key {
			matches = append(matches, id)
		}
	}
	slices.Sort(matches)
	return matches
}

// canonicalWorkspaceOwner names the record that keeps a directory when several
// claim it. Global is addressed by that fixed ID across the app and can never
// be removed, so it wins; otherwise the oldest registration survives, and the
// sorted order settles equal or missing timestamps.
func canonicalWorkspaceOwner(state State, matches []string) string {
	owner := ""
	for _, id := range matches {
		if id == GlobalWorkspaceID {
			return id
		}
		if owner == "" || state.Workspaces[id].CreatedAt.Before(state.Workspaces[owner].CreatedAt) {
			owner = id
		}
	}
	return owner
}

// absorbDuplicateWorkspaceIdentities folds the losing records for one physical
// directory into owner. Registrations written before physical identity could
// record a directory twice, and every later resolution over it stays ambiguous
// until exactly one record owns it again. Sessions and their organization move
// with the record, so the repair never drops history.
func absorbDuplicateWorkspaceIdentities(state *State, owner string, matches []string) {
	target, ok := state.Workspaces[owner]
	if !ok || len(matches) < 2 {
		return
	}
	for _, id := range matches {
		duplicate, exists := state.Workspaces[id]
		if id == owner || !exists {
			continue
		}
		for _, sessionID := range duplicate.SessionIDs {
			if !contains(target.SessionIDs, sessionID) {
				target.SessionIDs = append(target.SessionIDs, sessionID)
			}
		}
		target.Visible = target.Visible || duplicate.Visible
		if strings.TrimSpace(target.Title) == "" {
			target.Title = duplicate.Title
		}
		absorbOrganization(&target, duplicate.Organization)
		repointWorkspaceReferences(state, id, owner)
		delete(state.Workspaces, id)
		state.WorkspaceIDs = remove(state.WorkspaceIDs, id)
	}
	target.UpdatedAt = time.Now().UTC()
	state.Workspaces[owner] = target
}

func absorbOrganization(target *Workspace, source *Organization) {
	if source == nil {
		return
	}
	if target.Organization == nil {
		target.Organization = &Organization{}
	}
	merged := target.Organization
	normalizeOrganization(merged)
	for _, key := range source.Order {
		if !slices.Contains(merged.Order, key) {
			merged.Order = append(merged.Order, key)
		}
	}
	for _, group := range source.Groups {
		index := slices.IndexFunc(merged.Groups, func(existing OrganizationGroup) bool { return existing.ID == group.ID })
		if index < 0 {
			merged.Groups = append(merged.Groups, group)
			continue
		}
		for _, member := range group.Members {
			if !slices.Contains(merged.Groups[index].Members, member) {
				merged.Groups[index].Members = append(merged.Groups[index].Members, member)
			}
		}
	}
	for key, imported := range source.Imported {
		if imported {
			merged.Imported[key] = true
		}
	}
	merged.ManualOrderEnabled = merged.ManualOrderEnabled || source.ManualOrderEnabled
	merged.MigrationVersion = max(merged.MigrationVersion, source.MigrationVersion)
	merged.Revision++
}

// repointWorkspaceReferences moves every record that addresses a workspace by
// ID. An in-flight create, purge, or recovery entry keeps its owner across the
// repair instead of resolving to a workspace this state no longer has.
func repointWorkspaceReferences(state *State, from, to string) {
	for key, mapping := range state.SourceMappings {
		if mapping.WorkspaceID == from {
			mapping.WorkspaceID = to
			state.SourceMappings[key] = mapping
		}
	}
	for key, pending := range state.PendingCreates {
		if pending.WorkspaceID == from {
			pending.WorkspaceID = to
			state.PendingCreates[key] = pending
		}
	}
	for key, operation := range state.PendingOperations {
		if operation.WorkspaceID == from {
			operation.WorkspaceID = to
			state.PendingOperations[key] = operation
		}
	}
	for key, entry := range state.RecoveryEntries {
		if entry.WorkspaceID == from {
			entry.WorkspaceID = to
			state.RecoveryEntries[key] = entry
		}
	}
}

// directoryVerified reports whether path names a directory that exists now. A
// path resolved through a missing leaf borrows the links and case rules of
// whichever ancestor happens to exist, so two distinct directories on an
// unreachable volume can share a key. Folding records is not reversible, so it
// is only done on a key the directory itself produced.
func directoryVerified(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// EnsureWorkspaceResolved registers workspace or returns the authoritative ID
// of an existing workspace with the same physical directory identity.
func (s *Store) EnsureWorkspaceResolved(ctx context.Context, workspace Workspace) (string, error) {
	workspace.ID = strings.TrimSpace(workspace.ID)
	if workspace.ID == "" {
		return "", errors.New("workspace id is required")
	}
	resolvedID := workspace.ID
	err := s.mutate(ctx, func(state *State) error {
		var candidate pathidentity.Identity
		var resolveErr error
		if strings.TrimSpace(workspace.Root) != "" {
			candidate, resolveErr = pathidentity.Resolve(workspace.Root, pathidentity.Options{FollowLeaf: true})
			if resolveErr != nil {
				return fmt.Errorf("resolve workspace root: %w", resolveErr)
			}
		}
		revalidateCandidate := func() error {
			if candidate.Key == "" {
				return nil
			}
			latest, latestErr := pathidentity.Resolve(workspace.Root, pathidentity.Options{FollowLeaf: true})
			if latestErr != nil {
				return fmt.Errorf("revalidate workspace root: %w", latestErr)
			}
			if latest.Key != candidate.Key {
				return fmt.Errorf("%w: workspace root identity changed during registration", ErrMutationConflict)
			}
			return nil
		}
		if candidate.Key != "" {
			matches := matchingWorkspaceIDs(*state, candidate)
			if len(matches) > 0 {
				if err := revalidateCandidate(); err != nil {
					return err
				}
				resolvedID = canonicalWorkspaceOwner(*state, matches)
				if len(matches) > 1 && directoryVerified(candidate.PhysicalPath) {
					absorbDuplicateWorkspaceIdentities(state, resolvedID, matches)
				}
				return nil
			}
		}
		now := time.Now().UTC()
		current, exists := state.Workspaces[workspace.ID]
		if exists {
			if current.Root == workspace.Root || (current.Root == "" && workspace.Root == "") {
				return revalidateCandidate()
			}
			if workspace.ID == GlobalWorkspaceID && candidate.Key != "" {
				if err := revalidateCandidate(); err != nil {
					return err
				}
				rebindGlobalRoot(state, workspace.Root, now)
				return nil
			}
			if workspace.ID == GlobalWorkspaceID || candidate.Key == "" {
				return ErrMutationConflict
			}
			resolvedID = versionedWorkspaceID(candidate.Key)
			if fallback, fallbackExists := state.Workspaces[resolvedID]; fallbackExists {
				identity, resolveErr := pathidentity.Resolve(fallback.Root, pathidentity.Options{FollowLeaf: true})
				if resolveErr != nil {
					return fmt.Errorf("resolve collided workspace %q: %w", resolvedID, resolveErr)
				}
				if identity.Key != candidate.Key {
					return ErrMutationConflict
				}
				if err := revalidateCandidate(); err != nil {
					return err
				}
				return nil
			}
		}
		if err := revalidateCandidate(); err != nil {
			return err
		}
		workspace.ID = resolvedID
		workspace.SessionIDs = []string{}
		workspace.CreatedAt, workspace.UpdatedAt = now, now
		state.Workspaces[workspace.ID] = workspace
		state.WorkspaceIDs = append(state.WorkspaceIDs, workspace.ID)
		return nil
	})
	return resolvedID, err
}

// rebindGlobalRoot moves global to root. Global is a singleton derived from the
// state root, so a different stored root is where the data directory used to
// be, not a second workspace; it is kept so sessions recorded there stay global.
func rebindGlobalRoot(state *State, root string, now time.Time) {
	w := state.Workspaces[GlobalWorkspaceID]
	former := slices.DeleteFunc(slices.Clone(w.FormerRoots), func(r string) bool { return r == root || r == w.Root })
	if w.Root != "" {
		former = append(former, w.Root)
	}
	w.Root, w.FormerRoots, w.UpdatedAt = root, former, now
	state.Workspaces[GlobalWorkspaceID] = w
}

func versionedWorkspaceID(identityKey string) string {
	sum := sha256.Sum256([]byte("reasonix-workspace-pathidentity-v2\x00" + identityKey))
	return "project-v2-" + hex.EncodeToString(sum[:12])
}
