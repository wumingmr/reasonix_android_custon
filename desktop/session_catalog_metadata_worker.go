package main

import "context"

// startCatalogMetadataRefresh owns at most one refresh and one pending wakeup.
// Requesting work never waits for the catalog writer. The caller cancels ctx
// and joins done before releasing the watcher lifecycle.
func startCatalogMetadataRefresh(ctx context.Context, sync func(context.Context)) (request func(), done <-chan struct{}) {
	requests := make(chan struct{}, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-ctx.Done():
				return
			case <-requests:
				if ctx.Err() != nil {
					return
				}
				sync(ctx)
			}
		}
	}()
	return func() {
		if ctx.Err() != nil {
			return
		}
		select {
		case requests <- struct{}{}:
		default:
		}
	}, finished
}
