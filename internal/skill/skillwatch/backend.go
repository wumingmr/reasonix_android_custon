package skillwatch

import (
	"errors"
	"io"
)

// errHelperStopped reports a helper backend that cannot serve registrations:
// process dead and out of restart budget, or never started.
var errHelperStopped = errors.New("watch helper stopped")

// helperProcess is the minimal process handle the helper client needs; it
// exists so tests can inject a re-exec without depending on os/exec here.
type helperProcess interface {
	Stdin() io.Writer
	Stdout() io.Reader
	Wait() error
	Kill() error
}

// backend abstracts the physical watch mechanism. register may block (the
// helper path waits for the pipe round trip), so the service always calls it
// from its own goroutine; cancel is fire-and-forget and close is terminal.
type backend interface {
	register(id, rootGen uint64, root string, dirs []string) error
	cancel(id uint64)
	close() error
	physicalWatches() uint64
}

// errScanOnly reports a service that was asked for no physical watches.
var errScanOnly = errors.New("scan-only service has no physical watches")

// scanOnlyBackend refuses every registration, which is how the service already
// handles a backend it cannot use: the root degrades to backoff scanning and
// the store keeps working from its scans. Options.ScanOnly selects it so a
// caller can hold a real, closable service without a helper process.
type scanOnlyBackend struct{}

func (scanOnlyBackend) register(uint64, uint64, string, []string) error { return errScanOnly }
func (scanOnlyBackend) cancel(uint64)                                   {}
func (scanOnlyBackend) close() error                                    { return nil }
func (scanOnlyBackend) physicalWatches() uint64                         { return 0 }
