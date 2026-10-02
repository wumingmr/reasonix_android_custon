// Package historywork bounds reconstructable history work independently of
// persistence and runtime ownership. Cancelling a reader never cancels a save.
package historywork

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	BatchEntries   = 128
	BatchBytes     = 4 << 20
	ReadChunk      = 64 << 10
	BytesPerSecond = 8 << 20
	SliceDuration  = 50 * time.Millisecond
	PauseDuration  = 100 * time.Millisecond
)

// Coordinator is shared by the catalog and historical source discovery.
// Background permits are held for one bounded slice, never a directory scan.
type Coordinator struct {
	once            sync.Once
	background      chan struct{}
	foreground      chan struct{}
	mu              sync.Mutex
	readers         int
	changed         chan struct{}
	backgroundReady time.Time
	readBytes       atomic.Int64
	readCalls       atomic.Uint64
	canceledReads   atomic.Uint64
	slices          atomic.Uint64
	lastSliceNanos  atomic.Int64
}

func (c *Coordinator) init() {
	c.once.Do(func() {
		c.background = make(chan struct{}, 1)
		c.foreground = make(chan struct{}, 1)
		c.changed = make(chan struct{})
	})
}

func (c *Coordinator) Foreground(ctx context.Context) (func(), error) {
	c.init()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case c.foreground <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-c.foreground
		return nil, err
	}
	c.mu.Lock()
	c.readers++
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.readers--
			close(c.changed)
			c.changed = make(chan struct{})
			c.mu.Unlock()
			<-c.foreground
		})
	}, nil
}

func (c *Coordinator) Background(ctx context.Context) (func(), error) {
	release, err := c.BackgroundSlice(ctx, false)
	if err != nil {
		return nil, err
	}
	return func() { release(0) }, nil
}

func (c *Coordinator) ForegroundActive() bool {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readers > 0
}

// BackgroundSlice applies one shared rate budget across all discovery owners.
// P1 metadata may proceed during a foreground preparation; P2 waits for it.
func (c *Coordinator) BackgroundSlice(ctx context.Context, foregroundAllowed bool) (func(int64), error) {
	return c.backgroundSlice(ctx, foregroundAllowed, false)
}

var ErrForegroundActive = errors.New("foreground history preparation active")

// A single scheduling loop must be able to reconsider P1 while P2 is paused.
// Yielding preserves the caller's iterator; this is not a failed slice.
func (c *Coordinator) BackgroundSliceYielding(ctx context.Context, foregroundAllowed bool) (func(int64), error) {
	return c.backgroundSlice(ctx, foregroundAllowed, true)
}

func (c *Coordinator) backgroundSlice(ctx context.Context, foregroundAllowed, yield bool) (func(int64), error) {
	c.init()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		busy, changed, ready := c.readers > 0 && !foregroundAllowed, c.changed, c.backgroundReady
		c.mu.Unlock()
		if busy {
			if yield {
				return nil, ErrForegroundActive
			}
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if delay := time.Until(ready); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		select {
		case c.background <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		c.mu.Lock()
		foregroundBusy := c.readers > 0 && !foregroundAllowed
		busy = foregroundBusy || time.Now().Before(c.backgroundReady)
		c.mu.Unlock()
		if busy {
			<-c.background
			if foregroundBusy && yield {
				return nil, ErrForegroundActive
			}
			continue
		}
		var once sync.Once
		started := time.Now()
		return func(bytes int64) {
			once.Do(func() {
				c.slices.Add(1)
				c.lastSliceNanos.Store(time.Since(started).Nanoseconds())
				c.mu.Lock()
				c.backgroundReady = time.Now().Add(max(PauseDuration, time.Duration(max(0, bytes))*time.Second/BytesPerSecond))
				c.mu.Unlock()
				<-c.background
			})
		}, nil
	}
}

// Pause includes both the per-slice yield and the byte-rate allowance. Waiting
// is cancellable; tests may inject a clock through a caller's slice runner.
func Pause(ctx context.Context, bytes int64) error {
	delay := max(PauseDuration, time.Duration(bytes)*time.Second/BytesPerSecond)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reader checks cancellation before each bounded source read, including reads
// performed inside a JSON decoder rather than only between decoded records.
type Reader struct {
	Context context.Context
	Source  io.Reader
	Bytes   int64
}

func (r *Reader) Read(p []byte) (int, error) {
	meter, _ := r.Context.Value(meterKey{}).(*Coordinator)
	if err := r.Context.Err(); err != nil {
		if meter != nil {
			meter.canceledReads.Add(1)
		}
		return 0, err
	}
	if len(p) > ReadChunk {
		p = p[:ReadChunk]
	}
	n, err := r.Source.Read(p)
	r.Bytes += int64(n)
	if meter != nil {
		meter.readBytes.Add(int64(n))
		meter.readCalls.Add(1)
	}
	return n, err
}

type meterKey struct{}

func (c *Coordinator) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, meterKey{}, c)
}

// Counters contain no source identities or transcript text. Bytes counts only
// reads routed through Reader, rather than pretending a stat size was read.
type Diagnostics struct {
	ForegroundActive        int     `json:"foregroundActive"`
	BackgroundActive        int     `json:"backgroundActive"`
	BackgroundSlices        uint64  `json:"backgroundSlices"`
	InstrumentedReadBytes   int64   `json:"instrumentedReadBytes"`
	InstrumentedReadCalls   uint64  `json:"instrumentedReadCalls"`
	CanceledReadCheckpoints uint64  `json:"canceledReadCheckpoints"`
	LastBackgroundSliceMS   float64 `json:"lastBackgroundSliceMs"`
}

func (c *Coordinator) Diagnostics() Diagnostics {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	return Diagnostics{ForegroundActive: c.readers, BackgroundActive: len(c.background), BackgroundSlices: c.slices.Load(), InstrumentedReadBytes: c.readBytes.Load(), InstrumentedReadCalls: c.readCalls.Load(), CanceledReadCheckpoints: c.canceledReads.Load(), LastBackgroundSliceMS: float64(c.lastSliceNanos.Load()) / float64(time.Millisecond)}
}
