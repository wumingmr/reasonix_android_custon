package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type maintenanceCLIController struct {
	control.SessionAPI
	cancelCalls int
	submissions []string
	result      control.SubmitResult
}

type cliBlockingSummaryProvider struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (p *cliBlockingSummaryProvider) Name() string { return "cli-blocking-summary" }

func (p *cliBlockingSummaryProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	return nil, ctx.Err()
}

func (c *maintenanceCLIController) Cancel() { c.cancelCalls++ }

func (c *maintenanceCLIController) SubmitDisplayWithResult(display, input string) control.SubmitResult {
	c.submissions = append(c.submissions, display+"\x00"+input)
	return c.result
}

func maintenanceEvent(id, activity, status string) agentEventMsg {
	return agentEventMsg(event.Event{
		Kind: event.SessionOperation,
		SessionOperation: &event.SessionOperationInfo{
			OperationID: id,
			Kind:        "compact",
			Activity:    activity,
			Status:      status,
		},
	})
}

func TestSessionOperationOwnsMaintenanceWithoutStartingTurn(t *testing.T) {
	ctrl := &maintenanceCLIController{SessionAPI: newOwnedTestController(t, control.Options{})}
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)

	next, _ := m.Update(maintenanceEvent("op-1", "running", "running"))
	m = next.(chatTUI)
	if m.state != tuiIdle {
		t.Fatalf("maintenance changed ordinary turn state = %v, want idle", m.state)
	}
	if m.maintenance == nil || m.maintenance.OperationID != "op-1" || m.maintenance.Activity != "running" {
		t.Fatalf("maintenance state = %+v, want running op-1", m.maintenance)
	}
	if !m.runStart.IsZero() || m.elapsedTickGeneration != 0 {
		t.Fatalf("maintenance started ordinary turn timing: start=%v generation=%d", m.runStart, m.elapsedTickGeneration)
	}

	next, _ = m.Update(maintenanceEvent("op-1", "finalizing", "finalizing"))
	m = next.(chatTUI)
	if m.maintenance == nil || m.maintenance.Activity != "finalizing" {
		t.Fatalf("finalizing operation state = %+v", m.maintenance)
	}

	next, _ = m.Update(maintenanceEvent("op-1", "finalizing", "completed"))
	m = next.(chatTUI)
	if m.maintenance != nil {
		t.Fatalf("terminal operation left maintenance active: %+v", m.maintenance)
	}
}

func TestTerminalSessionOperationCannotRegressToCancelling(t *testing.T) {
	ctrl := &maintenanceCLIController{SessionAPI: newOwnedTestController(t, control.Options{})}
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)

	next, _ := m.Update(maintenanceEvent("op-1", "running", "running"))
	m = next.(chatTUI)
	next, _ = m.Update(maintenanceEvent("op-1", "finalizing", "completed"))
	m = next.(chatTUI)
	terminalCard := m.transcript[m.maintenanceTranscriptIdx]

	next, _ = m.Update(maintenanceEvent("op-1", "cancelling", "cancelling"))
	m = next.(chatTUI)
	if m.maintenance != nil {
		t.Fatalf("late cancelling event reactivated maintenance: %+v", m.maintenance)
	}
	if got := m.transcript[m.maintenanceTranscriptIdx]; got != terminalCard {
		t.Fatalf("late cancelling event replaced terminal card:\n%s", got)
	}
}

func TestSessionOperationRevisionRejectsOlderProgress(t *testing.T) {
	ctrl := &maintenanceCLIController{SessionAPI: newOwnedTestController(t, control.Options{})}
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)

	newer := event.Event{Kind: event.SessionOperation, SessionOperation: &event.SessionOperationInfo{
		OperationID: "op-1", OperationRevision: 3, RuntimeEpoch: "epoch-1",
		Kind: "compact", Activity: "finalizing", Status: "finalizing", Detail: "keep",
	}}
	older := event.Event{Kind: event.SessionOperation, SessionOperation: &event.SessionOperationInfo{
		OperationID: "op-1", OperationRevision: 2, RuntimeEpoch: "epoch-1",
		Kind: "compact", Activity: "cancelling", Status: "cancelling",
	}}
	next, _ := m.Update(agentEventMsg(newer))
	m = next.(chatTUI)
	next, _ = m.Update(agentEventMsg(older))
	m = next.(chatTUI)

	if m.maintenance == nil || m.maintenance.OperationRevision != 3 || m.maintenance.Activity != "finalizing" {
		t.Fatalf("older progress replaced newer maintenance state: %+v", m.maintenance)
	}
	if m.maintenance.Detail != "keep" {
		t.Fatalf("older progress cleared detail: %+v", m.maintenance)
	}
}

