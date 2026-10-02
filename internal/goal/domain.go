package goal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const StateVersion = 1

type Phase string

const (
	PhaseActive   Phase = "active"
	PhasePaused   Phase = "paused"
	PhaseBlocked  Phase = "blocked"
	PhaseComplete Phase = "complete"
)

type Activation string

const (
	ActivationArmed    Activation = "armed"
	ActivationDisarmed Activation = "disarmed"
)

type ErrorCode string

const (
	ErrNotFound              ErrorCode = "GOAL_NOT_FOUND"
	ErrAlreadyExists         ErrorCode = "GOAL_ALREADY_EXISTS"
	ErrStaleRevision         ErrorCode = "GOAL_STALE_REVISION"
	ErrInvalidObjective      ErrorCode = "GOAL_INVALID_OBJECTIVE"
	ErrInvalidRoundLimit     ErrorCode = "GOAL_INVALID_MAX_ROUNDS"
	ErrInvalidBlockReason    ErrorCode = "GOAL_INVALID_BLOCK_REASON"
	ErrInvalidEdit           ErrorCode = "GOAL_INVALID_EDIT"
	ErrInvalidTransition     ErrorCode = "GOAL_INVALID_TRANSITION"
	ErrRoundLimit            ErrorCode = "GOAL_ROUND_LIMIT"
	ErrBlockedTooEarly       ErrorCode = "GOAL_BLOCKED_TOO_EARLY"
	ErrUserAuthorityRequired ErrorCode = "GOAL_USER_AUTHORITY_REQUIRED"
	ErrUnsupportedVersion    ErrorCode = "GOAL_UNSUPPORTED_VERSION"
)

type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string { return e.Message }

