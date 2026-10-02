package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const pendingHotBytes = 16 << 20

type queueReservation struct {
	binding *PersistenceBinding
	bytes   int64
	mu      sync.Mutex
	state   uint8 // 0 reserved, 1 consumed, 2 released
}

func (r *queueReservation) release() {
	if r == nil || r.binding == nil {
		return
	}
	r.mu.Lock()
	if r.state != 0 {
		r.mu.Unlock()
		return
	}
	r.state = 2
	r.mu.Unlock()
	r.binding.mu.Lock()
	r.binding.reservedBytes -= r.bytes
	r.binding.notifySpaceLocked()
	r.binding.mu.Unlock()
}

// PersistenceBinding delivers accepted commits to one physical SessionHandle.
//
// It holds only the write-behind prefix of the same event sequence that Session
// owns; it is not a second source of business state. Delivery into the queue is
// a pure memory operation, so a slow disk can never extend the session commit
// lock or block cancellation.
type PersistenceBinding struct {
	// Immutable after construction: Session readers retain this reference while
	// Close shuts down the handle. Write admission is guarded by closed/accepting.
	handle SessionHandle
	dir    string

	// metadataSource rebuilds the list projection for the catalog cache once
	// the accepted prefix is fully durable. Session supplies it so the binding
	// never holds a projection of its own.
	metadataSource         func(durable uint64) (catalogMetadata, bool)
	recoverySource         func(durable uint64) (recoveryPublishState, bool)
	recoveryPublished      func(durable uint64)
	disableRecoveryPublish bool

	mu            sync.Mutex
	queue         []Commit
	durable       uint64
	timer         timerHandle
	draining      bool
	autoPaused    bool
	accepting     bool
	closed        bool
	writeErr      error
	uncertain     *uncertainWrite
	hotBytes      int64
	reservedBytes int64
	spaceChanged  chan struct{}

	drainMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error

	afterFunc func(time.Duration, func()) timerHandle
	writeFn   func(context.Context, io.Writer, []byte) error
	syncFn    func(*os.File) error
}

func newPersistenceBinding(handle SessionHandle, dir string, durable uint64, opts OpenOptions) *PersistenceBinding {
	after := opts.AfterFunc
	if after == nil {
		after = func(delay time.Duration, fire func()) timerHandle { return time.AfterFunc(delay, fire) }
	}
	writeFn := opts.Write
	if writeFn == nil {
		writeFn = writeAllContext
	}
	syncFn := opts.Sync
	if syncFn == nil {
		syncFn = func(file *os.File) error { return file.Sync() }
	}
	return &PersistenceBinding{
		handle: handle, dir: dir, durable: durable, accepting: true,
		afterFunc: after, writeFn: writeFn, syncFn: syncFn,
		spaceChanged: make(chan struct{}),
	}
}

