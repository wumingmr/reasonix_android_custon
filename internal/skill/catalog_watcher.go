package skill

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/fsnotify/fsnotify"
)

type watcherLifecycle struct {
	cancel context.CancelFunc
	active bool
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.watcherMu.Lock()
	if s.hostWatch.active {
		// Service-backed watches: Release is logical and never blocks on
		// backend IO, so Close cannot get stuck on an uninterruptible
		// registration path.
		subs := s.hostWatch.subs
		s.hostWatch.subs = nil
		s.hostWatch.active = false
		s.closed = true
		s.watcherMu.Unlock()
		for _, sub := range subs {
			sub.Release()
		}
		s.catalogMu.Lock()
		if s.catalogFlight != nil && s.catalogFlight.cancel != nil {
			s.catalogFlight.cancel()
		}
		s.catalogMu.Unlock()
		return nil
	}
	if s.closed {
		done := s.watcherDone
		s.watcherMu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	s.closed = true
	s.watcherGeneration++
	done, cancel := s.watcherDone, s.watcherLifecycle.cancel
	s.watcher, s.watcherLifecycle.cancel = nil, nil
	s.watcherLifecycle.active = false
	s.watcherMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.catalogMu.Lock()
	if s.catalogFlight != nil && s.catalogFlight.cancel != nil {
		s.catalogFlight.cancel()
	}
	s.catalogMu.Unlock()
	if done != nil {
		<-done
	}
	return nil
}

func (s *Store) ensureWatcher() {
	if s == nil || s.disableDiscovery {
		return
	}
	if s.hostWatch.service != nil {
		s.subscribeHostWatch()
		return
	}
	s.watcherMu.Lock()
	if s.closed || s.watcherLifecycle.active {
		s.watcherMu.Unlock()
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		s.watcherMu.Unlock()
		return
	}
	s.watcherGeneration++
	generation := s.watcherGeneration
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	s.watcher, s.watcherDone, s.watcherLifecycle.active = watcher, done, true
	s.watcherLifecycle.cancel = cancel
	s.watcherMu.Unlock()

	ready := make(chan struct{})
	go s.watchCatalog(ctx, watcher, generation, done, ready)
	<-ready
}

func (s *Store) watchCatalog(ctx context.Context, watcher *fsnotify.Watcher, generation uint64, done, ready chan struct{}) {
	defer close(done)
	defer func() {
		s.watcherMu.Lock()
		if s.watcher == watcher && s.watcherGeneration == generation {
			s.watcher = nil
			s.watcherDone = nil
			s.watcherLifecycle.active = false
		}
		s.watcherMu.Unlock()
	}()
	runCatalogWatch(ctx, watcher.Events, watcher.Errors, ready,
		func() { s.refreshWatcherPaths(ctx, watcher, generation) }, watcher.Close,
		func(reason string) {
			if s.watcherCurrent(watcher, generation) {
				s.Invalidate(reason)
			}
		})
}

func runCatalogWatch(ctx context.Context, events <-chan fsnotify.Event, errors <-chan error, ready chan struct{}, register func(), closeWatcher func() error, invalidate func(string)) {
	ctx, cancel := context.WithCancel(ctx)
	refresh := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		register()
		close(ready)
		for {
			select {
			case <-ctx.Done():
				_ = closeWatcher()
				return
			case <-refresh:
				register()
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	// Add and Close may wait for fsnotify's error sender. Keep both channels
	// draining while the registration worker mutates or closes the watcher.
	for events != nil || errors != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ctx.Err() != nil {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Write|fsnotify.Chmod) == 0 {
				continue
			}
			invalidate("filesystem changed")
			// A create/rename can introduce a directory, symlink target, or a
			// previously missing root. Rebuild the subscriptions from the roots.
			select {
			case refresh <- struct{}{}:
			default:
			}
		case _, ok := <-errors:
			if !ok {
				errors = nil
				continue
			}
			if ctx.Err() == nil {
				invalidate("filesystem watcher failed")
			}
		}
	}
}

func (s *Store) watcherCurrent(watcher *fsnotify.Watcher, generation uint64) bool {
	s.watcherMu.Lock()
	defer s.watcherMu.Unlock()
	return !s.closed && s.watcher == watcher && s.watcherGeneration == generation
}

func (s *Store) refreshWatcherPaths(ctx context.Context, watcher *fsnotify.Watcher, generation uint64) {
	if !s.watcherCurrent(watcher, generation) {
		return
	}
	for _, root := range s.roots() {
		directories, _ := watchDirectoriesContext(ctx, root.Dir, s.maxDepth)
		for _, dir := range directories {
			if ctx.Err() != nil {
				return
			}
			_ = watcher.Add(dir)
		}
	}
}

// watchDirectories includes every existing directory that discovery can visit.
// For a missing root it subscribes to the nearest existing ancestor, allowing
// later creation to invalidate the snapshot. Symlink targets are traversed once.
func watchDirectoriesContext(ctx context.Context, root string, maxDepth int) ([]string, bool) {
	root = filepath.Clean(root)
	probe := root
	for {
		if ctx.Err() != nil {
			return nil, false
		}
		info, err := os.Stat(probe)
		if err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil, true
		}
		probe = parent
	}
	if probe != root {
		return []string{probe}, true
	}
	type pendingDir struct {
		path  string
		depth int
	}
	pending := []pendingDir{{path: root, depth: 0}}
	seen := map[string]bool{}
	var out []string
	for len(pending) > 0 {
		if ctx.Err() != nil {
			return nil, false
		}
		current := pending[0]
		pending = pending[1:]
		resolved := current.path
		if target, err := filepath.EvalSymlinks(current.path); err == nil {
			resolved = filepath.Clean(target)
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		info, err := os.Stat(current.path)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, current.path)
		if resolved != current.path {
			out = append(out, resolved)
		}
		if current.depth >= maxDepth {
			continue
		}
		entries, err := os.ReadDir(current.path)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return nil, false
			}
			child := filepath.Join(current.path, entry.Name())
			if entry.IsDir() {
				pending = append(pending, pendingDir{path: child, depth: current.depth + 1})
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if target, err := os.Stat(child); err == nil && target.IsDir() {
					pending = append(pending, pendingDir{path: child, depth: current.depth + 1})
				}
			}
		}
	}
	sort.Strings(out)
	return out, true
}

// Invalidate advances the catalog generation. The last complete snapshot stays
// available to cancelled callers until a replacement scan completes.
