package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"reasonix/internal/agent"
	"reasonix/internal/event"
)

type PendingPromptOwner struct {
	mu       sync.Mutex
	pending  map[string]PendingPrompt
	resolved map[string]PromptResolution
	next     uint64
	revision uint64
}

type PendingPromptState string

const (
	PromptPending   PendingPromptState = "pending"
	PromptResolving PendingPromptState = "resolving"
)

type PromptTerminalState string

const (
	PromptAnswered    PromptTerminalState = "answered"
	PromptRejected    PromptTerminalState = "rejected"
	PromptCancelled   PromptTerminalState = "cancelled"
	PromptUnavailable PromptTerminalState = "unavailable"
)

type PromptResolution struct {
	Identity     PromptIdentity
	State        PromptTerminalState
	AnswerDigest string
}

type PendingPrompt struct {
	Identity PromptIdentity
	State    PendingPromptState
	Order    uint64
	// AnswerDigest is filled only after the exact resolver wins the one-shot
	// transition. It lets an identical retry succeed idempotently while a
	// conflicting late answer is rejected.
	AnswerDigest string
	Done         chan struct{}
	Resolve      func(PromptAnswer) error
	Cancel       func() error
}

func (o *PendingPromptOwner) RegisterPrompt(prompt PendingPrompt) error {
	identity := prompt.Identity
	if identity.PromptID == "" {
		return ErrPromptNotPending
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending == nil {
		o.pending = make(map[string]PendingPrompt)
	}
	if o.resolved == nil {
		o.resolved = make(map[string]PromptResolution)
	}
	if _, exists := o.resolved[identity.PromptID]; exists {
		return fmt.Errorf("prompt %q was already terminal", identity.PromptID)
	}
	if _, exists := o.pending[identity.PromptID]; exists {
		return fmt.Errorf("prompt %q already registered", identity.PromptID)
	}
	if prompt.State == "" {
		prompt.State = PromptPending
	}
	if prompt.Done == nil {
		prompt.Done = make(chan struct{})
	}
	o.next++
	prompt.Order = o.next
	o.pending[identity.PromptID] = prompt
	o.revision++
	return nil
}

func (o *PendingPromptOwner) Register(identity PromptIdentity) error {
	return o.RegisterPrompt(PendingPrompt{Identity: identity, State: PromptPending})
}
func (o *PendingPromptOwner) Identity(id string) (PromptIdentity, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.pending[id]
	return v.Identity, ok
}
func (o *PendingPromptOwner) Prompt(id string) (PendingPrompt, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.pending[id]
	return p, ok
}
func (o *PendingPromptOwner) Remove(id string) {
	o.mu.Lock()
	if prompt, ok := o.pending[id]; ok {
		delete(o.pending, id)
		close(prompt.Done)
		o.revision++
	}
	o.mu.Unlock()
}
func (o *PendingPromptOwner) RemoveKind(kind PromptKind) {
	o.mu.Lock()
	defer o.mu.Unlock()
	changed := false
	for id, prompt := range o.pending {
		if prompt.Identity.Kind == kind {
			delete(o.pending, id)
			close(prompt.Done)
			changed = true
		}
	}
	if changed {
		o.revision++
	}
}

func (o *PendingPromptOwner) MarkKindTerminal(kind PromptKind, state PromptTerminalState) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.resolved == nil {
		o.resolved = make(map[string]PromptResolution)
	}
	changed := false
	for id, prompt := range o.pending {
		if prompt.Identity.Kind != kind {
			continue
		}
		delete(o.pending, id)
		close(prompt.Done)
		changed = true
		if _, exists := o.resolved[id]; !exists {
			o.resolved[id] = PromptResolution{Identity: prompt.Identity, State: state}
		}
	}
	if changed {
		o.revision++
	}
}
func (o *PendingPromptOwner) MarkResolved(identity PromptIdentity) {
	o.MarkTerminal(identity, PromptAnswered)
}
func (o *PendingPromptOwner) MarkTerminal(identity PromptIdentity, state PromptTerminalState) {
	o.mu.Lock()
	defer o.mu.Unlock()
	prompt, pending := o.pending[identity.PromptID]
	if pending {
		identity = normalizePromptIdentity(identity, prompt.Identity)
	}
	delete(o.pending, identity.PromptID)
	if pending {
		close(prompt.Done)
	}
	if o.resolved == nil {
		o.resolved = make(map[string]PromptResolution)
	}
	if _, exists := o.resolved[identity.PromptID]; !exists {
		o.resolved[identity.PromptID] = PromptResolution{Identity: identity, State: state, AnswerDigest: prompt.AnswerDigest}
		pending = true
	}
	if pending {
		o.revision++
	}
}
func (o *PendingPromptOwner) BeginResolve(identity PromptIdentity) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.pending[identity.PromptID]
	if !ok {
		if _, resolved := o.resolved[identity.PromptID]; resolved {
			return ErrPromptAlreadyResolved
		}
		return ErrPromptNotPending
	}
	identity = normalizePromptIdentity(identity, p.Identity)
	if p.Identity != identity {
		return ErrPromptStaleTurn
	}
	if p.State == PromptResolving {
		return ErrPromptAlreadyResolved
	}
	p.State = PromptResolving
	if p.Done == nil {
		p.Done = make(chan struct{})
	}
	o.pending[identity.PromptID] = p
	o.revision++
	return nil
}

