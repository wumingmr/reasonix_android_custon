package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/event"
)

// ForkAvailability names why one source turn can or cannot start a child
// session. A surface shows the reason instead of collapsing every refusal into
// one "unavailable" message.
type ForkAvailability string

const (
	// ForkAvailable means the turn closed at an atomic commit boundary.
	ForkAvailable ForkAvailability = "available"
	// ForkTurnOpen means the turn has no terminal turn/end record yet.
	ForkTurnOpen ForkAvailability = "turn_open"
	// ForkActiveAuthority means the cut would inherit in-flight execution state.
	ForkActiveAuthority ForkAvailability = "active_authority"
	// ForkHistoryUnverifiable means the source keeps no persisted turn records,
	// so no boundary can be proven. Text matching, elapsed time, or a turn that
	// merely looks finished must never substitute for one.
	ForkHistoryUnverifiable ForkAvailability = "history_unverifiable"
	// ForkStaleSource means the request no longer addresses the session boundary
	// the caller displayed.
	ForkStaleSource ForkAvailability = "stale_source"
	// ForkUnsupported means this surface has no create-only session fork.
	ForkUnsupported ForkAvailability = "unsupported"
)

// ForkTarget is one source turn a client may fork from. It is derived only from
// committed events, so the same source yields the same targets whether it is
// live in this process, owned by another process, or read cold from disk.
type ForkTarget struct {
	TurnID        string `json:"turnId"`
	TurnNumber    int    `json:"turnNumber"`
	StartSequence uint64 `json:"startSequence"`
	EndSequence   uint64 `json:"endSequence"`
	// BoundarySequence is the complete atomic commit boundary observed by the
	// caller. CreateFork requires the same value so a delayed request cannot be
	// reinterpreted against another projection.
	BoundarySequence uint64           `json:"boundarySequence"`
	Status           event.TurnStatus `json:"status"`
	// MessageID is the stable transcript identity of this turn's final assistant
	// reply, empty when the turn committed none.
	MessageID string           `json:"messageId,omitempty"`
	Available bool             `json:"available"`
	Reason    ForkAvailability `json:"reason,omitempty"`
}

// ForkTargetSet is the fork state of one source session. Targets stay empty and
// Verifiable stays false for legacy history that keeps messages without turn
// records, which is what lets a surface say the boundary is unverifiable
// instead of offering a cut it cannot prove.
type ForkTargetSet struct {
	Source     SessionRef   `json:"source"`
	Targets    []ForkTarget `json:"targets"`
	Verifiable bool         `json:"verifiable"`
}

func forkProjectionAvailability(projection Projection, boundary uint64) ForkAvailability {
	if boundary == 0 || boundary != projection.CommittedSequence {
		return ForkHistoryUnverifiable
	}
	if projection.TurnID != "" || len(projection.Interactions) != 0 || len(projection.ActiveTools) != 0 {
		return ForkActiveAuthority
	}
	return ForkAvailable
}

// ForkTargets lists the source's turns in display order. The open turn is
// included as ForkTurnOpen so a surface can explain why its own turn is not
// forkable yet without disabling the turns that already finished.
func ForkTargets(projection Projection) ForkTargetSet {
	targets := make([]ForkTarget, 0, len(projection.Turns)+1)
	for _, turn := range projection.Turns {
		if projection.HiddenTurns[turn.TurnID] {
			continue
		}
		target := ForkTarget{
			TurnID: turn.TurnID, TurnNumber: len(targets) + 1,
			StartSequence: turn.StartSequence, EndSequence: turn.EndSequence,
			BoundarySequence: turn.BoundarySequence,
			Status:           turn.Status, MessageID: turn.MessageID,
			Available: turn.Availability == ForkAvailable,
		}
		if !target.Available && turn.Availability != "" {
			target.Reason = turn.Availability
		} else if !target.Available {
			target.Reason = ForkHistoryUnverifiable
		}
		targets = append(targets, target)
	}
	if projection.TurnID != "" {
		targets = append(targets, ForkTarget{
			TurnID: projection.TurnID, TurnNumber: len(targets) + 1,
			StartSequence: projection.CurrentTurnStart, Status: projection.TurnStatus,
			MessageID: projection.CurrentTurnMessageID, Reason: ForkTurnOpen,
		})
	}
	return ForkTargetSet{Targets: targets, Verifiable: len(projection.Turns) > 0 || projection.TurnID != ""}
}