func (b *PersistenceBinding) reserve(ctx context.Context, bytes int64) (*queueReservation, error) {
	if b == nil {
		return nil, osClosedError()
	}
	charge := min(max(bytes, 1), int64(pendingHotBytes))
	for {
		b.mu.Lock()
		if !b.accepting || b.closed {
			b.mu.Unlock()
			return nil, osClosedError()
		}
		if b.hotBytes+b.reservedBytes+charge <= pendingHotBytes {
			b.reservedBytes += charge
			reservation := &queueReservation{binding: b, bytes: charge}
			b.mu.Unlock()
			return reservation, nil
		}
		wait := b.spaceChanged
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (b *PersistenceBinding) notifySpaceLocked() {
	if b.spaceChanged == nil {
		b.spaceChanged = make(chan struct{})
		return
	}
	close(b.spaceChanged)
	b.spaceChanged = make(chan struct{})
}

// accept atomically adds an immutable commit to the write-behind queue and
// publishes it to Session through accepted. It performs no file I/O and calls
// accepted exactly once while the queue is locked. Keeping these two memory
// mutations in one boundary prevents a closed binding from rejecting a commit
// after Session has already exposed it, and prevents Flush from persisting a
// commit before Session exposes it.
func (b *PersistenceBinding) accept(commit Commit, reservation *queueReservation, accepted func()) error {
	if b == nil {
		return osClosedError()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.accepting || b.closed {
		if reservation != nil {
			reservation.mu.Lock()
			if reservation.state == 0 {
				reservation.state = 2
				b.reservedBytes -= reservation.bytes
				b.notifySpaceLocked()
			}
			reservation.mu.Unlock()
		}
		return osClosedError()
	}
	if reservation == nil || reservation.binding != b {
		return errors.New("session: missing or foreign queue reservation")
	}
	reservation.mu.Lock()
	if reservation.state != 0 {
		reservation.mu.Unlock()
		return errors.New("session: queue reservation is no longer valid")
	}
	reservation.state = 1
	reservation.mu.Unlock()
	b.reservedBytes -= reservation.bytes
	b.hotBytes += reservation.bytes
	// Session transfers an immutable commit here. Keep one shared payload backing
	// for the accepted log and write queue; cloning large bodies would turn the
	// queue budget into multiple hidden copies.
	b.queue = append(b.queue, commit)
	accepted()
	if !b.autoPaused && !b.draining && b.timer == nil {
		b.scheduleDrainLocked()
	}
	return nil
}

func commitHotBytes(commit Commit) int64 {
	var bytes int64 = 512
	for _, event := range commit.Events {
		bytes += int64(len(event.ID)+len(event.Kind)+len(event.Payload)) + 256
		if event.PayloadRef != nil {
			bytes += int64(len(event.PayloadRef.Digest)+len(event.PayloadRef.IndexDigest)+len(event.PayloadRef.MediaType)+len(event.PayloadRef.Name)) + 64
		}
	}
	return min(max(bytes, 1), int64(pendingHotBytes))
}

func (b *PersistenceBinding) stopAccepting() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.accepting = false
	b.notifySpaceLocked()
	b.mu.Unlock()
}

func (b *PersistenceBinding) scheduleDrainLocked() {
	b.timer = b.afterFunc(LiveBatchDelay, func() { _ = b.drain(context.Background(), false) })
}

// progress reports the durable watermark and whether the accepted prefix is
// fully persisted.
func (b *PersistenceBinding) progress() (uint64, PersistenceStatus, string) {
	if b == nil {
		return 0, PersistenceReady, ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	status := PersistenceReady
	if b.writeErr != nil {
		status = PersistenceFailed
		if errors.Is(b.writeErr, ErrPersistenceUncertain) {
			status = PersistenceUncertain
		}
	} else if len(b.queue) > 0 || b.draining {
		status = PersistencePending
	}
	return b.durable, status, errorString(b.writeErr)
}

// Flush drains the queue and reports the durable sequence. Cancelling one
// caller's wait never cancels the shared physical write.
func (b *PersistenceBinding) Flush(ctx context.Context) (DurableReceipt, error) {
	if b == nil {
		return DurableReceipt{}, fmt.Errorf("session: nil persistence binding")
	}
	if err := ctx.Err(); err != nil {
		return DurableReceipt{}, err
	}
	// Once an append starts, one caller cannot cancel the shared physical write.
	// drainMu merges callers onto the ordered write chain while each caller can
	// still stop waiting through its own context.
	done := make(chan error, 1)
	go func() { done <- b.drain(context.Background(), true) }()
	select {
	case err := <-done:
		return DurableReceipt{DurableSequence: b.durableSequence()}, err
	case <-ctx.Done():
		return DurableReceipt{DurableSequence: b.durableSequence()}, ctx.Err()
	}
}

// FlushThrough waits for one accepted commit boundary. Later writes may keep
// draining, but cannot extend this caller's snapshot or cancel its shared writer.
func (b *PersistenceBinding) FlushThrough(ctx context.Context, through uint64) (DurableReceipt, error) {
	if b == nil {
		return DurableReceipt{}, os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return DurableReceipt{}, err
	}
	b.mu.Lock()
	durable, changed := b.durable, b.spaceChanged
	b.mu.Unlock()
	if durable >= through {
		return DurableReceipt{DurableSequence: durable}, nil
	}
	done := make(chan error, 1)
	go func() { done <- b.drain(context.Background(), true) }()
	for {
		select {
		case <-ctx.Done():
			return DurableReceipt{DurableSequence: b.durableSequence()}, ctx.Err()
		case err := <-done:
			durable = b.durableSequence()
			if durable >= through {
				return DurableReceipt{DurableSequence: durable}, nil
			}
			if err == nil {
				err = fmt.Errorf("session: watermark %d was not accepted", through)
			}
			return DurableReceipt{DurableSequence: durable}, err
		case <-changed:
			b.mu.Lock()
			durable, changed = b.durable, b.spaceChanged
			b.mu.Unlock()
			if durable >= through {
				return DurableReceipt{DurableSequence: durable}, nil
			}
		}
	}
}

func (b *PersistenceBinding) durableSequence() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.durable
}

func (b *PersistenceBinding) drain(ctx context.Context, explicit bool) error {
	b.drainMu.Lock()
	defer b.drainMu.Unlock()
	for {
		b.mu.Lock()
		if b.closed || b.handle == nil {
			b.mu.Unlock()
			return os.ErrClosed
		}
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
		if len(b.queue) == 0 {
			b.draining = false
			b.mu.Unlock()
			b.refreshCatalogMetadata()
			return nil
		}
		uncertain := cloneUncertainWrite(b.uncertain)
		if uncertain != nil && !explicit {
			err := b.writeErr
			b.draining = false
			b.mu.Unlock()
			return err
		}
		b.draining = true
		// Detach only the queue header. Commit payloads are immutable after
		// acceptance and can be streamed by the physical writer without copying.
		pending := append([]Commit(nil), b.queue...)
		handle := b.handle
		b.mu.Unlock()

		alreadyPersisted := false
		var err error
		if uncertain != nil {
			alreadyPersisted, err = b.reconcileUncertain(ctx, handle, *uncertain)
		}
		if err == nil && alreadyPersisted {
			confirmed := uncertain.commitCount
			if confirmed <= 0 || confirmed > len(pending) {
				err = fmt.Errorf("%w: uncertain batch commit count %d exceeds pending prefix %d", ErrDamagedStore, confirmed, len(pending))
			} else {
				b.mu.Lock()
				if len(b.queue) < confirmed || !sameCommitPrefix(b.queue, pending[:confirmed]) {
					err = fmt.Errorf("%w: uncertain batch no longer matches pending prefix", ErrDamagedStore)
					b.writeErr = err
					b.autoPaused = true
					b.draining = false
					b.mu.Unlock()
					return err
				}
				for _, commit := range b.queue[:confirmed] {
					b.hotBytes -= commitHotBytes(commit)
				}
				b.queue = b.queue[confirmed:]
				b.notifySpaceLocked()
				b.durable = pending[confirmed-1].LastSequence()
				b.writeErr = nil
				b.uncertain = nil
				b.autoPaused = false
				if len(b.queue) == 0 {
					b.draining = false
					b.mu.Unlock()
					b.refreshCatalogMetadata()
					return nil
				}
				b.mu.Unlock()
				continue
			}
		}
		if err == nil && !alreadyPersisted {
			err = b.persist(ctx, handle, pending)
		}
		b.mu.Lock()
		if err != nil {
			b.draining = false
			b.writeErr = err
			var uncertainErr *uncertainAppendError
			if errors.As(err, &uncertainErr) {
				copy := uncertainErr.write
				b.uncertain = &copy
			}
			b.autoPaused = true
			b.mu.Unlock()
			return err
		}
		if len(b.queue) < len(pending) || !sameCommitPrefix(b.queue, pending) {
			b.draining = false
			b.writeErr = fmt.Errorf("%w: pending batch order changed", ErrDamagedStore)
			b.autoPaused = true
			err := b.writeErr
			b.mu.Unlock()
			return err
		}
		for _, commit := range b.queue[:len(pending)] {
			b.hotBytes -= commitHotBytes(commit)
		}
		b.queue = b.queue[len(pending):]
		b.notifySpaceLocked()
		b.durable = pending[len(pending)-1].LastSequence()
		b.writeErr = nil
		b.uncertain = nil
		b.autoPaused = false
		if len(b.queue) == 0 {
			b.draining = false
			b.mu.Unlock()
			b.refreshCatalogMetadata()
			return nil
		}
		// Match DSH's drain chain: once a batch starts writing, events accepted
		// during that write are drained immediately in the next physical batch.
		// The fixed 200ms window applies only to the first pending batch.
		b.mu.Unlock()
	}
}

func (b *PersistenceBinding) refreshCatalogMetadata() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed || len(b.queue) != 0 {
		b.mu.Unlock()
		return
	}
	durable := b.durable
	b.mu.Unlock()
	recoveryPublished := false
	if !b.disableRecoveryPublish && b.recoverySource != nil {
		if state, ok := b.recoverySource(durable); ok {
			if store, ok := b.handle.(*Store); ok && store.recovery != nil {
				if err := store.recovery.publish(context.Background(), state.checkpoint, state.operations); err == nil {
					recoveryPublished = true
				}
			}
		}
	}
	if b.metadataSource != nil {
		if metadata, ok := b.metadataSource(durable); ok {
			_ = writeCatalogMetadataForSession(filepath.Join(filepath.Dir(b.dir), ".query-cache", filepath.Base(b.dir)), b.dir, metadata)
		}
	}
	if (recoveryPublished || b.disableRecoveryPublish || b.recoverySource == nil) && b.recoveryPublished != nil {
		b.recoveryPublished(durable)
	}
}

func (b *PersistenceBinding) persist(ctx context.Context, handle SessionHandle, commits []Commit) error {
	return handle.Append(ctx, commits)
}

// reconcileUncertain proves whether the prior append completed before retrying.
// The writer lease and drainMu make this a single-owner repair operation. A
// partial tail is preserved byte-for-byte before truncation; a mismatching or
// unexpectedly extended tail remains recovery-required rather than guessed.
func (b *PersistenceBinding) reconcileUncertain(ctx context.Context, handle SessionHandle, uncertain uncertainWrite) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	physical, ok := handle.(*Store)
	if !ok || physical == nil {
		return false, fmt.Errorf("%w: handle cannot reconcile an uncertain append", ErrPersistenceUncertain)
	}
	file, err := physical.writableFile()
	if err != nil {
		return false, err
	}
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() < uncertain.start {
		return false, fmt.Errorf("%w: log shrank below uncertain offset %d", ErrPersistenceUncertain, uncertain.start)
	}
	tailLen := info.Size() - uncertain.start
	if tailLen == 0 {
		return false, nil
	}
	staged, err := os.Open(uncertain.stagedPath)
	if err != nil {
		return false, fmt.Errorf("%w: open staged append evidence: %w", ErrPersistenceUncertain, err)
	}
	defer staged.Close()
	stagedInfo, err := staged.Stat()
	if err != nil || stagedInfo.Size() != uncertain.stagedBytes {
		return false, fmt.Errorf("%w: staged append evidence changed: %w", ErrPersistenceUncertain, err)
	}
	if tailLen > uncertain.stagedBytes {
		return false, fmt.Errorf("%w: on-disk tail exceeds staged batch at offset %d", ErrPersistenceUncertain, uncertain.start)
	}
	matches, err := equalReaderPrefix(ctx, io.NewSectionReader(file, uncertain.start, tailLen), staged, tailLen)
	if err != nil {
		return false, fmt.Errorf("%w: inspect uncertain tail: %w", ErrPersistenceUncertain, err)
	}
	if tailLen == uncertain.stagedBytes && matches {
		if err := b.syncFn(file); err != nil {
			return false, &uncertainAppendError{cause: fmt.Errorf("fsync verified append: %w", err), write: uncertain}
		}
		// The uncertain fsync did not advance the physical index. Rebuild its
		// durable cursor before validating a queued successor, or the successor
		// would appear to start after a gap.
		if err := physical.rebuildWriterIndex(file); err != nil {
			return false, fmt.Errorf("%w: rebuild index after verified append: %w", ErrPersistenceUncertain, err)
		}
		_ = staged.Close()
		_ = os.Remove(uncertain.stagedPath)
		return true, nil
	}
	if tailLen < uncertain.stagedBytes && matches {
		backup, err := preserveAndTruncateTail(filepath.Join(b.dir, currentLogName), uncertain.start, "uncertain")
		if err != nil {
			return false, fmt.Errorf("%w: preserve partial tail: %w", ErrPersistenceUncertain, err)
		}
		_ = backup
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return false, fmt.Errorf("%w: seek repaired log: %w", ErrPersistenceUncertain, err)
		}
		_ = staged.Close()
		_ = os.Remove(uncertain.stagedPath)
		return false, nil
	}
	return false, fmt.Errorf("%w: on-disk tail does not match batch at offset %d", ErrPersistenceUncertain, uncertain.start)
}