func ErrorCodeOf(err error) ErrorCode {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

func goalError(code ErrorCode, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

type Ref struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

type BlockReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Snapshot struct {
	ID            string       `json:"id"`
	Revision      uint64       `json:"revision"`
	Objective     string       `json:"objective"`
	Phase         Phase        `json:"phase"`
	MaxGoalRounds *uint64      `json:"maxGoalRounds"`
	RoundsStarted uint64       `json:"roundsStarted"`
	BlockedReason *BlockReason `json:"blockedReason,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}

func (s Snapshot) Ref() Ref { return Ref{ID: s.ID, Revision: s.Revision} }

type View struct {
	Snapshot
	Activation Activation `json:"activation"`
	StopReason string     `json:"stopReason,omitempty"`
}

func (v View) Ref() Ref { return v.Snapshot.Ref() }

type CreateRequest struct {
	Objective     string
	MaxGoalRounds *uint64
}

// RoundLimitChange distinguishes an omitted edit from explicitly removing a
// limit. Set=false leaves the existing limit unchanged; Set=true with a nil
// Value selects unlimited rounds.
type RoundLimitChange struct {
	Set   bool
	Value *uint64
}

type EditRequest struct {
	Objective     *string
	MaxGoalRounds RoundLimitChange
}

type stateDocument struct {
	Version   int                        `json:"version"`
	Current   *Snapshot                  `json:"current"`
	Cleared   *Ref                       `json:"cleared,omitempty"`
	ClearedAt *time.Time                 `json:"clearedAt,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

type Machine struct {
	mu         sync.Mutex
	now        func() time.Time
	newID      func() string
	current    *Snapshot
	activation Activation
	stopReason string
	cleared    *Ref
	clearedAt  *time.Time
	extra      map[string]json.RawMessage
}

func NewMachine(now func() time.Time, newID func() string) *Machine {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = randomID
	}
	return &Machine{now: now, newID: newID, activation: ActivationDisarmed}
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("goal-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func (m *Machine) Get() *View {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

// Clone returns an independent candidate with the same durable and live state.
// Hosts use it to prepare a mutation before the corresponding session event is
// accepted, then publish the candidate atomically after Append succeeds.
func (m *Machine) Clone() *Machine {
	if m == nil {
		return NewMachine(nil, nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	extra := make(map[string]json.RawMessage, len(m.extra))
	for key, value := range m.extra {
		extra[key] = append(json.RawMessage(nil), value...)
	}
	return &Machine{
		now:        m.now,
		newID:      m.newID,
		current:    cloneSnapshot(m.current),
		activation: m.activation,
		stopReason: m.stopReason,
		cleared:    cloneRef(m.cleared),
		clearedAt:  cloneTime(m.clearedAt),
		extra:      extra,
	}
}

// InheritRuntimeFrom copies only process-local activation state when both
// machines describe the exact same durable goal version. It never changes the
// persisted snapshot or lifecycle revision.
func (m *Machine) InheritRuntimeFrom(previous *Machine) error {
	if m == nil || previous == nil {
		return nil
	}
	prior := previous.Get()
	m.mu.Lock()
	defer m.mu.Unlock()
	if prior == nil && m.current == nil {
		return nil
	}
	if prior == nil || m.current == nil || prior.ID != m.current.ID || prior.Revision != m.current.Revision || prior.RoundsStarted != m.current.RoundsStarted || prior.Phase != m.current.Phase {
		return goalError(ErrStaleRevision, "cannot inherit activation across different goal snapshots")
	}
	m.activation = prior.Activation
	m.stopReason = prior.StopReason
	return nil
}

func (m *Machine) Create(request CreateRequest) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil && m.current.Phase != PhaseComplete {
		return View{}, goalError(ErrAlreadyExists, "an unfinished goal already exists")
	}
	objective, err := validObjective(request.Objective)
	if err != nil {
		return View{}, err
	}
	if err := validLimit(request.MaxGoalRounds, 0); err != nil {
		return View{}, err
	}
	now := m.now().UTC()
	m.current = &Snapshot{
		ID:            m.newID(),
		Revision:      1,
		Objective:     objective,
		Phase:         PhaseActive,
		MaxGoalRounds: cloneLimit(request.MaxGoalRounds),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	m.activation = ActivationArmed
	m.stopReason = ""
	m.cleared, m.clearedAt = nil, nil
	return *m.viewLocked(), nil
}

// Replace is the explicit host/UI operation for installing a new goal while
// preserving the replaced goal reference as a clear tombstone in the same
// versioned snapshot. Model create_goal intentionally cannot invoke it.
func (m *Machine) Replace(request CreateRequest) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objective, err := validObjective(request.Objective)
	if err != nil {
		return View{}, err
	}
	if err := validLimit(request.MaxGoalRounds, 0); err != nil {
		return View{}, err
	}
	now := m.now().UTC()
	if m.current != nil {
		ref := m.current.Ref()
		m.cleared = &ref
		m.clearedAt = &now
	}
	m.current = &Snapshot{
		ID: m.newID(), Revision: 1, Objective: objective, Phase: PhaseActive,
		MaxGoalRounds: cloneLimit(request.MaxGoalRounds), CreatedAt: now, UpdatedAt: now,
	}
	m.activation = ActivationArmed
	m.stopReason = ""
	return *m.viewLocked(), nil
}

func (m *Machine) Edit(ref Ref, request EditRequest) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return View{}, err
	}
	if request.Objective == nil && !request.MaxGoalRounds.Set {
		return View{}, goalError(ErrInvalidEdit, "goal edit requires objective and/or max_goal_rounds")
	}
	if request.Objective != nil {
		objective, validateErr := validObjective(*request.Objective)
		if validateErr != nil {
			return View{}, validateErr
		}
		current.Objective = objective
	}
	if request.MaxGoalRounds.Set {
		if validateErr := validLimit(request.MaxGoalRounds.Value, current.RoundsStarted); validateErr != nil {
			return View{}, validateErr
		}
		current.MaxGoalRounds = cloneLimit(request.MaxGoalRounds.Value)
	}
	m.bumpLocked(current)
	return *m.viewLocked(), nil
}

func (m *Machine) Pause(ref Ref) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return View{}, err
	}
	if current.Phase != PhaseActive {
		return View{}, goalError(ErrInvalidTransition, "only an active goal can be paused")
	}
	current.Phase = PhasePaused
	current.BlockedReason = nil
	m.activation = ActivationDisarmed
	m.stopReason = "user-paused"
	m.bumpLocked(current)
	return *m.viewLocked(), nil
}

func (m *Machine) Resume(ref Ref, directUser bool) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return View{}, err
	}
	if !directUser {
		return View{}, goalError(ErrUserAuthorityRequired, "resuming a goal requires current direct user authority")
	}
	if current.Phase == PhaseComplete || (current.Phase == PhaseActive && m.activation == ActivationArmed) {
		return View{}, goalError(ErrInvalidTransition, "goal cannot be resumed from %s/%s", current.Phase, m.activation)
	}
	if current.MaxGoalRounds != nil && current.RoundsStarted >= *current.MaxGoalRounds {
		return View{}, goalError(ErrRoundLimit, "goal exhausted its configured round limit")
	}
	current.Phase = PhaseActive
	current.BlockedReason = nil
	m.activation = ActivationArmed
	m.stopReason = ""
	m.bumpLocked(current)
	return *m.viewLocked(), nil
}

