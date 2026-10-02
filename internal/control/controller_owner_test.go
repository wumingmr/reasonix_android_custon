package control

import (
	"context"
	"testing"
)

func newOwnedTestController(t testing.TB, options Options) *Controller {
	t.Helper()
	controller := New(options)
	t.Cleanup(func() {
		controller.Close()
		// Close only starts teardown; finalizeControllerClose joins the workers
		// that may still write under SessionDir. Waiting keeps a late Flush from
		// racing t.TempDir removal, so it is unconditional.
		<-controller.closeFinalized
		if options.SessionService == nil {
			return
		}
		// Close an idle retained runtime when this is the final owner. A runtime
		// still bound by another controller belongs to that controller's cleanup.
		_ = options.SessionService.CloseAll(context.Background())
	})
	return controller
}
