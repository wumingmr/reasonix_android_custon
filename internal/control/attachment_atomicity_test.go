package control

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestMixedDraftAndOrdinaryWorkspaceImageAreBothPrepared(t *testing.T) {
	root := t.TempDir()
	c := newOwnedTestController(t, Options{WorkspaceRoot: root})
	draft, err := c.StageImage(t.Context(), "draft.png", "image/png", "data:image/png;base64,"+tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ordinary.png"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := c.PrepareSubmission(t.Context(), SubmissionRequest{Input: "inspect @ordinary.png", Attachments: []SubmissionAttachment{{ClientAttachmentID: "draft", DraftID: draft.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.images.inputs) != 2 {
		t.Fatalf("mixed image count = %d", len(prepared.images.inputs))
	}
}

func TestInvalidImageBatchNeverStartsEndpointOrGoal(t *testing.T) {
	for _, endpoint := range []string{"normal", "http", "edit", "goal", "run"} {
		t.Run(endpoint, func(t *testing.T) {
			root := t.TempDir()
			p := &reviewImageProvider{requests: make(chan provider.Request, 4)}
			ag := agent.New(p, tool.NewRegistry(), agent.NewSession("sys"), agent.Options{}, event.Discard)
			c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionPath: filepath.Join(root, "session.jsonl"), Runner: ag, Executor: ag})
			good, err := SaveImageDataURLInRoot(root, "data:image/png;base64,"+tinyPNG)
			if err != nil {
				t.Fatal(err)
			}
			input := "inspect @" + good + " @.reasonix/attachments/missing.png"
			setupCalled := false
			switch endpoint {
			case "run":
				err = c.RunTurn(t.Context(), input)
			default:
				req := SubmissionRequest{ID: "reject-batch", Input: input, HTTP: endpoint == "http"}
				if endpoint == "edit" {
					req.Original = "original"
				}
				_, err = c.SubmitIdentifiedWithSetupContext(t.Context(), req, func() error { setupCalled = true; return nil })
			}
			if err == nil || setupCalled || c.Running() {
				t.Fatalf("partial admission: err=%v setup=%v running=%v", err, setupCalled, c.Running())
			}
			if len(p.requests) != 0 {
				t.Fatal("invalid batch reached provider")
			}
			for _, msg := range ag.Session().Snapshot() {
				if msg.Role == provider.RoleUser {
					t.Fatal("rejected batch appended a user message")
				}
			}
		})
	}
}