// BindRouting fills routing fields that were not available when a prompt was
// queued behind another prompt. It never rewrites a captured identity: once a
// turn or runtime epoch is known, a later event must use that same value.
func (o *PendingPromptOwner) BindRouting(id, turnID, runtimeEpoch string) (PromptIdentity, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	prompt, ok := o.pending[id]
	if !ok {
		return PromptIdentity{}, false
	}
	changed := false
	if prompt.Identity.TurnID == "" && turnID != "" {
		prompt.Identity.TurnID = turnID
		changed = true
	}
	if prompt.Identity.RuntimeEpoch == "" && runtimeEpoch != "" {
		prompt.Identity.RuntimeEpoch = runtimeEpoch
		changed = true
	}
	o.pending[id] = prompt
	if changed {
		o.revision++
	}
	return prompt.Identity, true
}
func (o *PendingPromptOwner) Resolve(identity PromptIdentity, answer PromptAnswer) error {
	digest := promptAnswerDigest(answer)
	wantState := promptAnswerTerminal(identity, answer)
	for {
		o.mu.Lock()
		if resolution, ok := o.resolved[identity.PromptID]; ok {
			identity = normalizePromptIdentity(identity, resolution.Identity)
			o.mu.Unlock()
			if resolution.Identity == identity && resolution.State == wantState && resolution.AnswerDigest == digest {
				return nil
			}
			return ErrPromptAlreadyResolved
		}
		prompt, ok := o.pending[identity.PromptID]
		if !ok {
			o.mu.Unlock()
			return ErrPromptNotPending
		}
		identity = normalizePromptIdentity(identity, prompt.Identity)
		if prompt.Identity != identity {
			o.mu.Unlock()
			return ErrPromptStaleTurn
		}
		if prompt.State == PromptResolving {
			if prompt.AnswerDigest != digest {
				o.mu.Unlock()
				return ErrPromptAlreadyResolved
			}
			done := prompt.Done
			o.mu.Unlock()
			<-done
			continue
		}
		prompt.State = PromptResolving
		prompt.AnswerDigest = digest
		if prompt.Done == nil {
			prompt.Done = make(chan struct{})
		}
		o.pending[identity.PromptID] = prompt
		o.revision++
		o.mu.Unlock()

		if prompt.Resolve == nil {
			// A published request without a live answerer is terminal. Leaving it
			// pending would recreate the permanent-wait failure this registry owns.
			o.MarkTerminal(identity, PromptUnavailable)
			return ErrPromptUnavailable
		}
		if err := prompt.Resolve(answer); err != nil {
			// The typed answerer failed after this registry awarded it the
			// one-shot transition. It is no longer safe to advertise the request
			// as answerable: terminalize it and detach typed cleanup so neither a
			// dead connection nor a throwing adapter can create an infinite wait.
			o.MarkTerminal(identity, PromptUnavailable)
			if prompt.Cancel != nil {
				go func(cancel func() error) { _ = cancel() }(prompt.Cancel)
			}
			return errors.Join(ErrPromptUnavailable, err)
		}
		o.MarkTerminal(identity, wantState)
		resolution, ok := o.Resolution(identity.PromptID)
		if !ok || resolution.State != wantState || resolution.AnswerDigest != digest {
			return ErrPromptAlreadyResolved
		}
		return nil
	}
}