func (m *Machine) Complete(ref Ref) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return View{}, err
	}
	if current.Phase != PhaseActive {
		return View{}, goalError(ErrInvalidTransition, "only an active goal can complete")
	}
	current.Phase = PhaseComplete
	current.BlockedReason = nil
	m.activation = ActivationDisarmed
	m.stopReason = "complete"
	m.bumpLocked(current)
	return *m.viewLocked(), nil
}

func (m *Machine) Block(ref Ref, reason BlockReason, directUser bool, minimumAutomaticRounds uint64) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return View{}, err
	}
	if current.Phase != PhaseActive {
		return View{}, goalError(ErrInvalidTransition, "only an active goal can be blocked")
	}
	reason.Code = strings.TrimSpace(reason.Code)
	reason.Message = strings.TrimSpace(reason.Message)
	if reason.Code == "" || reason.Message == "" {
		return View{}, goalError(ErrInvalidBlockReason, "blocked goal requires a code and message")
	}
	if !directUser && current.RoundsStarted < minimumAutomaticRounds {
		return View{}, goalError(ErrBlockedTooEarly, "automatic blocking requires at least %d admitted rounds", minimumAutomaticRounds)
	}
	current.Phase = PhaseBlocked
	current.BlockedReason = &reason
	m.activation = ActivationDisarmed
	m.stopReason = reason.Code
	m.bumpLocked(current)
	return *m.viewLocked(), nil
}

func (m *Machine) AdmitRound(ref Ref) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return View{}, err
	}
	if current.Phase != PhaseActive || m.activation != ActivationArmed {
		return View{}, goalError(ErrInvalidTransition, "goal is not armed for automatic continuation")
	}
	if current.MaxGoalRounds != nil && current.RoundsStarted >= *current.MaxGoalRounds {
		return View{}, goalError(ErrRoundLimit, "goal exhausted its configured round limit")
	}
	current.RoundsStarted++
	return *m.viewLocked(), nil
}

func (m *Machine) Disarm(reason string) *View {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return nil
	}
	if m.current.Phase != PhaseActive {
		return m.viewLocked()
	}
	m.activation = ActivationDisarmed
	m.stopReason = strings.TrimSpace(reason)
	return m.viewLocked()
}

func (m *Machine) Clear(ref Ref) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.exactLocked(ref)
	if err != nil {
		return err
	}
	cleared := current.Ref()
	clearedAt := m.now().UTC()
	m.current = nil
	m.activation = ActivationDisarmed
	m.stopReason = "cleared"
	m.cleared = &cleared
	m.clearedAt = &clearedAt
	return nil
}

