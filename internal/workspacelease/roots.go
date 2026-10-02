package workspacelease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"reasonix/internal/filelock"
)

type rootLockDomain struct {
	path string
	key  string
	mode filelock.Mode
}

var workspaceRootsAfterAcquire = func() {}

// HoldWriteRoots acquires one exclusive hold spanning several workspace roots.
// It preserves the legacy ancestor locks and coalesces tree-stripe collisions
// before acquisition, so a group cannot wait for a stripe it already owns.
// Callers must not hold separate leases for these roots while acquiring a group.
func HoldWriteRoots(ctx context.Context, lockDir string, roots ...string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	expected, err := snapshotWorkspaceRoots(roots)
	if err != nil {
		return nil, err
	}
	owner, domains, err := rootLockDomains(lockDir, roots)
	if err != nil {
		return nil, err
	}
	releases, err := acquireRootDomains(ctx, owner, domains)
	if err != nil {
		return nil, err
	}
	workspaceRootsAfterAcquire()
	for _, snapshot := range expected {
		canonical, _, identityErr := workspaceIdentities(snapshot.path)
		currentInfo, statErr := os.Stat(snapshot.path)
		currentExists := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) && identityErr == nil {
			identityErr = statErr
		}
		changed := identityErr == nil && (canonical != snapshot.key || currentExists != snapshot.exists || (currentExists && !os.SameFile(snapshot.info, currentInfo)))
		if identityErr != nil || changed {
			runReleases(releases)
			if identityErr != nil {
				return nil, fmt.Errorf("revalidate workspace root: %w", identityErr)
			}
			return nil, errors.New("workspace root identity changed while waiting")
		}
	}
	if err := ctx.Err(); err != nil {
		runReleases(releases)
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { runReleases(releases) }) }, nil
}

type workspaceRootSnapshot struct {
	path   string
	key    string
	info   os.FileInfo
	exists bool
}

func snapshotWorkspaceRoots(roots []string) ([]workspaceRootSnapshot, error) {
	snapshots := make([]workspaceRootSnapshot, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		key, _, err := workspaceIdentities(root)
		if err != nil {
			return nil, err
		}
		info, statErr := os.Stat(root)
		exists := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil, statErr
		}
		snapshots = append(snapshots, workspaceRootSnapshot{path: root, key: key, info: info, exists: exists})
	}
	return snapshots, nil
}

func acquireRootDomains(ctx context.Context, owner *Owner, domains []rootLockDomain) ([]func(), error) {
	notified := false
	for {
		releases := make([]func(), 0, len(domains))
		var blocked *rootLockDomain
		for i := range domains {
			domain := &domains[i]
			release, err := filelock.TryAcquireModeWithKey(domain.path, domain.key, domain.mode)
			if err == nil {
				releases = append(releases, release)
				continue
			}
			runReleases(releases)
			if !errors.Is(err, filelock.ErrHeld) {
				return nil, err
			}
			blocked = domain
			break
		}
		if blocked == nil {
			return releases, nil
		}
		waitRelease, err := owner.acquireQueuedMode(ctx, blocked.path, blocked.mode, &notified)
		if err != nil {
			return nil, err
		}
		waitRelease()
	}
}

func rootLockDomains(lockDir string, roots []string) (*Owner, []rootLockDomain, error) {
	lockDir = strings.TrimSpace(lockDir)
	var coordinator *Owner
	var compatibilityRoots []string
	exclusive := map[string]bool{}
	trees := map[string]bool{}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		owner, err := New(root, lockDir, nil)
		if err != nil {
			return nil, nil, err
		}
		if coordinator == nil {
			coordinator = owner
		}
		exclusive[owner.canonical] = true
		compatibilityRoots = append(compatibilityRoots, ancestorDirectories(owner.canonical)...)
		compatibilityRoots = append(compatibilityRoots, ancestorDirectories(owner.compatibility)...)
		trees[owner.treeLockPath(owner.canonical)] = true
	}
	// Match the single-root protocol: all compatibility ancestors first, then
	// tree stripes. Promote an ancestor requested by this group to exclusive.
	var domains []rootLockDomain
	for _, root := range orderedWorkspaceRoots(compatibilityRoots) {
		mode := filelock.ModeShared
		if exclusive[normalizeIdentityPath(root)] {
			mode = filelock.ModeExclusive
		}
		domain, err := makeRootLockDomain(workspaceLockPath(lockDir, root), mode)
		if err != nil {
			return nil, nil, err
		}
		domains = append(domains, domain)
	}
	paths := make([]string, 0, len(trees))
	for path := range trees {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		domain, err := makeRootLockDomain(path, filelock.ModeExclusive)
		if err != nil {
			return nil, nil, err
		}
		domains = append(domains, domain)
	}
	return coordinator, domains, nil
}

func makeRootLockDomain(path string, mode filelock.Mode) (rootLockDomain, error) {
	key, err := lockIdentityKey(path)
	if err != nil {
		return rootLockDomain{}, err
	}
	return rootLockDomain{path: path, key: key, mode: mode}, nil
}
