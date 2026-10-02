package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

const terminationFlushTimeout = 15 * time.Second

var errTerminationDurability = errors.New("turn termination was not made durable")

type executionTokenKey struct{}

func (c *Controller) failTermination(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.enterRecoveryLocked("termination_commit_failed")
	c.mu.Unlock()
	c.disarmGoalLifecycle("persistence-error")
	c.failTurnEventLedger(fmt.Errorf("%w: %w", errTerminationDurability, err))
}

// messageCommitAllowedLocked is checked after acquiring commitMu, so a
// recorder waiting behind a terminal cannot revive its closed turn.
func (c *Controller) messageCommitAllowedLocked(ctx context.Context, store *session.Session) bool {
	if !c.sessionEventCommitAllowed() {
		return false
	}
	token, turnID, _ := c.currentTurnToken()
	if ctx != nil {
		if origin, ok := ctx.Value(executionTokenKey{}).(uint64); ok && origin != token {
			return false
		}
	}
	if turnID != "" && c.turnEvents.finalizedTurn == turnID {
		return false
	}
	projection := store.StateSnapshot().Projection
	return projection.Recovery == nil || projection.Recovery.State != "recovery_required"
}

type terminationBoundary struct {
	token        uint64
	prefix       map[string]bool
	fallback     provider.Message
	preserveUser bool
}

func (c *Controller) noteTerminationBoundary(fallback provider.Message, preserveUser bool) {
	if c.executor == nil {
		return
	}
	token, _, _ := c.currentTurnToken()
	b := &terminationBoundary{token: token, prefix: map[string]bool{}, fallback: fallback, preserveUser: preserveUser}
	for _, message := range c.executor.Session().Snapshot() {
		b.prefix[message.ID] = true
	}
	c.turnEvents.commitMu.Lock()
	c.turnEvents.terminationBoundary = b
	c.turnEvents.commitMu.Unlock()
}

// terminationMessages is for terminal metadata only. UI and execution readers
// continue to observe the accepted projection until the terminal batch lands.
func (c *Controller) terminationMessages() []provider.Message {
	c.turnEvents.commitMu.Lock()
	defer c.turnEvents.commitMu.Unlock()
	if p := c.turnEvents.pendingTermination; p != nil {
		return append([]provider.Message(nil), p.Messages...)
	}
	return c.executor.Session().Snapshot()
}

// TerminationPlan is an owned snapshot of one explicit cleanup. Retractions
// come only from its input workset, never from a diff against durable history.
type TerminationPlan struct {
	SessionID  string
	TurnID     string
	Generation uint64
	Token      uint64
	Messages   []provider.Message
	Events     []session.Event
}

func buildTerminationPlan(before, after []provider.Message) (*TerminationPlan, error) {
	// Detach nested tool/reasoning records from the worker's mutable cache.
	data, err := json.Marshal(after)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &after); err != nil {
		return nil, err
	}
	p := &TerminationPlan{Messages: after}
	old := make(map[string]provider.Message, len(before))
	for _, message := range before {
		old[message.ID] = message
	}
	kept := make(map[string]bool, len(after))
	for _, message := range after {
		if message.ID == "" {
			return nil, fmt.Errorf("termination message has no stable identity")
		}
		kept[message.ID] = true
		if previous, ok := old[message.ID]; ok && reflect.DeepEqual(previous, message) {
			continue
		}
		payload, err := json.Marshal(map[string]any{"message": message})
		if err != nil {
			return nil, err
		}
		p.Events = append(p.Events, session.Event{Kind: "message/upsert", Payload: payload})
	}
	var removed []string
	for id := range old {
		if id != "" && !kept[id] {
			removed = append(removed, id)
		}
	}
	if len(removed) > 0 {
		sort.Strings(removed)
		payload, _ := json.Marshal(map[string]any{"messageIds": removed, "reason": "interrupted-turn-cleanup"})
		p.Events = append(p.Events, session.Event{Kind: "message/retract", Payload: payload, Required: true})
	}
	model := provider.ModelMessages(after)
	if model == nil {
		model = []provider.Message{}
	}
	payload, err := json.Marshal(map[string]any{"messages": model, "reason": "interrupted-turn-cleanup"})
	if err != nil {
		return nil, err
	}
	p.Events = append(p.Events, session.Event{Kind: "model/context-replace", Payload: payload})
	return p, nil
}

