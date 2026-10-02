// Package skillwatch shares physical skill-directory watches across stores.
// Healthy roots use coalesced native events. Failed roots use bounded backoff
// scans until registration recovers. Windows isolates blocking filesystem APIs
// in a helper process. Subscribe completes registration before the caller's
// first catalog scan, subject to the helper timeout.
package skillwatch

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"
)

// scanBackoffs is the degraded-mode cadence replacing the retired 250ms
// polling generation.
var scanBackoffs = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}

const (
	coalesceWindow   = 100 * time.Millisecond
	coalesceMaxDelay = 500 * time.Millisecond
	// helperControlTimeout bounds one helper control round trip; a slower
	// helper is torn down and rebuilt. helperRestartWindow / helperRestartLimit
	// cap automatic rebuilds before the service degrades to scanning.
	helperControlTimeout = 5 * time.Second
	helperRestartWindow  = 30 * time.Second
	helperRestartLimit   = 2
	// helperSubscribeWait bounds how long Subscribe waits for the helper to
	// confirm a registration before letting the caller's first scan proceed.
	// Expiry is non-fatal: the registration stays in flight under the control
	// timeout and late confirmation still arms the watches.
	helperSubscribeWait = 2 * time.Second
)

// ScopeFunc returns the directories discovery can visit under root for the
// given depth. It includes the nearest existing ancestor of a missing root so
// later creation invalidates the snapshot, and excludes subtrees discovery
// skips (scripts/assets/references bodies) so their content churn cannot
// trigger catalog rebuilds.
type ScopeFunc func(ctx context.Context, root string, maxDepth int) (dirs []string, complete bool)

// HashFunc summarizes one root's watched tree for degraded-mode scanning and
// reports how many entries the scan visited. ok is false when the scan could
// not complete (cancelled, IO error).
type HashFunc func(ctx context.Context, root string, maxDepth int) (sum [sha256.Size]byte, entries int, ok bool)

// Options configure the service.
type Options struct {
	// Stderr receives diagnostic warnings; nil defaults to os.Stderr.
	Stderr io.Writer
	// HelperCommand overrides the helper process used by Windows and tests.
	HelperCommand func(ctx context.Context) (helperProcess, error)
	// ForceHelper routes every platform through the helper backend. Test-only.
	ForceHelper bool
	// ScanOnly serves subscriptions from backoff scanning and never starts a
	// helper. Test-only: it lets a caller hold a real, closable service without
	// spawning a child (which in a test binary is the test binary itself).
	ScanOnly bool
}

// Diagnostics snapshots the resource counters. Healthy idle state keeps scans
// at zero after initialization and never grows physical watches while the
// subscription set is constant.
type Diagnostics struct {
	PhysicalWatches      uint64 `json:"physicalWatches"`
	LogicalSubscriptions uint64 `json:"logicalSubscriptions"`
	Scans                uint64 `json:"scans"`
	ScannedEntries       uint64 `json:"scannedEntries"`
	EventsReceived       uint64 `json:"eventsReceived"`
	Notifications        uint64 `json:"notifications"`
	DegradedRoots        uint64 `json:"degradedRoots"`
	HelperRestarts       uint64 `json:"helperRestarts"`
}

// Subscription is one store's handle on a physical root. Release is safe to
// call twice and never blocks on backend IO: the logical subscription dies
// immediately and late events for it are dropped.
type Subscription struct {
	svc      *Service
	root     string // canonical root path (resolved)
	maxDepth int
	onChange func(reason string)

	mu       sync.Mutex
	released bool
}

// Release detaches the subscription. The last subscription on a root tears the
// physical watches down; no historical project watches are retained.
func (sub *Subscription) Release() {
	if sub == nil {
		return
	}
	sub.mu.Lock()
	if sub.released {
		sub.mu.Unlock()
		return
	}
	sub.released = true
	sub.mu.Unlock()
	sub.svc.dropSubscription(sub)
}

