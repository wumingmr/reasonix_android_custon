package control

import (
	"context"
	"sync"
)

type imageReplayWarningsKey struct{}

// Admission owns warning lifetime across sampling rounds. Only redacted causes
// are remembered; image processing still retries on every request.
type imageReplayWarnings struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func withImageReplayWarnings(ctx context.Context) context.Context {
	return context.WithValue(ctx, imageReplayWarningsKey{}, &imageReplayWarnings{seen: make(map[string]struct{})})
}

func (w *imageReplayWarnings) first(detail string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.seen[detail]; ok {
		return false
	}
	w.seen[detail] = struct{}{}
	return true
}