// ForkSequence resolves the cut for one turn identity. Only a turn that closed
// at an atomic commit boundary resolves; an open turn and an unknown identity
// are refused rather than silently redirected to the newest turn.
func ForkSequence(projection Projection, turnID string) (uint64, ForkAvailability, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return 0, "", fmt.Errorf("session: fork needs a turn id")
	}
	if projection.HiddenTurns[turnID] {
		return 0, ForkHistoryUnverifiable, nil
	}
	if projection.TurnID == turnID {
		return 0, ForkTurnOpen, nil
	}
	for _, turn := range projection.Turns {
		if turn.TurnID != turnID {
			continue
		}
		if turn.Availability != ForkAvailable {
			reason := turn.Availability
			if reason == "" {
				reason = ForkHistoryUnverifiable
			}
			return 0, reason, nil
		}
		return turn.BoundarySequence, ForkAvailable, nil
	}
	return 0, ForkHistoryUnverifiable, nil
}

// ForkSequenceForNumber resolves the cut for a display turn number. It exists
// only for clients that still address turns by their 1-based position; new
// clients carry the stable turn identity instead.
func ForkSequenceForNumber(projection Projection, turn int) (uint64, ForkAvailability, error) {
	set := ForkTargets(projection)
	if turn < 1 || turn > len(set.Targets) {
		return 0, ForkHistoryUnverifiable, fmt.Errorf("session: turn %d is not a forkable turn", turn)
	}
	return ForkSequence(projection, set.Targets[turn-1].TurnID)
}

// ForkTargetSetFor reads the fork state of one session without requiring a live
// runtime. A session owned by another process is read from its durable commits
// and keeps its lease untouched.
func (s *Service) ForkTargetSetFor(ctx context.Context, ref SessionRef) (ForkTargetSet, error) {
	if s == nil {
		return ForkTargetSet{}, fmt.Errorf("session: nil service")
	}
	if err := ref.validate(s.hostID); err != nil {
		return ForkTargetSet{}, err
	}
	projection, err := s.forkTurnProjection(ctx, ref)
	if err != nil {
		return ForkTargetSet{}, err
	}
	set := ForkTargets(projection)
	set.Source = ref
	return set, nil
}

// forkTurnProjection reads only the projection a fork resolves its cut from. A
// live runtime already holds that projection in memory, so it is read without
// reconstructing the durable transcript: surfaces refresh their fork state
// after every turn, next to the running turn. A session with no runtime in this
// process is read cold from its durable commits, which keeps the lease of the
// process that owns it untouched.
func (s *Service) forkTurnProjection(ctx context.Context, ref SessionRef) (Projection, error) {
	if runtime, ok := s.Runtime(ref); ok {
		return runtime.Session().ExecutionSnapshot().Projection, nil
	}
	snapshot, err := s.query.Snapshot(ctx, ref)
	if err != nil {
		return Projection{}, err
	}
	return snapshot.Projection, nil
}

// ForkUnavailableError reports a refused cut together with the reason a surface
// shows. Every refusal keeps its own reason so "still running", "read-only" and
// "no boundary" never collapse into one message.
type ForkUnavailableError struct {
	TurnID string
	Reason ForkAvailability
}

func (e *ForkUnavailableError) Error() string {
	return fmt.Sprintf("session: turn %q cannot start a fork (%s)", e.TurnID, e.Reason)
}

// ForkRequest identifies one create-a-child-session request. The host resolves
// the cut from persisted turn records and requires BoundarySequence to match
// the atomic boundary the client observed. Array positions and checkpoint
// numbers are never accepted as authority.
type ForkRequest struct {
	Source SessionRef
	// TurnID is the stable identity of the completed turn to cut after.
	TurnID string
	// BoundarySequence is the exact atomic boundary the client observed for the
	// turn. The host re-resolves it and refuses a stale or reinterpreted anchor.
	BoundarySequence uint64
	// ChildID is optional; the host mints one when empty.
	ChildID string
	// OperationID identifies this creation request. A retried submission with
	// the same operation id addresses the same child instead of minting a
	// second fork of one turn.
	OperationID string
}

// ForkResult reports the created child and the turn it was cut at.
type ForkResult struct {
	Child SessionRef
	Turn  ForkTarget
}

