package provider

import (
	"context"
	"errors"
)

// StreamAuxiliary makes one search or summarization request. It buffers the
// response so failed partial summaries never leak into their caller.
func StreamAuxiliary(ctx context.Context, p Provider, req Request) (<-chan Chunk, error) {
	ctx = WithManagedRecovery(WithIndependentRequestAttemptCounter(ctx))
	out := make(chan Chunk)
	var aggregate Usage
	go func() {
		defer close(out)
		send := func(c Chunk) bool {
			select {
			case out <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		attemptCtx, cancel := context.WithCancel(ctx)
		ch, err := Stream(attemptCtx, p, req)
		var latest *Usage
		var chunks []Chunk
		complete := false
		bytes := 0
		if err == nil {
		loop:
			for {
				select {
				case <-ctx.Done():
					cancel()
					return
				case c, ok := <-ch:
					if !ok {
						break loop
					}
					if c.Type == ChunkError {
						err = c.Err
						if err == nil {
							err = errors.New("auxiliary provider error")
						}
						break loop
					}
					bytes += len(c.Text)
					if bytes > 16*1024*1024 {
						err = errors.New("auxiliary response exceeds local limit")
						break loop
					}
					if c.Type == ChunkUsage {
						latest = c.Usage
						continue
					}
					complete = complete || c.Type == ChunkDone
					chunks = append(chunks, c)
				}
			}
			if err == nil && !complete {
				err = StreamInterrupt(errors.New("auxiliary response ended before terminal event"), "unexpected_eof")
			}
		}
		cancel()
		if latest == nil {
			aggregate.Unknown = true
		}
		if latest != nil {
			aggregate = *latest
		}
		aggregate.RequestCount = RequestAttemptCount(ctx)
		if aggregate.RequestCount == 0 {
			aggregate.RequestCount = 1
		}
		if err == nil {
			send(Chunk{Type: ChunkUsage, Usage: &aggregate})
			for _, c := range chunks {
				if !send(c) {
					return
				}
			}
			return
		}
		send(Chunk{Type: ChunkUsage, Usage: &aggregate})
		send(Chunk{Type: ChunkError, Err: err})
	}()
	return out, nil
}