func equalReaderPrefix(ctx context.Context, left, right io.Reader, length int64) (bool, error) {
	leftBuffer := make([]byte, 1<<20)
	rightBuffer := make([]byte, 1<<20)
	remaining := length
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		chunk := min(remaining, int64(len(leftBuffer)))
		ln, leftErr := io.ReadFull(left, leftBuffer[:chunk])
		rn, rightErr := io.ReadFull(right, rightBuffer[:chunk])
		if leftErr != nil || rightErr != nil {
			return false, errors.Join(leftErr, rightErr)
		}
		if ln != rn || !bytes.Equal(leftBuffer[:ln], rightBuffer[:rn]) {
			return false, nil
		}
		remaining -= int64(ln)
	}
	return true, nil
}

// freezePhysical runs fn while the physical write chain is idle. Export uses it
// so the copied bytes cannot advance between the manifest and the log.
func (b *PersistenceBinding) freezePhysical(fn func() error) error {
	if b == nil || fn == nil {
		return nil
	}
	b.drainMu.Lock()
	defer b.drainMu.Unlock()
	return fn()
}

// Close drains the queue and closes the physical handle. It is deliberately
// uncancellable and idempotent: every caller observes the same result.
func (b *PersistenceBinding) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.accepting = false
		needsFlush := len(b.queue) > 0 || b.uncertain != nil
		b.notifySpaceLocked()
		b.mu.Unlock()
		var flushErr error
		if needsFlush {
			_, flushErr = b.Flush(context.Background())
		}
		b.drainMu.Lock()
		defer b.drainMu.Unlock()
		b.mu.Lock()
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
		handle := b.handle
		b.closed = true
		b.notifySpaceLocked()
		b.mu.Unlock()
		var closeErr error
		if handle != nil {
			closeErr = handle.Close(context.Background())
		}
		b.closeErr = errors.Join(flushErr, closeErr)
	})
	return b.closeErr
}

func cloneUncertainWrite(in *uncertainWrite) *uncertainWrite {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