// CreateFork publishes an independent child session from one completed turn of
// the source. It never switches, closes, or writes to the source: a running
// parent keeps running, and a parent owned by another process keeps its lease.
func (s *Service) CreateFork(ctx context.Context, request ForkRequest) (ForkResult, error) {
	if s == nil {
		return ForkResult{}, fmt.Errorf("session: nil service")
	}
	if err := request.Source.validate(s.hostID); err != nil {
		return ForkResult{}, err
	}
	filesystem, ok := s.persistence.(*FilesystemPersistence)
	if !ok {
		return ForkResult{}, errors.New("session: persistence does not support filesystem fork")
	}
	projection, err := s.forkTurnProjection(ctx, request.Source)
	if err != nil {
		return ForkResult{}, err
	}
	sequence, availability, err := ForkSequence(projection, request.TurnID)
	if err != nil {
		return ForkResult{}, err
	}
	if availability != ForkAvailable {
		return ForkResult{}, &ForkUnavailableError{TurnID: request.TurnID, Reason: availability}
	}
	if request.BoundarySequence == 0 || request.BoundarySequence != sequence {
		return ForkResult{}, &ForkUnavailableError{TurnID: request.TurnID, Reason: ForkStaleSource}
	}
	target, ok := forkTargetByID(projection, request.TurnID)
	if !ok {
		return ForkResult{}, &ForkUnavailableError{TurnID: request.TurnID, Reason: ForkHistoryUnverifiable}
	}
	childID := strings.TrimSpace(request.ChildID)
	if childID == "" {
		if operation := strings.TrimSpace(request.OperationID); operation != "" {
			childID = deterministicID("fork\x00" + request.Source.SessionID + "\x00" + request.TurnID + "\x00" + operation)
		} else {
			childID = randomID()
		}
	}
	if err := validateSessionID(childID); err != nil {
		return ForkResult{}, err
	}
	parent, closeParent, err := s.forkSource(ctx, request.Source)
	if err != nil {
		return ForkResult{}, err
	}
	defer closeParent()
	childRef := SessionRef{HostID: s.hostID, SessionID: childID}
	childDir := filepath.Join(filesystem.Root, childID)
	parentDir := parent.dir()
	if matched, readErr := forkChildMatches(childDir, parentDir, sequence); readErr == nil {
		// A retried request reuses the child it already published. A different
		// session that merely holds this identity is a real conflict.
		if matched {
			return ForkResult{Child: childRef, Turn: target}, nil
		}
		return ForkResult{}, fmt.Errorf("session: child session %q already exists", childID)
	} else if !os.IsNotExist(readErr) {
		return ForkResult{}, readErr
	}
	if _, err := parent.Fork(ctx, childDir, childID, sequence); err != nil {
		// The check above is no reservation: two callers with one operation id
		// both reach the publish, and the child the winner published is this
		// request's own result, so the race resolves as the idempotent success.
		if matched, readErr := forkChildMatches(childDir, parentDir, sequence); readErr == nil && matched {
			return ForkResult{Child: childRef, Turn: target}, nil
		}
		// Both refusals mean the boundary this target advertised cannot carry a
		// safe child. Each keeps its own reason so the surface says which one.
		switch {
		case errors.Is(err, ErrForkActiveAuthority):
			return ForkResult{}, &ForkUnavailableError{TurnID: request.TurnID, Reason: ForkActiveAuthority}
		case errors.Is(err, ErrForkBoundaryNotAtomic):
			return ForkResult{}, &ForkUnavailableError{TurnID: request.TurnID, Reason: ForkHistoryUnverifiable}
		default:
			return ForkResult{}, err
		}
	}
	return ForkResult{Child: childRef, Turn: target}, nil
}

// forkChildMatches reports whether childDir already holds the child this request
// asked for: the same inherited cut, from the same parent directory. The read
// error is passed through so a caller can tell "no child yet" from a store it
// cannot read.
func forkChildMatches(childDir, parentDir string, sequence uint64) (bool, error) {
	manifest, err := readStoredManifest(filepath.Join(childDir, "manifest.json"))
	if err != nil {
		return false, err
	}
	return manifest.InheritedEvents == sequence && manifest.Source != nil &&
		filepath.Clean(manifest.Source.Path) == filepath.Clean(parentDir), nil
}

// forkSource returns the session to read the durable prefix from, plus its
// release. A live source is used as-is so its accepted tail is flushed first; a
// source with no runtime in this process is opened read-only, which neither
// restores the parent agent nor disturbs another process's lease.
func (s *Service) forkSource(ctx context.Context, ref SessionRef) (*Session, func(), error) {
	if runtime, ok := s.Runtime(ref); ok {
		return runtime.session, func() {}, nil
	}
	parent, err := s.persistence.Open(ref.SessionID, ReadOnly)
	if err != nil {
		return nil, nil, err
	}
	return parent, func() { _ = parent.Close(context.WithoutCancel(ctx)) }, nil
}

func forkTargetByID(projection Projection, turnID string) (ForkTarget, bool) {
	turnID = strings.TrimSpace(turnID)
	for _, target := range ForkTargets(projection).Targets {
		if target.TurnID == turnID {
			return target, true
		}
	}
	return ForkTarget{}, false
}