func (m *Machine) Encode() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc := map[string]any{"version": StateVersion, "current": m.current}
	if m.cleared != nil {
		doc["cleared"] = m.cleared
		doc["clearedAt"] = m.clearedAt
	}
	for key, value := range m.extra {
		if _, reserved := doc[key]; !reserved {
			doc[key] = json.RawMessage(append([]byte(nil), value...))
		}
	}
	return json.Marshal(doc)
}

func (m *Machine) Restore(data []byte) (*View, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, goalError(ErrUnsupportedVersion, "decode goal state: %v", err)
	}
	var doc stateDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, goalError(ErrUnsupportedVersion, "decode goal state: %v", err)
	}
	if doc.Version != StateVersion {
		return nil, goalError(ErrUnsupportedVersion, "unsupported goal state version %d", doc.Version)
	}
	if doc.Current != nil {
		if err := validateSnapshot(*doc.Current); err != nil {
			return nil, err
		}
	}
	delete(raw, "version")
	delete(raw, "current")
	delete(raw, "cleared")
	delete(raw, "clearedAt")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current = cloneSnapshot(doc.Current)
	m.cleared = cloneRef(doc.Cleared)
	m.clearedAt = cloneTime(doc.ClearedAt)
	m.extra = raw
	m.activation = ActivationDisarmed
	m.stopReason = "cold-restore"
	return m.viewLocked(), nil
}

func (m *Machine) exactLocked(ref Ref) (*Snapshot, error) {
	if m.current == nil {
		return nil, goalError(ErrNotFound, "no current goal")
	}
	if ref.ID != m.current.ID || ref.Revision != m.current.Revision {
		return nil, goalError(ErrStaleRevision, "goal reference is stale")
	}
	return m.current, nil
}

func (m *Machine) bumpLocked(current *Snapshot) {
	current.Revision++
	current.UpdatedAt = m.now().UTC()
}

func (m *Machine) viewLocked() *View {
	if m.current == nil {
		return nil
	}
	return &View{Snapshot: *cloneSnapshot(m.current), Activation: m.activation, StopReason: m.stopReason}
}

func validObjective(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", goalError(ErrInvalidObjective, "goal objective must not be empty")
	}
	return value, nil
}

func validLimit(limit *uint64, roundsStarted uint64) error {
	if limit != nil && (*limit == 0 || *limit < roundsStarted) {
		return goalError(ErrInvalidRoundLimit, "max goal rounds must be positive and not below admitted rounds")
	}
	return nil
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.ID == "" || snapshot.Revision == 0 {
		return goalError(ErrUnsupportedVersion, "goal state has invalid identity")
	}
	if _, err := validObjective(snapshot.Objective); err != nil {
		return err
	}
	if err := validLimit(snapshot.MaxGoalRounds, snapshot.RoundsStarted); err != nil {
		return err
	}
	switch snapshot.Phase {
	case PhaseActive, PhasePaused, PhaseComplete:
		if snapshot.BlockedReason != nil {
			return goalError(ErrUnsupportedVersion, "non-blocked goal contains blockedReason")
		}
	case PhaseBlocked:
		if snapshot.BlockedReason == nil || strings.TrimSpace(snapshot.BlockedReason.Code) == "" || strings.TrimSpace(snapshot.BlockedReason.Message) == "" {
			return goalError(ErrInvalidBlockReason, "blocked goal requires a code and message")
		}
	default:
		return goalError(ErrUnsupportedVersion, "unsupported goal phase %q", snapshot.Phase)
	}
	if snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return goalError(ErrUnsupportedVersion, "goal state has invalid timestamps")
	}
	return nil
}

func cloneLimit(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneSnapshot(value *Snapshot) *Snapshot {
	if value == nil {
		return nil
	}
	copy := *value
	copy.MaxGoalRounds = cloneLimit(value.MaxGoalRounds)
	if value.BlockedReason != nil {
		reason := *value.BlockedReason
		copy.BlockedReason = &reason
	}
	return &copy
}

func cloneRef(value *Ref) *Ref {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
