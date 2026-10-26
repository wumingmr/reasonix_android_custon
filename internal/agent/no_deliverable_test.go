package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestNoDeliverableRecoveryDoesNotResetAfterTools(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		reasoningOnlyStop("I should execute."),
		{toolCallChunk("read-once", "read_file", `{}`), {Type: provider.ChunkDone}},
		reasoningOnlyStop("I should answer."),
		{{Type: provider.ChunkText, Text: "next user turn"}, {Type: provider.ChunkDone}},
	}}
	reg := tool.NewRegistry()
	reg.Add(fakeReadFileTool{})
	var summaries []*event.ResponseSummary
	a := New(prov, reg, NewSession("stable system"), Options{ModelRef: "p/test"}, event.FuncSink(func(e event.Event) {
		if e.StreamAttempt.Response != nil {
			summaries = append(summaries, e.StreamAttempt.Response)
		}
	}))
	if err := a.Run(t.Context(), "read and answer"); !errors.Is(err, provider.ErrNoDeliverableOutput) {
		t.Fatalf("Run = %v", err)
	}
	if prov.call != 3 || countUserMessagesContaining(a.Session(), "visible answer") != 1 {
		t.Fatalf("recovery restarted after a tool: calls=%d", prov.call)
	}
	// The recovery is append-only; original prompt and reasoning stay intact.
	prefix := prov.requests[0].Messages
	if !reflect.DeepEqual(prefix, prov.requests[1].Messages[:len(prefix)]) {
		t.Fatal("recovery rewrote the provider-visible prefix")
	}
	if summaries[0].FinishReason != "stop" || summaries[0].TextBytes != 0 || summaries[0].ReasoningBytes == 0 ||
		summaries[0].ModelRef != "p/test" || !summaries[0].StreamComplete {
		t.Fatalf("incomplete response diagnostics: %+v", summaries[0])
	}
	// Only the reply to the recovery prompt is attributed to it; later rounds
	// in the same Run are ordinary responses.
	if got := []int{summaries[0].RecoveryAttempts, summaries[1].RecoveryAttempts, summaries[2].RecoveryAttempts}; !reflect.DeepEqual(got, []int{0, 1, 0}) {
		t.Fatalf("recovery attribution = %v, want [0 1 0]", got)
	}
	toolResults := 0
	for _, m := range a.Session().Snapshot() {
		if m.Role == provider.RoleTool && m.ToolCallID == "read-once" {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Fatalf("tool replayed or lost: results=%d", toolResults)
	}
	if err := a.Run(t.Context(), "continue"); err != nil || prov.call != 4 {
		t.Fatalf("new user turn unavailable: calls=%d, err=%v", prov.call, err)
	}
}

func TestNoDeliverableRecoveryHonorsCancellationAndBudget(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "budget", true: "cancel"}[cancel], func(t *testing.T) {
			prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{{
				{Type: provider.ChunkReasoning, Text: "thinking"},
				{Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10, FinishReason: "stop"}},
				{Type: provider.ChunkDone},
			}}}
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			opts := Options{}
			if !cancel {
				opts.TaskBudget = TaskBudget{Tokens: 1}
			}
			a := New(prov, tool.NewRegistry(), NewSession(""), opts, event.FuncSink(func(e event.Event) {
				if cancel && e.Kind == event.StreamAttempt && e.StreamAttempt.Action == event.StreamAttemptCommit {
					stop()
				}
			}))
			err := a.Run(ctx, "answer")
			var budget *taskBudgetPause
			if cancel && !errors.Is(err, context.Canceled) || !cancel && !errors.As(err, &budget) {
				t.Fatalf("Run=%v", err)
			}
			if prov.call != 1 || sessionHasUserMessageContaining(a.Session(), "visible answer") {
				t.Fatalf("recovery bypassed stop: calls=%d", prov.call)
			}
		})
	}
}

func TestCancelAfterCommittedAnswerEndsRunCancelled(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "complete answer"}, {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "must not run"}, {Type: provider.ChunkDone}},
	}}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, event.FuncSink(func(e event.Event) {
		if e.Kind == event.StreamAttempt && e.StreamAttempt.Action == event.StreamAttemptCommit {
			stop()
		}
	}))
	if err := a.Run(ctx, "answer"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run=%v, want cancellation to win after commit", err)
	}
	if prov.call != 1 || lastAssistantContent(a.Session()) != "complete answer" {
		t.Fatalf("calls=%d, committed answer=%q", prov.call, lastAssistantContent(a.Session()))
	}
}

func TestNoDeliverableBudgetSpentDuringRecoveryPauses(t *testing.T) {
	stop := func() []provider.Chunk {
		return []provider.Chunk{
			{Type: provider.ChunkReasoning, Text: "thinking"},
			{Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10, FinishReason: "stop"}},
			{Type: provider.ChunkDone},
		}
	}
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{stop(), stop()}}
	// The first stop stays under budget; the recovery round crosses it.
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{TaskBudget: TaskBudget{Tokens: 15}}, event.Discard)
	err := a.Run(t.Context(), "answer")
	var budget *taskBudgetPause
	if !errors.As(err, &budget) || errors.Is(err, provider.ErrNoDeliverableOutput) || prov.call != 2 {
		t.Fatalf("Run=%v, calls=%d, want resumable budget pause", err, prov.call)
	}
}

func TestNoDeliverableFailsWithoutProviderSpecificPolicy(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		reasoningOnlyStop("thinking"),
		reasoningOnlyStop("still thinking"),
	}}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, event.Discard)
	err := a.Run(t.Context(), "answer")
	if !errors.Is(err, provider.ErrNoDeliverableOutput) || prov.call != 2 {
		t.Fatalf("Run=%v, calls=%d", err, prov.call)
	}
	if d := provider.DiagnoseFailure(err); d == nil || d.Kind != "no_deliverable_output" {
		t.Fatalf("failure classification=%+v", d)
	}
	if provider.ClassifyRecovery(err).Retryable {
		t.Fatal("transport recovery must not restart the exhausted turn")
	}
	// Host diagnostics must never become model-visible context.
	for _, req := range prov.requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "recoveryAttempts") || strings.Contains(m.Content, "streamComplete") {
				t.Fatal("diagnostics leaked into prompt")
			}
		}
	}
}

type failureMarkerRecorder struct {
	settlementRecorder
	err error
}

func (r *failureMarkerRecorder) RecordSessionMessages(ctx context.Context, reason string, messages []provider.Message) error {
	if reason == "no-deliverable-output" {
		return r.err
	}
	return r.settlementRecorder.RecordSessionMessages(ctx, reason, messages)
}

func TestNoDeliverableFailureMarkerPersistenceWins(t *testing.T) {
	saveErr := errors.New("fixture storage unavailable")
	recorder := &failureMarkerRecorder{
		settlementRecorder: settlementRecorder{accepted: make(map[string]provider.Message)},
		err:                saveErr,
	}
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		reasoningOnlyStop("thinking"),
		reasoningOnlyStop("still thinking"),
	}}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{SessionCheckpointer: recorder}, event.Discard)
	if err := a.Run(t.Context(), "answer"); !errors.Is(err, saveErr) || prov.call != 2 {
		t.Fatalf("storage failure was hidden or retried: err=%v calls=%d", err, prov.call)
	}
}
