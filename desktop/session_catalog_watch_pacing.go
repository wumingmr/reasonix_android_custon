package main

import "time"

const (
	catalogWatchBatchDelay = 250 * time.Millisecond
	catalogRootPaceInitial = time.Second
	catalogRootPaceMax     = time.Minute
)

// catalogRootPacer bounds full-root rescans per directory. A root invalidated
// again soon after its last admission waits twice as long as before, so a
// trigger that never settles converges to one scan per catalogRootPaceMax.
type catalogRootPacer struct {
	roots map[string]catalogRootPace
}

type catalogRootPace struct {
	last      time.Time
	delay     time.Duration
	expedited bool // a session file was removed or renamed since the last admission
}

func (p *catalogRootPacer) due(key string) time.Time {
	pace := p.roots[key]
	if pace.expedited {
		return time.Time{}
	}
	return pace.last.Add(pace.delay)
}

// expedite admits the root's next scan at once without growing its backoff:
// a vanished transcript is user-visible and must not wait behind churn.
func (p *catalogRootPacer) expedite(key string) {
	if p == nil {
		return
	}
	if p.roots == nil {
		p.roots = map[string]catalogRootPace{}
	}
	pace := p.roots[key]
	pace.expedited = true
	p.roots[key] = pace
}

func (p *catalogRootPacer) forget(key string) {
	if p != nil {
		delete(p.roots, key)
	}
}

func (p *catalogRootPacer) admitted(key string, now time.Time) {
	if p.roots == nil {
		p.roots = map[string]catalogRootPace{}
	}
	pace := p.roots[key]
	if pace.expedited {
		pace.expedited, pace.last = false, now
		p.roots[key] = pace
		return
	}
	if pace.last.IsZero() || now.Sub(pace.last) >= 2*catalogRootPaceMax {
		pace.delay = 0
	} else {
		pace.delay = min(max(2*pace.delay, catalogRootPaceInitial), catalogRootPaceMax)
	}
	pace.last = now
	p.roots[key] = pace
}

// catalogWatchBatchTimer keeps one pending batch wake-up, moved earlier when a
// new invalidation needs a sooner batch than the deferred one already armed.
type catalogWatchBatchTimer struct {
	timer *time.Timer
	ready <-chan time.Time
	at    time.Time
}

func (b *catalogWatchBatchTimer) arm(wait time.Duration) {
	at := time.Now().Add(wait)
	if b.ready != nil && !at.Before(b.at) {
		return
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer, b.at = time.NewTimer(wait), at
	b.ready = b.timer.C
}

func (b *catalogWatchBatchTimer) stop() {
	if b.timer != nil {
		b.timer.Stop()
	}
}