// normalizePromptIdentity preserves compatibility with transports that were
// shipped before toolCallId became part of the shared interaction snapshot.
// The owner remains authoritative: an omitted field adopts the captured value,
// while a conflicting non-empty value still fails the exact identity check.
func normalizePromptIdentity(candidate, owned PromptIdentity) PromptIdentity {
	if candidate.ToolCallID == "" {
		candidate.ToolCallID = owned.ToolCallID
	}
	return candidate
}

func promptAnswerDigest(answer PromptAnswer) string {
	b, _ := json.Marshal(answer)
	return string(b)
}

func promptAnswerTerminal(identity PromptIdentity, answer PromptAnswer) PromptTerminalState {
	if identity.Kind == PromptMCP && answer.Action == "cancel" {
		return PromptCancelled
	}
	if (identity.Kind == PromptApproval && !answer.Allow) ||
		(identity.Kind == PromptMCP && answer.Action == "decline") ||
		(identity.Kind == PromptPlan && answer.Action != "start") ||
		(identity.Kind == PromptRecovery && answer.Action != string(agent.RecoveryActionContinue)) {
		return PromptRejected
	}
	return PromptAnswered
}

func (o *PendingPromptOwner) MarkIDTerminal(id string, state PromptTerminalState) {
	if prompt, ok := o.Prompt(id); ok {
		o.MarkTerminal(prompt.Identity, state)
	}
}
func (o *PendingPromptOwner) WasResolved(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.resolved[id]
	return ok
}
func (o *PendingPromptOwner) Resolution(id string) (PromptResolution, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	resolution, ok := o.resolved[id]
	return resolution, ok
}
func (o *PendingPromptOwner) Clear() {
	o.mu.Lock()
	for _, prompt := range o.pending {
		close(prompt.Done)
	}
	o.pending = make(map[string]PendingPrompt)
	o.resolved = make(map[string]PromptResolution)
	o.revision++
	o.mu.Unlock()
}
func (o *PendingPromptOwner) CancelAll() {
	o.cancelMatching(func(PromptIdentity) bool { return true })
}

// CancelTurn cannot close prompts registered by a successor while an older
// Stop request was waiting on its asynchronous publication lane.
func (o *PendingPromptOwner) CancelTurn(turnID string) {
	o.cancelMatching(func(identity PromptIdentity) bool { return identity.TurnID == turnID })
}

func (o *PendingPromptOwner) cancelMatching(matches func(PromptIdentity) bool) {
	o.mu.Lock()
	cancels := make([]func() error, 0, len(o.pending))
	identities := make([]PromptIdentity, 0, len(o.pending))
	prompts := make([]PendingPrompt, 0, len(o.pending))
	for _, prompt := range o.pending {
		if !matches(prompt.Identity) {
			continue
		}
		prompts = append(prompts, prompt)
		identities = append(identities, prompt.Identity)
		if prompt.Cancel != nil {
			cancels = append(cancels, prompt.Cancel)
		}
	}
	for _, identity := range identities {
		delete(o.pending, identity.PromptID)
	}
	if o.resolved == nil {
		o.resolved = make(map[string]PromptResolution)
	}
	for _, identity := range identities {
		if _, exists := o.resolved[identity.PromptID]; !exists {
			o.resolved[identity.PromptID] = PromptResolution{Identity: identity, State: PromptCancelled}
		}
	}
	for _, prompt := range prompts {
		close(prompt.Done)
	}
	if len(identities) > 0 {
		o.revision++
	}
	o.mu.Unlock()
	for _, cancel := range cancels {
		// A connector or legacy adapter may provide a cancellation callback that
		// blocks. Registry state is already terminal, so cleanup runs detached and
		// can never delay the session's Stop path.
		go func(cancel func() error) { _ = cancel() }(cancel)
	}
}
func (o *PendingPromptOwner) Identities() []PromptIdentity {
	identities, _ := o.IdentitiesRevision()
	return identities
}

