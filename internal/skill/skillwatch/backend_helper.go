package skillwatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"sync"
	"time"

	"reasonix/internal/proc"
)

// helperClient speaks the pipe protocol to a watcher helper process. Control
// operations get helperControlTimeout; a helper that misses it is killed and
// rebuilt at most helperRestartLimit times per helperRestartWindow before the
// client reports unavailable and every root degrades to scan fallback.
//
// Cancel semantics: the service drops the logical subscription before cancel
// is sent, so late events for a cancelled registration are recognized by
// (id, rootGen) and dropped host-side even if the helper never saw the cancel.
type helperClient struct {
	start func(ctx context.Context) (helperProcess, error)
	svc   *Service

	mu         sync.Mutex
	proc       helperProcess
	active     map[uint64]activeRegistration
	restarts   []time.Time
	unusable   bool
	closed     bool
	restarting bool

	writeMu sync.Mutex
	confirm map[uint64]chan helperConfirm
}

type activeRegistration struct {
	rootGen uint64
	dirs    []string
}

type helperConfirm struct {
	id  uint64
	err error
}

func newHelperClient(start func(ctx context.Context) (helperProcess, error), svc *Service) *helperClient {
	c := &helperClient{
		start:   start,
		svc:     svc,
		active:  map[uint64]activeRegistration{},
		confirm: map[uint64]chan helperConfirm{},
	}
	c.spawn()
	return c
}