type rootState struct {
	canonical string
	gen       uint64 // bumped on every (re)registration; late events dropped
	dirs      []string
	watchID   uint64 // backend registration carrying the current gen
	maxDepth  int
	subs      map[*Subscription]struct{}
	scope     ScopeFunc
	hash      HashFunc

	scopeDirty bool

	// regDone closes when the current registration attempt settles, bounding
	// the Subscribe-side wait for the register-before-scan ordering.
	regDone chan struct{}

	// coalescing
	pending   bool
	firstSeen time.Time
	timer     *time.Timer

	// degraded scanning
	degraded  bool
	scanStop  chan struct{}
	scanDone  chan struct{}
	lastHash  [sha256.Size]byte
	hasHash   bool
	backoffIx int
}

// Service owns physical watches for any number of subscription roots.
type Service struct {
	stderr io.Writer

	mu          sync.Mutex
	roots       map[string]*rootState
	nextID      uint64
	closed      bool
	stats       Diagnostics
	backend     backend
	backendKind string
	helper      *helperClient
}

// NewService builds the host watch service. The backend is chosen once: the
// helper process where available (Windows by default), otherwise in-process
// native watches.
func NewService(opts Options) *Service {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	svc := &Service{
		stderr: stderr,
		roots:  map[string]*rootState{},
		// Registration ids start at 1: zero means "no previous registration"
		// in the cancel-on-reregister bookkeeping.
		nextID: 1,
	}
	if opts.ScanOnly {
		// No platform backend, so no helper process on any platform.
		svc.backend, svc.backendKind = scanOnlyBackend{}, "scan-only"
		return svc
	}
	svc.backend, svc.backendKind, svc.helper = newPlatformBackend(svc, opts)
	return svc
}

// Subscribe adds one logical subscription for root. It registers the physical
// watches before returning (bounded on the helper path) so the caller's first
// scan cannot race the watch into missing changes. A registration failure
// degrades the root to backoff scanning instead of failing the store.
func (s *Service) Subscribe(root string, maxDepth int, scope ScopeFunc, hash HashFunc, onChange func(reason string)) *Subscription {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	sub := &Subscription{
		svc:      s,
		root:     root,
		maxDepth: maxDepth,
		onChange: onChange,
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		// A closed service hands out dead subscriptions; stores built during
		// teardown keep working from their initial scan.
		return sub
	}
	state := s.roots[root]
	if state == nil {
		state = &rootState{
			canonical: root, subs: map[*Subscription]struct{}{},
			maxDepth: maxDepth, scope: scope, hash: hash,
		}
		s.roots[root] = state
	}
	state.subs[sub] = struct{}{}
	raised := maxDepth > state.maxDepth
	if raised {
		state.maxDepth = maxDepth
	}
	s.stats.LogicalSubscriptions++
	if len(state.dirs) == 0 || raised || state.scopeDirty {
		s.registerRootLocked(state)
	}
	regDone := state.regDone
	kind := s.backendKind
	s.mu.Unlock()
	// Preserve register-before-scan ordering. Native and scan-only registration
	// are local and settle before Subscribe returns; helper confirmation travels
	// over a pipe, so only that path needs a timeout for a wedged child.
	if regDone != nil && (kind == "native" || kind == "scan-only") {
		<-regDone
	}
	if regDone != nil && kind == "helper" {
		timer := time.NewTimer(helperSubscribeWait)
		defer timer.Stop()
		select {
		case <-regDone:
		case <-timer.C:
		}
	}
	return sub
}

// registerRootLocked rebuilds the watch scope for one root and hands it to the
// backend. Caller holds s.mu. Registration is generation fenced: any result
// for an older generation is discarded. The superseded registration is
// cancelled once the new one settles, so a root keeps at most one live
// backend registration and physical watches never accumulate.
func (s *Service) registerRootLocked(state *rootState) {
	state.gen++
	gen := state.gen
	dirs, _ := state.scope(context.Background(), state.canonical, state.maxDepth)
	state.dirs = dirs
	state.scopeDirty = false
	id := s.nextID
	s.nextID++
	oldID := state.watchID
	state.watchID = id
	done := make(chan struct{})
	state.regDone = done
	go func() {
		defer close(done)
		err := s.backend.register(id, gen, state.canonical, dirs)
		if oldID != 0 {
			s.backend.cancel(oldID)
		}
		s.mu.Lock()
		if state.gen != gen {
			// Superseded by a newer registration, which owns cancelling id.
			s.mu.Unlock()
			return
		}
		if len(state.subs) == 0 {
			// Fully released while registering: reclaim our own registration.
			s.mu.Unlock()
			s.backend.cancel(id)
			return
		}
		if err != nil {
			s.degradeRootLocked(state, err)
			s.mu.Unlock()
			return
		}
		if state.degraded {
			s.stopScanLocked(state)
		}
		s.mu.Unlock()
	}()
}