// IdentitiesRevision returns one registry projection boundary. Runtime-state
// assembly verifies the revision after sampling its other owners so a prompt
// transition cannot be published with an older todo/turn snapshot.
func (o *PendingPromptOwner) IdentitiesRevision() ([]PromptIdentity, uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	type orderedIdentity struct {
		identity PromptIdentity
		order    uint64
	}
	ordered := make([]orderedIdentity, 0, len(o.pending))
	for _, prompt := range o.pending {
		ordered = append(ordered, orderedIdentity{identity: prompt.Identity, order: prompt.Order})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })
	out := make([]PromptIdentity, len(ordered))
	for i := range ordered {
		out[i] = ordered[i].identity
	}
	return out, o.revision
}

func (o *PendingPromptOwner) Revision() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.revision
}

// PromptKind identifies the interactive surface that owns a pending decision.
type PromptKind string

const (
	PromptAsk      PromptKind = "ask"
	PromptApproval PromptKind = "approval"
	PromptPlan     PromptKind = "plan"
	PromptRecovery PromptKind = "recovery"
	PromptMCP      PromptKind = "mcp"
)

// PromptIdentity is the immutable identity captured when a decision card is
// emitted. Turn and runtime fences prevent a delayed UI action crossing a
// controller replacement.
type PromptIdentity struct {
	PromptID     string
	ToolCallID   string
	TurnID       string
	RuntimeEpoch string
	Kind         PromptKind
}

func (c *Controller) promptIdentitySnapshot() (string, string) {
	turnID, _, _, _ := c.turnEventRuntimeStatus()
	c.promptEpochMu.RLock()
	epoch := c.promptRuntimeEpoch
	c.promptEpochMu.RUnlock()
	return turnID, epoch
}

func (c *Controller) registerOwnedPrompt(id string, kind PromptKind) {
	turn, epoch := c.promptIdentitySnapshot()
	identity := PromptIdentity{PromptID: id, ToolCallID: id, TurnID: turn, RuntimeEpoch: epoch, Kind: kind}
	resolve := func(answer PromptAnswer) error {
		switch kind {
		case PromptAsk:
			return c.answerQuestionCheckedLocked(id, answer.Questions)
		case PromptApproval:
			return c.resolveApprovalLocked(id, answer.Allow, scopeFromApprove(answer.Allow, answer.Session, answer.Persist))
		case PromptPlan:
			return c.resolvePlanDecisionWithFeedbackLocked(id, PlanDecisionAction(answer.Action), answer.Feedback)
		case PromptRecovery:
			return c.resolveRecoveryLocked(id, agent.RecoveryAction(answer.Action), answer.Feedback)
		case PromptMCP:
			return c.answerMCPInteractionCheckedLocked(id, answer.Action, answer.Content)
		default:
			return ErrPromptNotPending
		}
	}
	cancel := func() error {
		switch kind {
		case PromptAsk:
			c.approval.cancelAsk(id)
		case PromptApproval, PromptPlan, PromptRecovery:
			c.approval.cancel(id)
		case PromptMCP:
			c.approval.cancelMCPInteraction(id)
		}
		return nil
	}
	_ = c.promptOwner.RegisterPrompt(PendingPrompt{Identity: identity, State: PromptPending, Resolve: resolve, Cancel: cancel})
}