// defaultHelperCommand re-enters this executable through the internal helper
// entry. The helper is the same binary, so the protocol can never drift
// between host and helper.
func defaultHelperCommand(ctx context.Context) (helperProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// The helper is a console-less child of a GUI process, so it must be
	// spawned through internal/proc: a bare exec.Command leaves Windows to
	// allocate a console for it and flashes a window on every start.
	cmd := proc.CommandContext(ctx, exe)
	cmd.Env = append(os.Environ(), watchHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execHelper{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

const watchHelperEnv = "REASONIX_SKILL_WATCH_HELPER"

type execHelper struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (h *execHelper) Stdin() io.Writer  { return h.stdin }
func (h *execHelper) Stdout() io.Reader { return h.stdout }
func (h *execHelper) Wait() error       { return h.cmd.Wait() }
func (h *execHelper) Kill() error       { return h.cmd.Process.Kill() }

func (c *helperClient) spawn() {
	ctx := context.Background()
	proc, err := c.start(ctx)
	if err != nil {
		c.svc.warnf("skillwatch: helper start failed: %v", err)
		c.markUnavailable()
		return
	}
	c.mu.Lock()
	c.proc = proc
	c.mu.Unlock()
	go c.readLoop(proc)
}

// readLoop dispatches helper frames until the pipe closes.
func (c *helperClient) readLoop(proc helperProcess) {
	for {
		f, err := readFrame(proc.Stdout())
		if err != nil {
			c.processDied()
			return
		}
		switch f.Kind {
		case wireRegistered:
			c.resolve(f.ID, nil)
		case wireError:
			c.resolve(f.ID, errors.New(f.Msg))
		case wireEvent:
			c.svc.eventArrived(f.ID, f.RootGen, f.Op)
		case wireReady, wirePong, wireRegister, wireCancel, wireShutdown, wirePing:
			// Unsolicited control frames are ignored.
		}
	}
}

func (c *helperClient) resolve(id uint64, err error) {
	c.mu.Lock()
	ch := c.confirm[id]
	delete(c.confirm, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- helperConfirm{id: id, err: err}
	}
}

func (c *helperClient) send(f frame) error {
	c.mu.Lock()
	proc := c.proc
	c.mu.Unlock()
	if proc == nil {
		return errHelperStopped
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(proc.Stdin(), f)
}

func (c *helperClient) register(id, rootGen uint64, _ string, dirs []string) error {
	c.mu.Lock()
	if c.unusable || c.closed {
		c.mu.Unlock()
		return errHelperStopped
	}
	ch := make(chan helperConfirm, 1)
	c.confirm[id] = ch
	c.mu.Unlock()

	err := c.send(frame{Kind: wireRegister, ID: id, RootGen: rootGen, Dirs: dirs})
	if err != nil {
		c.processDied()
		return err
	}
	timer := time.NewTimer(helperControlTimeout)
	defer timer.Stop()
	select {
	case confirm := <-ch:
		if confirm.err == nil {
			// Record only confirmed registrations: physicalWatches must not
			// report watches before the helper has armed them.
			c.mu.Lock()
			c.active[id] = activeRegistration{rootGen: rootGen, dirs: dirs}
			c.mu.Unlock()
		}
		return confirm.err
	case <-timer.C:
		go c.restart()
		return fmt.Errorf("helper registration timed out after %s", helperControlTimeout)
	}
}

func (c *helperClient) cancel(id uint64) {
	// The logical subscription already died host-side; this only reclaims the
	// helper's descriptors and must never wait on the pipe.
	c.mu.Lock()
	delete(c.active, id)
	proc := c.proc
	c.mu.Unlock()
	if proc == nil {
		return
	}
	c.writeMu.Lock()
	_ = writeFrame(proc.Stdin(), frame{Kind: wireCancel, ID: id})
	c.writeMu.Unlock()
}

// restart rebuilds the helper within the restart budget and replays the active
// registrations so existing roots resume native watching without service churn.
// Concurrent triggers (read-loop EOF and a control timeout racing) collapse
// into one cycle via the restarting flag.
func (c *helperClient) restart() {
	c.mu.Lock()
	if c.closed || c.unusable || c.restarting {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	kept := c.restarts[:0]
	for _, ts := range c.restarts {
		if now.Sub(ts) <= helperRestartWindow {
			kept = append(kept, ts)
		}
	}
	c.restarts = kept
	if len(c.restarts) >= helperRestartLimit {
		c.mu.Unlock()
		c.markUnavailable()
		return
	}
	c.restarts = append(c.restarts, now)
	c.restarting = true
	if c.proc != nil {
		_ = c.proc.Kill()
		c.proc = nil
	}
	c.mu.Unlock()
	c.svc.helperRestarted()

	c.spawn()
	c.replay()

	c.mu.Lock()
	c.restarting = false
	// A death that arrived mid-restart collapsed into this cycle; if the
	// process is still gone (budget unspent), run one more cycle.
	needRestart := c.proc == nil && !c.unusable && !c.closed
	c.mu.Unlock()
	if needRestart {
		go c.restart()
	}
}

// replay re-registers every active root on the fresh helper. Failures leave
// the registration in place; the service-side control timeout and event fence
// treat the root as failed on its next interaction.
func (c *helperClient) replay() {
	c.mu.Lock()
	regs := make(map[uint64]activeRegistration, len(c.active))
	maps.Copy(regs, c.active)
	unusable := c.unusable
	c.mu.Unlock()
	if unusable {
		c.svc.helperDied()
		return
	}
	deadline := time.Now().Add(helperControlTimeout)
	for id, reg := range regs {
		if time.Now().After(deadline) {
			c.svc.helperDied()
			return
		}
		if err := c.register(id, reg.rootGen, "", reg.dirs); err != nil {
			c.svc.helperDied()
			return
		}
	}
}

func (c *helperClient) processDied() {
	c.mu.Lock()
	c.proc = nil
	pending := make([]chan helperConfirm, 0, len(c.confirm))
	for _, ch := range c.confirm {
		pending = append(pending, ch)
	}
	c.confirm = map[uint64]chan helperConfirm{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- helperConfirm{err: errHelperStopped}
	}
	c.restart()
}

func (c *helperClient) markUnavailable() {
	c.mu.Lock()
	if c.unusable {
		c.mu.Unlock()
		return
	}
	c.unusable = true
	c.mu.Unlock()
	c.svc.helperDied()
}

// physicalWatches reports the directories covered by live registrations. The
// helper owns the authoritative count; this is the host-side view used for
// diagnostics.
func (c *helperClient) physicalWatches() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n uint64
	for _, reg := range c.active {
		n += uint64(len(reg.dirs))
	}
	return n
}

var _ backend = (*helperClient)(nil)

func (c *helperClient) close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	proc := c.proc
	c.proc = nil
	c.mu.Unlock()
	if proc != nil {
		c.writeMu.Lock()
		_ = writeFrame(proc.Stdin(), frame{Kind: wireShutdown})
		c.writeMu.Unlock()
		// Grace period for a clean exit, then reclaim. Application exit must
		// never hang on the helper.
		done := make(chan struct{})
		go func() { _ = proc.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = proc.Kill()
			<-done
		}
	}
	return nil
}
