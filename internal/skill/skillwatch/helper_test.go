package skillwatch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// pipeHelperProcess adapts an in-goroutine RunHelper to the helperProcess
// interface, so the full pipe protocol is exercised without re-exec.
type pipeHelperProcess struct {
	stdin  io.Writer
	stdout io.Reader
	done   chan struct{}
	once   sync.Once
	fail   func()
}

func (p *pipeHelperProcess) Stdin() io.Writer  { return p.stdin }
func (p *pipeHelperProcess) Stdout() io.Reader { return p.stdout }
func (p *pipeHelperProcess) Wait() error {
	<-p.done
	return nil
}
func (p *pipeHelperProcess) Kill() error {
	p.once.Do(func() {
		if p.fail != nil {
			p.fail()
		}
	})
	return nil
}

// startRunHelper pipes RunHelper directly. Killing the "process" closes the
// pipes, which unblocks RunHelper's reader and tears the helper down.
func startRunHelper(ctx context.Context) (helperProcess, error) {
	// io.Pipe() returns (reader, writer).
	hostRead, helperToHost := io.Pipe()    // helper writes -> host reads
	helperFromHost, hostWrite := io.Pipe() // host writes -> helper reads
	done := make(chan struct{})
	h := &pipeHelperProcess{stdin: hostWrite, stdout: hostRead, done: done}
	go func() {
		defer close(done)
		_ = RunHelper(helperFromHost, helperToHost)
	}()
	h.fail = func() {
		_ = hostWrite.Close()
		_ = helperFromHost.Close()
		_ = helperToHost.Close()
		_ = hostRead.Close()
	}
	return h, nil
}

func TestHelperProtocolEvents(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(Options{Stderr: io.Discard, ForceHelper: true, HelperCommand: startRunHelper})
	defer svc.Close()

	var hits int
	var mu sync.Mutex
	svc.Subscribe(dir, 2, countingScope, flatHash, func(string) { mu.Lock(); hits++; mu.Unlock() })

	// Subscribe waits (bounded) for the helper's registered confirmation; a
	// write after that must arrive as an event and coalesce into one notify.
	waitFor(t, "helper registration", func() bool {
		return svc.helper != nil && svc.helper.physicalWatches() > 0
	})
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "helper event notification", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits >= 1
	})
}

func TestHelperRestartBudgetDegradesThenScans(t *testing.T) {
	dir := t.TempDir()
	var procMu sync.Mutex
	var current *pipeHelperProcess
	svc := NewService(Options{Stderr: io.Discard, ForceHelper: true, HelperCommand: func(ctx context.Context) (helperProcess, error) {
		h, err := startRunHelper(ctx)
		if err != nil {
			return nil, err
		}
		procMu.Lock()
		current = h.(*pipeHelperProcess)
		procMu.Unlock()
		return h, nil
	}})
	defer svc.Close()

	var hits int
	var mu sync.Mutex
	svc.Subscribe(dir, 2, countingScope, flatHash, func(string) { mu.Lock(); hits++; mu.Unlock() })
	waitFor(t, "initial registration", func() bool {
		return svc.helper.physicalWatches() > 0
	})

	// Kill the helper repeatedly: restart 1, restart 2, then the budget is
	// spent and the service must degrade this root to scan fallback.
	for i := range 3 {
		procMu.Lock()
		proc := current
		procMu.Unlock()
		if proc != nil {
			proc.Kill()
		}
		if i < 2 {
			waitFor(t, "restart to re-register", func() bool {
				return svc.helper.physicalWatches() > 0 && svc.Diagnostics().HelperRestarts == uint64(i+1)
			})
		}
	}
	waitFor(t, "degraded root after restart budget", func() bool {
		return svc.Diagnostics().DegradedRoots == 1
	})

	// Degraded mode still reports changes through the bounded scans.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("after degradation"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "scan fallback notification after degradation", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits >= 1
	})
	// Close must reclaim the degraded scan loop and the dead helper cleanly.
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSubscribeOnDegradedHelperStillServesScans(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(Options{Stderr: io.Discard, ForceHelper: true, HelperCommand: func(ctx context.Context) (helperProcess, error) {
		return nil, errHelperStopped
	}})
	defer svc.Close()

	var hits int
	var mu sync.Mutex
	svc.Subscribe(dir, 2, countingScope, flatHash, func(string) { mu.Lock(); hits++; mu.Unlock() })
	if diag := svc.Diagnostics(); diag.PhysicalWatches != 0 {
		t.Fatalf("physical watches without helper = %d, want 0", diag.PhysicalWatches)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "degraded scan fallback", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits >= 1
	})
}

func TestLateEventsForReleasedSubscriptionDropped(t *testing.T) {
	dir := t.TempDir()
	svc := NewService(Options{Stderr: io.Discard})
	defer svc.Close()

	var hits int
	var mu sync.Mutex
	sub := svc.Subscribe(dir, 2, countingScope, flatHash, func(string) { mu.Lock(); hits++; mu.Unlock() })
	sub.Release()

	// Feed an event that references the (now gone) registration; the service
	// must not notify and must not panic.
	svc.eventArrived(9999, 1, OpWrite)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Fatalf("released subscription notified %d times", hits)
	}
}
