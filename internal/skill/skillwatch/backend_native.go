//go:build !windows

package skillwatch

import (
	"errors"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

var errBackendClosed = errors.New("watch backend closed")

// nativeBackend watches directories in process. Add/Remove/Close run on one
// serialized registration goroutine while a pump drains Events and Errors, so
// a registration can never wait on fsnotify's error sender — the invariant the
// retired Store-owned watcher established. Physical watches are refcounted
// across registrations: the same directory is registered with the kernel once.
type nativeBackend struct {
	svc *Service

	mu       sync.Mutex
	closed   bool
	refs     map[string]int
	watchers map[string]map[regKey]struct{}
	byID     map[uint64]regKey

	ops      chan func()
	pumpDone chan struct{}
	watcher  *fsnotify.Watcher
}

type regKey struct {
	id  uint64
	gen uint64
}

var _ backend = (*nativeBackend)(nil)

// newPlatformBackend selects the in-process native backend. ForceHelper exists
// so tests exercise the helper protocol on any OS.
func newPlatformBackend(svc *Service, opts Options) (backend, string, *helperClient) {
	if opts.ForceHelper {
		start := opts.HelperCommand
		if start == nil {
			start = defaultHelperCommand
		}
		h := newHelperClient(start, svc)
		return h, "helper", h
	}
	return newNativeBackend(svc), "native", nil
}

func newNativeBackend(svc *Service) *nativeBackend {
	b := &nativeBackend{
		svc:      svc,
		refs:     map[string]int{},
		watchers: map[string]map[regKey]struct{}{},
		byID:     map[uint64]regKey{},
		ops:      make(chan func(), 4096),
		pumpDone: make(chan struct{}),
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Without a backend every registration fails and degrades to the
		// bounded scan fallback.
		close(b.pumpDone)
		return b
	}
	b.watcher = watcher
	go b.registrationLoop()
	go b.pump()
	return b
}

func (b *nativeBackend) registrationLoop() {
	defer close(b.pumpDone)
	for op := range b.ops {
		op()
	}
	if b.watcher != nil {
		_ = b.watcher.Close()
	}
}

func (b *nativeBackend) pump() {
	events := b.watcher.Events
	errors := b.watcher.Errors
	for events != nil || errors != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			b.dispatch(event)
		case _, ok := <-errors:
			if !ok {
				errors = nil
				continue
			}
			// Backend-level failure: touch every covered registration so each
			// root revalidates (and re-arms) through its subscribers.
			b.mu.Lock()
			keys := make([]regKey, 0, len(b.byID))
			for _, key := range b.byID {
				keys = append(keys, key)
			}
			b.mu.Unlock()
			for _, key := range keys {
				b.svc.eventArrived(key.id, key.gen, OpWrite)
			}
		}
	}
}

func (b *nativeBackend) dispatch(event fsnotify.Event) {
	var op Op
	switch {
	case event.Op&fsnotify.Create != 0:
		op = OpCreate
	case event.Op&fsnotify.Remove != 0:
		op = OpRemove
	case event.Op&fsnotify.Rename != 0:
		op = OpRename
	case event.Op&fsnotify.Write != 0:
		op = OpWrite
	case event.Op&fsnotify.Chmod != 0:
		op = OpChmod
	default:
		return
	}
	b.mu.Lock()
	// fsnotify reports the changed path (file or directory); registrations
	// cover directories. Walk up to find the watched ancestor(s).
	var keys []regKey
	for dir := event.Name; ; {
		if subs := b.watchers[dir]; subs != nil {
			for key := range subs {
				keys = append(keys, key)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	b.mu.Unlock()
	for _, key := range keys {
		b.svc.eventArrived(key.id, key.gen, op)
	}
}

func (b *nativeBackend) register(id, rootGen uint64, _ string, dirs []string) error {
	b.mu.Lock()
	watcher := b.watcher
	b.mu.Unlock()
	if watcher == nil {
		return errBackendClosed
	}
	key := regKey{id: id, gen: rootGen}
	result := make(chan error, 1)
	ok := b.enqueue(func() {
		// One critical section for Add and bookkeeping: physicalWatches>0
		// therefore implies the kernel watch is armed, and a dispatched event
		// can never fall between "watching" and "mapped".
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			result <- errBackendClosed
			return
		}
		var added []string
		for _, dir := range dirs {
			if b.refs[dir] > 0 {
				// Already watched for another registration: just join it.
			} else if err := watcher.Add(dir); err != nil {
				for _, undo := range added {
					b.removeRegistrationDirsLocked(key, undo)
				}
				b.mu.Unlock()
				result <- err
				return
			}
			b.refs[dir]++
			if b.watchers[dir] == nil {
				b.watchers[dir] = map[regKey]struct{}{}
			}
			b.watchers[dir][key] = struct{}{}
			added = append(added, dir)
		}
		b.byID[id] = key
		b.mu.Unlock()
		result <- nil
	})
	if !ok {
		return errBackendClosed
	}
	return <-result
}

// removeRegistrationDirsLocked unrolls one registration's state for one dir
// and removes the kernel watch when its refcount reaches zero.
func (b *nativeBackend) removeRegistrationDirsLocked(key regKey, dir string) {
	if subs := b.watchers[dir]; subs != nil {
		if _, ok := subs[key]; ok {
			delete(subs, key)
			if len(subs) == 0 {
				delete(b.watchers, dir)
			}
			b.refs[dir]--
			if b.refs[dir] <= 0 {
				delete(b.refs, dir)
				if b.watcher != nil {
					_ = b.watcher.Remove(dir)
				}
			}
		}
	}
}

// enqueue submits one serialized watcher operation. It reports false when the
// backend already closed or the queue is saturated; both surface as backend
// failure and degrade the root instead of blocking callers. Sends happen under
// mu, the same lock close() holds while closing the channel, so a late
// operation can never panic — and buffered sends never deadlock.
func (b *nativeBackend) enqueue(op func()) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	select {
	case b.ops <- op:
		return true
	default:
		return false
	}
}

// removeRegistrationLocked unrolls one registration's map state and removes
// kernel watches whose refcount reached zero. Called only from the ops
// goroutine, which is what owns watcher mutation.
func (b *nativeBackend) removeRegistrationLocked(key regKey) {
	if _, ok := b.byID[key.id]; !ok {
		return
	}
	delete(b.byID, key.id)
	for dir := range b.refs {
		b.removeRegistrationDirsLocked(key, dir)
	}
}

func (b *nativeBackend) cancel(id uint64) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	key, ok := b.byID[id]
	b.mu.Unlock()
	if !ok {
		return
	}
	done := make(chan struct{})
	b.enqueue(func() {
		defer close(done)
		b.mu.Lock()
		b.removeRegistrationLocked(key)
		b.mu.Unlock()
	})
	<-done
}

func (b *nativeBackend) close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	close(b.ops)
	b.mu.Unlock()
	<-b.pumpDone
	return nil
}

func (b *nativeBackend) physicalWatches() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return uint64(len(b.refs))
}
