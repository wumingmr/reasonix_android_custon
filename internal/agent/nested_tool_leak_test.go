package agent

import (
	"context"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/provider"
)

func TestBackgroundTaskToolEventsStayNestedUnderParentCall(t *testing.T) {
	finalText := []provider.Chunk{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}}
	sub := &scriptedProvider{name: "sub", turns: [][]provider.Chunk{
		{toolCallChunk("c1", "bash", `{"command":"ls"}`), {Type: provider.ChunkDone}},
		finalText, finalText,
	}}
	task := NewTaskTool(sub, nil, evidenceRegistry(), 20, 0, 0, 0, 0, 0, 0, 0.0, "", "sys", nil, 0, "", "", nil).
		WithTranscripts(NewSubagentStore(t.TempDir()), t.TempDir(), "base-model", "base-effort")
	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	rec := &recordSink{}
	ctx := withCallContext(testTaskContext(), "task-call", rec, nil, false)
	ctx = jobs.WithSession(ctx, "parent-session")
	ctx = jobs.WithManager(ctx, jm)

	out, err := task.Execute(ctx, []byte(`{"prompt":"list","run_in_background":true}`))
	if err != nil {
		t.Fatal(err)
	}
	res := jm.WaitForSession(context.Background(), "parent-session", []string{extractJobID(out)}, 5)
	if len(res) != 1 || res[0].Status != jobs.Done {
		t.Fatalf("job = %+v", res)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	seen := 0
	for _, e := range rec.evs {
		if e.Tool.ID == "" || e.Tool.ID == "task-call" {
			continue
		}
		seen++
		if e.Tool.ParentID != "task-call" {
			t.Errorf("event kind %v tool %q id %q has ParentID %q, want task-call", e.Kind, e.Tool.Name, e.Tool.ID, e.Tool.ParentID)
		}
	}
	if seen == 0 {
		t.Fatal("no sub-agent tool events observed")
	}
}
