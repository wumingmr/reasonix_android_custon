package provider

import (
	"context"
	"errors"
	"io"
	"net/http/httptrace"
	"sync/atomic"
)

// connectionProbe records what the transport did with one request attempt:
// whether it went out on a pooled connection that had been idle, and whether
// any response byte came back on it.
type connectionProbe struct {
	idleReuse atomic.Bool
	responded atomic.Bool
}

func (p *connectionProbe) attach(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			p.idleReuse.Store(info.Reused && info.WasIdle)
		},
		GotFirstResponseByte: func() { p.responded.Store(true) },
	})
}

// staleIdleConnection reports whether err is a pooled idle connection that the
// peer or an intermediary had already dropped: it closed before answering
// anything. The request may still have been delivered, so the caller resends
// at most once.
func (p *connectionProbe) staleIdleConnection(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || !p.idleReuse.Load() || p.responded.Load() {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	for _, closed := range closedConnectionErrnos {
		if errors.Is(err, closed) {
			return true
		}
	}
	return false
}