func TestSessionOperationUpdatesOneCompactionCard(t *testing.T) {
	ctrl := &maintenanceCLIController{SessionAPI: newOwnedTestController(t, control.Options{})}
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)

	next, _ := m.Update(maintenanceEvent("op-1", "running", "running"))
	m = next.(chatTUI)
	cardCount := len(m.transcript)
	cardIndex := m.maintenanceTranscriptIdx

	next, _ = m.Update(maintenanceEvent("op-1", "cancelling", "cancelling"))
	m = next.(chatTUI)
	if len(m.transcript) != cardCount || m.maintenanceTranscriptIdx != cardIndex {
		t.Fatalf("cancelling inserted another card: count=%d index=%d", len(m.transcript), m.maintenanceTranscriptIdx)
	}

	done := event.Event{Kind: event.SessionOperation, SessionOperation: &event.SessionOperationInfo{
		OperationID: "op-1", Kind: "compact", Activity: "finalizing", Status: "completed",
		Applied: true, InputTokens: 12000, ResultTokens: 3000, Messages: 6, Summary: "retained summary",
	}}
	next, _ = m.Update(agentEventMsg(done))
	m = next.(chatTUI)
	if len(m.transcript) != cardCount || m.maintenanceTranscriptIdx != cardIndex {
		t.Fatalf("completion inserted another card: count=%d index=%d", len(m.transcript), m.maintenanceTranscriptIdx)
	}
	card := m.transcript[cardIndex]
	for _, want := range []string{"retained summary", "12.0K", "3.0K"} {
		if !strings.Contains(card, want) {
			t.Fatalf("completed card missing %q:\n%s", want, card)
		}
	}
}

func TestEscCancelsMaintenanceOnceAndPreservesDraft(t *testing.T) {
	ctrl := &maintenanceCLIController{SessionAPI: newOwnedTestController(t, control.Options{})}
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	m.input.SetValue("keep this draft")
	m.pastedBlocks = []pastedBlock{{label: "[paste #1]", text: "payload"}}

	next, _ := m.Update(maintenanceEvent("op-1", "running", "running"))
	m = next.(chatTUI)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(chatTUI)

	if ctrl.cancelCalls != 1 {
		t.Fatalf("Esc cancel calls = %d, want 1", ctrl.cancelCalls)
	}
	if got := m.input.Value(); got != "keep this draft" {
		t.Fatalf("Esc changed maintenance draft to %q", got)
	}
	if len(m.pastedBlocks) != 1 || m.pastedBlocks[0].text != "payload" {
		t.Fatalf("Esc changed maintenance paste state: %+v", m.pastedBlocks)
	}
	if m.maintenance == nil || m.maintenance.Activity != "cancelling" {
		t.Fatalf("Esc maintenance state = %+v, want cancelling", m.maintenance)
	}

	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(chatTUI)
	if ctrl.cancelCalls != 1 {
		t.Fatalf("repeated Esc cancel calls = %d, want idempotent 1", ctrl.cancelCalls)
	}
	if got := m.input.Value(); got != "keep this draft" {
		t.Fatalf("repeated Esc changed maintenance draft to %q", got)
	}
}

func TestCompactSlashUsesRegisteredManagementSubmission(t *testing.T) {
	ctrl := &maintenanceCLIController{
		SessionAPI: newOwnedTestController(t, control.Options{}),
		result: control.SubmitResult{
			Disposition: control.SubmitManagementHandled,
			OperationID: "op-registered",
		},
	}
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)

	if cmd := m.runSlashCommand("/compact retain tests"); cmd != nil {
		t.Fatal("registered /compact should not return a long-running TUI command")
	}
	if len(ctrl.submissions) != 1 || ctrl.submissions[0] != "/compact retain tests\x00/compact retain tests" {
		t.Fatalf("management submissions = %#v", ctrl.submissions)
	}
	if m.maintenance == nil || m.maintenance.OperationID != "op-registered" {
		t.Fatalf("registered maintenance state = %+v", m.maintenance)
	}
}

func TestCompactCommandEscCancelsRealMaintenanceAndKeepsDraft(t *testing.T) {
	providerStub := &cliBlockingSummaryProvider{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	sess := agent.NewSession("sys")
	for range 8 {
		sess.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("question ", 300)})
		sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("answer ", 300)})
	}
	events := make(chan event.Event, 128)
	sink := event.FuncSink(func(e event.Event) { events <- e })
	exec := agent.New(providerStub, nil, sess, agent.Options{ContextWindow: 32000}, sink)
	ctrl := newOwnedTestController(t, control.Options{Executor: exec, Sink: sink})
	m := newChatTUI(ctrl, "", events, 80)

	if cmd := m.runSlashCommand("/compact retain tests"); cmd != nil {
		t.Fatal("registered /compact returned a blocking command")
	}
	select {
	case <-providerStub.started:
	case <-time.After(3 * time.Second):
		t.Fatal("summary provider did not start")
	}

	deadline := time.After(3 * time.Second)
	for len(m.maintenanceLatest) == 0 {
		select {
		case e := <-events:
			next, _ := m.Update(agentEventMsg(e))
			m = next.(chatTUI)
		case <-deadline:
			t.Fatal("SessionOperation start event was not dispatched to the TUI")
		}
	}
	m.input.SetValue("draft queued during compaction")
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(chatTUI)

	select {
	case <-providerStub.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("Esc did not cancel the real summary request")
	}
	if got := m.input.Value(); got != "draft queued during compaction" {
		t.Fatalf("Esc changed draft to %q", got)
	}
	if m.state != tuiIdle {
		t.Fatalf("maintenance changed ordinary turn state to %v", m.state)
	}
}