func (c *Controller) replaceSessionAfterCancelFrom(before, after []provider.Message) {
	c.replaceSessionAfterCancelFromScoped(before, after, false)
}

func (c *Controller) noteCommittedMessagesLocked(events []session.Event) {
	for _, e := range events {
		if e.Kind == "turn/start" {
			c.turnEvents.turnMessageIDs = map[string]bool{}
		}
		if e.Kind == "message/complete" {
			var payload struct {
				Message provider.Message `json:"message"`
			}
			if json.Unmarshal(e.Payload, &payload) == nil && payload.Message.ID != "" {
				if c.turnEvents.turnMessageIDs == nil {
					c.turnEvents.turnMessageIDs = map[string]bool{}
				}
				c.turnEvents.turnMessageIDs[payload.Message.ID] = true
			}
		}
	}
}

func (c *Controller) retractMissingTurnMessagesLocked(p *TerminationPlan) {
	kept := map[string]bool{}
	for _, m := range p.Messages {
		kept[m.ID] = true
	}
	var ids []string
	for id := range c.turnEvents.turnMessageIDs {
		if !kept[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	sort.Strings(ids)
	payload, _ := json.Marshal(map[string]any{"messageIds": ids, "reason": "synthetic-turn-interrupted"})
	p.Events = append([]session.Event{{Kind: "message/retract", Required: true, Payload: payload}}, p.Events...)
}

func (c *Controller) replaceSessionAfterCancelFromScoped(before, after []provider.Message, retractTurn bool) {
	if c.executor == nil {
		return
	}
	after = agent.PlanRetainedToolRecords(before, after)
	for i := range after {
		if after[i].ID == "" {
			after[i].ID = agent.NewMessageID()
		}
	}
	p, err := buildTerminationPlan(before, after)
	if err != nil {
		c.failTermination(err)
		return
	}
	c.snapshotMu.Lock()
	store := c.sessionEventStore()
	if store == nil {
		c.replaceLegacySessionAfterCancelLocked(after)
		c.snapshotMu.Unlock()
		return
	}
	p.SessionID = store.ID()
	p.Token, p.TurnID, _ = c.currentTurnToken()
	p.Generation = c.ExecutionGeneration()
	c.turnEvents.commitMu.Lock()
	if !c.sessionEventCommitAllowed() || (p.TurnID != "" && c.turnEvents.finalizedTurn == p.TurnID) {
		c.turnEvents.commitMu.Unlock()
		c.snapshotMu.Unlock()
		return
	}
	projection := store.StateSnapshot().Projection
	if projection.Recovery != nil && projection.Recovery.State == "recovery_required" {
		c.turnEvents.commitMu.Unlock()
		c.snapshotMu.Unlock()
		return
	}
	if p.TurnID != "" && projection.TurnID == p.TurnID {
		if retractTurn {
			c.retractMissingTurnMessagesLocked(p)
		}
		// Compatibility runners may update the executor before their recorder
		// commits. Preserve these unchanged messages explicitly as well.
		known := map[string]bool{}
		for _, m := range store.ExecutionSnapshot().Projection.ModelMessages {
			known[m.ID] = true
		}
		var unrecorded []session.Event
		for _, m := range before {
			if known[m.ID] {
				continue
			}
			for _, retained := range after {
				if retained.ID == m.ID && reflect.DeepEqual(m, retained) {
					payload, _ := json.Marshal(map[string]any{"message": retained})
					unrecorded = append(unrecorded, session.Event{Kind: "message/upsert", Payload: payload})
				}
			}
		}
		p.Events = append(unrecorded, p.Events...)
		c.turnEvents.pendingTermination = p
		c.turnEvents.commitMu.Unlock()
		c.snapshotMu.Unlock()
		return
	}
	// Resume cleanup has no live turn to close. Its deterministic repair
	// operation never adds a second terminal record.
	data, _ := json.Marshal(p.Events)
	digest := sha256.Sum256(data)
	ctx, cancel := context.WithTimeout(context.Background(), terminationFlushTimeout)
	_, err = c.appendSessionBatch(ctx, store, session.Batch{OperationID: fmt.Sprintf("turn-repair:%x", digest), Events: p.Events})
	if err == nil {
		c.executor.Session().Replace(p.Messages)
		_, err = store.Flush(ctx)
	}
	c.turnEvents.commitMu.Unlock()
	if err == nil && !c.sessionEngineEnabled() {
		c.replaceLegacySessionAfterCancelLocked(p.Messages)
	}
	c.snapshotMu.Unlock()
	cancel()
	if err != nil {
		c.failTermination(err)
	}
}

// appendTerminationLocked runs under snapshotMu -> commitMu. The runtime gate
// revalidates ownership and turn identity at acceptance, after preparation.
func (c *Controller) appendTerminationLocked(ctx context.Context, e event.Event, store *session.Session, terminal []session.Event) error {
	if c.turnEvents.finalizedTurn == e.TurnID {
		_, err := store.Flush(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", errTerminationDurability, err)
		}
		return nil
	}
	snapshot := store.StateSnapshot()
	if e.TurnID == "" || snapshot.Projection.TurnID != e.TurnID {
		return session.ErrStaleExecution
	}
	p := c.turnEvents.pendingTermination
	if e.Recovery != nil && e.Recovery.State == "recovery_required" {
		var err error
		p, err = c.watchdogTerminationPlanLocked(store, e.TurnID)
		if err != nil {
			return err
		}
	}
	if p != nil {
		token, turnID, _ := c.currentTurnToken()
		if p.SessionID != store.ID() || p.Generation != c.ExecutionGeneration() || p.Token != token || p.TurnID != turnID {
			return session.ErrStaleExecution
		}
		terminal = append(append([]session.Event(nil), p.Events...), terminal...)
	}
	batch := session.Batch{OperationID: "turn-finalize:" + e.TurnID, TurnID: e.TurnID, Events: terminal}
	prepared, err := store.PrepareBatchContext(ctx, batch.OperationID, batch)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, session.ErrOperationConflict) {
			return fmt.Errorf("%w: %w", errTerminationDurability, err)
		}
		return err
	}
	_, runtime, exclusive := c.v3Binding()
	if exclusive && runtime != nil {
		_, err = runtime.CommitPreparedForTurn(c.ExecutionGeneration(), e.TurnID, prepared)
	} else {
		_, err = store.CommitPrepared(prepared)
	}
	if err != nil {
		return err
	}
	c.turnEvents.finalizedTurn = e.TurnID
	c.turnEvents.pendingTermination = nil
	if p != nil && c.executor != nil {
		c.executor.Session().Replace(p.Messages)
	}
	_, err = store.Flush(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errTerminationDurability, err)
	}
	return err
}

func (c *Controller) watchdogTerminationPlanLocked(store *session.Session, turnID string) (*TerminationPlan, error) {
	// Seal accepted work only: an uncooperative worker may mutate its cache.
	before := store.ExecutionSnapshot().Projection.ModelMessages
	var next []provider.Message
	b := c.turnEvents.terminationBoundary
	token, _, _ := c.currentTurnToken()
	if b != nil && b.token == token {
		for _, message := range before {
			if b.prefix[message.ID] || agent.IsCompactionSummary(message) {
				next = append(next, message)
			}
		}
		if b.preserveUser && b.fallback.ID != "" {
			user := b.fallback
			for _, message := range before {
				if message.ID == user.ID {
					user = message
					break
				}
			}
			user.Content = StripComposePrefixes(user.Content)
			next = append(next, user)
		}
	} else {
		next = append([]provider.Message(nil), before...)
	}
	p, err := buildTerminationPlan(before, next)
	if err != nil {
		return nil, err
	}
	p.SessionID, p.TurnID, p.Generation, p.Token = store.ID(), turnID, c.ExecutionGeneration(), token
	if b != nil && !b.preserveUser {
		c.retractMissingTurnMessagesLocked(p)
	}
	return p, nil
}
