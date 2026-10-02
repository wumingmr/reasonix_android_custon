package control

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncontext"
)

func TestManualCompactHistoryReportsCompleted(t *testing.T) {
	for _, window := range []int{0, 128_000} {
		for _, slash := range []bool{false, true} {
			t.Run(fmt.Sprintf("window=%d/slash=%v", window, slash), func(t *testing.T) {
				prov := &recordingProvider{streams: [][]provider.Chunk{{
					{Type: provider.ChunkText, Text: "- Completed implementation."},
					{Type: provider.ChunkDone},
				}}}
				system := strings.Repeat("stable instructions ", 500)
				sess := agent.NewSession(system)
				sess.Add(agent.HostGeneratedUserMessage(sessioncontext.Build(sessioncontext.Sections{Workspace: strings.Repeat("workspace ", 500)}).Content))
				sess.Add(provider.Message{Role: provider.RoleUser, Content: "Implement this task."})
				sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("Implementation completed and verified. ", 50)})
				exec := agent.New(prov, nil, sess, agent.Options{ContextWindow: window}, event.Discard)
				dir := t.TempDir()
				c := newOwnedTestController(t, Options{Executor: exec, SystemPrompt: system, SessionDir: dir, SessionPath: filepath.Join(dir, "session.jsonl"), Sink: event.Discard})
				events := make(chan event.SessionOperationInfo, 8)
				c.sink = event.FuncSink(func(e event.Event) {
					if e.SessionOperation != nil {
						events <- *e.SessionOperation
					}
				})
				if slash {
					if result := c.SubmitDisplayWithResult("/compact", "/compact"); result.Disposition != SubmitManagementHandled {
						t.Fatalf("submit = %+v", result)
					}
				} else if err := c.Compact(t.Context(), ""); err != nil {
					t.Fatal(err)
				}
				for _, status := range []string{"running", "finalizing", "completed"} {
					select {
					case e := <-events:
						if e.Status != status {
							t.Fatalf("operation = %+v, want %s", e, status)
						}
						if status == "completed" && (!e.Applied || e.Summary == "" || e.ResultTokens >= e.InputTokens) {
							t.Fatalf("missing successful compaction receipt: %+v", e)
						}
					case <-time.After(5 * time.Second):
						t.Fatalf("missing %s event", status)
					}
				}
			})
		}
	}
}