func (s *Service) dropSubscription(sub *Subscription) {
	s.mu.Lock()
	state := s.roots[sub.root]
	if state == nil {
		s.mu.Unlock()
		return
	}
	if _, ok := state.subs[sub]; !ok {
		s.mu.Unlock()
		return
	}
	delete(state.subs, sub)
	if s.stats.LogicalSubscriptions > 0 {
		s.stats.LogicalSubscriptions--
	}
	if len(state.subs) > 0 {
		s.mu.Unlock()
		return
	}
	// Last subscription gone: release the physical watches. Reference counting
	// to zero is what keeps historical project roots from accumulating.
	id := state.watchID
	var scanDone chan struct{}
	if state.degraded {
		scanDone = state.scanDone
		s.stopScanLocked(state) // closes scanStop
	}
	s.cancelTimerLocked(state)
	delete(s.roots, sub.root)
	s.mu.Unlock()

	// Backend IO happens outside the lock: cancel never waits on registration.
	if id != 0 {
		s.backend.cancel(id)
	}
	if scanDone != nil {
		<-scanDone
	}
}

// eventArrived receives one raw backend event. Events for unknown, released or
// superseded registrations are dropped without producing a notification.
func (s *Service) eventArrived(id, rootGen uint64, op Op) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	var state *rootState
	for _, r := range s.roots {
		if r.watchID == id {
			state = r
			break
		}
	}
	if state == nil || state.gen != rootGen || len(state.subs) == 0 {
		return
	}
	s.stats.EventsReceived++
	if state.pending {
		// Extend the window unless that would exceed the maximum delay.
		if time.Since(state.firstSeen)+coalesceWindow <= coalesceMaxDelay {
			state.timer.Reset(coalesceWindow)
			if op&(OpCreate|OpRename) != 0 {
				state.scopeDirty = true
			}
			return
		}
		s.fireRootLocked(state)
	}
	state.pending = true
	state.firstSeen = time.Now()
	state.timer = time.AfterFunc(coalesceWindow, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if state.pending {
			s.fireRootLocked(state)
		}
	})
	if op&(OpCreate|OpRename) != 0 {
		// A create/rename can introduce a nested directory, a symlink target or
		// a previously missing root; rebuild subscriptions after notification.
		state.scopeDirty = true
	}
}

// fireRootLocked delivers the coalesced notification and applies a deferred
// scope rebuild. Caller holds s.mu.
func (s *Service) fireRootLocked(state *rootState) {
	s.cancelTimerLocked(state)
	state.pending = false
	s.stats.Notifications++
	if state.scopeDirty {
		s.registerRootLocked(state)
	}
	for sub := range state.subs {
		sub.onChange("filesystem changed")
	}
}

func (s *Service) cancelTimerLocked(state *rootState) {
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
}

// degradeRootLocked switches a root to bounded signature scanning. The helper
// restart budget is spent by the helper client before this point; degraded
// roots recover automatically when a later registration succeeds.
// Caller holds s.mu.
func (s *Service) degradeRootLocked(state *rootState, cause error) {
	if state.degraded {
		return
	}
	state.degraded = true
	s.stats.DegradedRoots++
	s.warnf("skillwatch: root %s degraded to scan fallback: %v", state.canonical, cause)
	state.scanStop = make(chan struct{})
	state.scanDone = make(chan struct{})
	stop, done := state.scanStop, state.scanDone
	go func() {
		s.scanLoop(state, stop, done)
	}()
}

