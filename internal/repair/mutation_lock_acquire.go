package repair

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"reasonix/internal/filelock"
	"reasonix/internal/pathidentity"
)

type repairMutationTarget struct {
	path   string
	key    string
	info   os.FileInfo
	exists bool
	link   string
}

type repairMutationLockDomain struct {
	path string
	key  string
}

func repairMutationTargets(paths []string) ([]repairMutationTarget, []string, []string, error) {
	targets := make([]repairMutationTarget, 0, len(paths))
	primary := map[string]struct{}{}
	locks := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		identity, err := pathidentity.Resolve(path, pathidentity.Options{FollowLeaf: false})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("lock repair mutations: resolve target: %w", err)
		}
		if _, exists := primary[identity.Key]; exists {
			continue
		}
		info, link, statErr := inspectRepairMutationEntry(identity.AccessPath)
		exists := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil, nil, nil, fmt.Errorf("lock repair mutations: inspect target: %w", statErr)
		}
		primary[identity.Key] = struct{}{}
		targets = append(targets, repairMutationTarget{identity.AccessPath, identity.Key, info, exists, link})
		locks[identity.Key] = struct{}{}
		if legacy := legacyCanonicalRepairPath(path); legacy != "" {
			locks[legacy] = struct{}{}
		}
	}
	return targets, sortedRepairKeys(primary), sortedRepairKeys(locks), nil
}

func sortedRepairKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func repairMutationLockDomains(lockDir string, keys []string) ([]repairMutationLockDomain, error) {
	domains := make([]repairMutationLockDomain, 0, len(keys))
	for _, key := range keys {
		digest := sha256.Sum256([]byte(key))
		path := filepath.Join(lockDir, fmt.Sprintf("%x.lock", digest))
		identity, err := pathidentity.Resolve(path, pathidentity.Options{FollowLeaf: false})
		if err != nil {
			return nil, fmt.Errorf("lock repair mutations: resolve lock identity: %w", err)
		}
		domains = append(domains, repairMutationLockDomain{path: path, key: identity.Key})
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i].key < domains[j].key })
	return domains, nil
}

func acquireRepairMutationLocks(timeout time.Duration, domains []repairMutationLockDomain) ([]func(), error) {
	if timeout <= 0 {
		timeout = repairMutationLockTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		releases := make([]func(), 0, len(domains))
		for _, domain := range domains {
			release, err := filelock.TryAcquireModeWithKey(domain.path, domain.key, filelock.ModeExclusive)
			if err == nil {
				releases = append(releases, release)
				continue
			}
			releaseRepairMutationLocks(releases)
			if !errors.Is(err, filelock.ErrHeld) {
				return nil, fmt.Errorf("lock repair mutations: %w", err)
			}
			break
		}
		if len(releases) == len(domains) {
			return releases, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, fmt.Errorf("lock repair mutations: %w", ctx.Err())
		}
	}
}

func revalidateRepairMutationTargets(targets []repairMutationTarget) error {
	for _, target := range targets {
		identity, resolveErr := pathidentity.Resolve(target.path, pathidentity.Options{FollowLeaf: false})
		currentInfo, currentLink, statErr := inspectRepairMutationEntry(target.path)
		currentExists := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) && resolveErr == nil {
			resolveErr = statErr
		}
		if resolveErr != nil {
			return fmt.Errorf("lock repair mutations: revalidate target: %w", resolveErr)
		}
		if identity.Key != target.key || currentLink != target.link || repairEntryRedirected(target.info, target.exists, currentInfo, currentExists) {
			return errors.New("lock repair mutations: target identity changed while waiting")
		}
	}
	return nil
}

func inspectRepairMutationEntry(path string) (os.FileInfo, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Inodes can be reused immediately after unlink. Preserve the link
		// destination as well so a redirected link cannot pass SameFile.
		link, err := os.Readlink(path)
		return info, link, err
	}
	return info, "", nil
}

func releaseRepairMutationLocks(releases []func()) {
	for _, release := range slices.Backward(releases) {
		release()
	}
}

// Regular files may be atomically replaced under the lock; content checks
// detect stale state. Other node types retain native identity.
func repairEntryRedirected(before os.FileInfo, beforeExists bool, after os.FileInfo, afterExists bool) bool {
	if (!beforeExists || before.Mode().IsRegular()) && (!afterExists || after.Mode().IsRegular()) {
		return false
	}
	return beforeExists != afterExists || !os.SameFile(before, after)
}