// bindOwnedPromptRouting completes an identity immediately before its request
// event is emitted. This closes the startup window where the prompt is queued
// before the turn ledger has published its active turn or runtime epoch.
func (c *Controller) bindOwnedPromptRouting(id, turnID, runtimeEpoch string) PromptIdentity {
	if c == nil {
		return PromptIdentity{}
	}
	identity, _ := c.promptOwner.BindRouting(id, turnID, runtimeEpoch)
	return identity
}

func (c *Controller) PendingPromptIdentities() []PromptIdentity { return c.promptOwner.Identities() }

func (c *Controller) cancelOwnedPrompt(id string) {
	c.cancelOwnedPromptLocked(id)
}

func (c *Controller) cancelOwnedPromptLocked(id string) {
	if prompt, ok := c.promptOwner.Prompt(id); ok && prompt.Cancel != nil {
		_ = prompt.Cancel()
	} else {
		c.approval.cancel(id)
		c.approval.cancelAsk(id)
		c.approval.cancelMCPInteraction(id)
	}
	c.promptOwner.MarkIDTerminal(id, PromptCancelled)
}

var (
	ErrPromptStaleTurn       = errors.New("prompt belongs to a stale turn")
	ErrPromptStaleRuntime    = errors.New("prompt belongs to a stale runtime")
	ErrPromptAlreadyResolved = errors.New("prompt is already resolved")
	ErrPromptNotPending      = errors.New("prompt is not pending")
	ErrPromptUnavailable     = errors.New("prompt answerer is unavailable")
)

// PromptAnswer is the transport-neutral union used by exact prompt resolve.
type PromptAnswer struct {
	Questions          []event.AskAnswer
	Allow              bool
	Session            bool
	Persist            bool
	Action             string
	Feedback           string
	Content            map[string]any
	Generation         uint64
	PermissionRevision uint64
}

// ResolvePromptExact is the single controller-owned decision boundary. The
// specialized resolvers retain their validation and durable receipts. The
// owner claims the one-shot transition before invoking an answerer, without
// holding its registry lock or any controller-wide lock.
func (c *Controller) ResolvePromptExact(identity PromptIdentity, answer PromptAnswer) error {
	defer c.refreshRuntimeState(event.Event{})
	if c == nil {
		return ErrPromptNotPending
	}
	if identity.PromptID == "" || identity.TurnID == "" {
		return ErrPromptNotPending
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrPromptNotPending
	}
	c.promptEpochMu.RLock()
	epoch := c.promptRuntimeEpoch
	c.promptEpochMu.RUnlock()
	if epoch != "" && (identity.RuntimeEpoch == "" || identity.RuntimeEpoch != epoch) {
		return ErrPromptStaleRuntime
	}
	turnID, _, _, _ := c.turnEventRuntimeStatus()
	if turnID != identity.TurnID {
		return ErrPromptStaleTurn
	}
	owned, ok := c.promptOwner.Identity(identity.PromptID)
	if !ok {
		// Let the owner compare the terminal state and answer digest. An
		// identical transport retry is idempotent; a different late answer is
		// rejected as a conflict.
		return c.promptOwner.Resolve(identity, answer)
	}
	identity = normalizePromptIdentity(identity, owned)
	if owned.TurnID != identity.TurnID || owned.Kind != identity.Kind {
		return ErrPromptStaleTurn
	}
	if owned.RuntimeEpoch != identity.RuntimeEpoch {
		return ErrPromptStaleRuntime
	}
	if identity.Kind == PromptApproval {
		if answer.Generation != 0 && answer.Generation != c.runtimeGeneration {
			return ErrPromptStaleRuntime
		}
		if answer.PermissionRevision != 0 && answer.PermissionRevision != c.permissionRevision.Load() {
			return ErrPromptStaleRuntime
		}
	}
	return c.promptOwner.Resolve(identity, answer)
}