func (s *Service) stopScanLocked(state *rootState) {
	if !state.degraded {
		return
	}
	state.degraded = false
	if s.stats.DegradedRoots > 0 {
		s.stats.DegradedRoots--
	}
	if state.scanStop != nil {
		close(state.scanStop)
		state.scanStop = nil
	}
	state.backoffIx = 0
	state.hasHash = false
}

// scanLoop is the degraded-mode replacement for the retired 250ms polling
// generation: one scan at a time per root, growing 2/5/15/30s backoff while
// the watch backend stays unavailable. A signature diff notifies subscribers
// exactly like a native event would, and every pass retries the backend so a
// recovered helper or filesystem returns the root to native watching.
func (s *Service) scanLoop(state *rootState, stop, done chan struct{}) {
	defer close(done)
	for {
		interval := s.nextScanInterval(state)
		select {
		case <-stop:
			return
		case <-time.After(interval):
		}
		s.mu.Lock()
		if s.closed || !state.degraded || len(state.subs) == 0 {
			s.mu.Unlock()
			return
		}
		gen, hash, depth := state.gen, state.hash, state.maxDepth
		s.mu.Unlock()

		sum, entries, ok := hash(context.Background(), state.canonical, depth)

		s.mu.Lock()
		s.stats.Scans++
		s.stats.ScannedEntries += uint64(entries)
		if state.gen != gen || !state.degraded {
			s.mu.Unlock()
			return
		}
		if ok && (!state.hasHash || sum != state.lastHash) {
			state.lastHash, state.hasHash = sum, true
			for sub := range state.subs {
				sub.onChange("filesystem changed (scan fallback)")
			}
		}
		// Every pass retries the watch backend: a recovered helper or
		// filesystem flips the root back to native watching, and
		// registerRootLocked itself stops the scan on success.
		s.registerRootLocked(state)
		s.mu.Unlock()
	}
}

func (s *Service) nextScanInterval(state *rootState) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix := state.backoffIx
	if ix >= len(scanBackoffs) {
		ix = len(scanBackoffs) - 1
	}
	state.backoffIx++
	return scanBackoffs[ix]
}

// helperDied tells the service the helper exhausted its restart budget (or
// never started): every live root re-registers, fails, and degrades to its
// scan fallback. Recovery from degraded state needs a working backend, which
// a degraded host regains only through the per-pass re-registration in
// scanLoop once the helper serves again.
func (s *Service) helperDied() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	states := make([]*rootState, 0, len(s.roots))
	for _, state := range s.roots {
		states = append(states, state)
	}
	for _, state := range states {
		if len(state.subs) > 0 {
			s.registerRootLocked(state)
		}
	}
}

func (s *Service) helperRestarted() {
	s.mu.Lock()
	s.stats.HelperRestarts++
	s.mu.Unlock()
}

func (s *Service) warnf(format string, args ...any) {
	// Diagnostics only: counts, root paths and failure causes, never contents.
	_, _ = fmt.Fprintf(s.stderr, format+"\n", args...)
}

// Diagnostics returns a snapshot of the resource counters.
func (s *Service) Diagnostics() Diagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	out.PhysicalWatches = s.backend.physicalWatches()
	out.DegradedRoots = 0
	for _, state := range s.roots {
		if state.degraded {
			out.DegradedRoots++
		}
	}
	return out
}

// Close tears the service down: every subscription dies, physical watches and
// the helper process are reclaimed. It is idempotent and safe to call during
// application exit; it never waits on uninterruptible backend registration.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	type teardown struct {
		id   uint64
		done chan struct{}
	}
	steps := make([]teardown, 0, len(s.roots)*2)
	for _, state := range s.roots {
		s.cancelTimerLocked(state)
		if state.degraded && state.scanStop != nil {
			close(state.scanStop)
			state.scanStop = nil
			steps = append(steps, teardown{done: state.scanDone})
		}
		if state.watchID != 0 {
			steps = append(steps, teardown{id: state.watchID})
		}
	}
	s.roots = map[string]*rootState{}
	s.stats.LogicalSubscriptions = 0
	s.mu.Unlock()
	for _, st := range steps {
		if st.id != 0 {
			s.backend.cancel(st.id)
		}
		if st.done != nil {
			<-st.done
		}
	}
	return s.backend.close()
}
