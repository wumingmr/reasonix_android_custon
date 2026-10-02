package control

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/tool"
)

func TestCorruptQueuedImageBlocksExecution(t *testing.T) {
	root := t.TempDir()
	p := &reviewImageProvider{requests: make(chan provider.Request, 4)}
	ag := agent.New(p, tool.NewRegistry(), agent.NewSession("sys"), agent.Options{}, event.Discard)
	c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionPath: filepath.Join(root, "session.jsonl"), Runner: ag, Executor: ag})
	draft, err := c.StageImage(t.Context(), "queued.png", "image/png", "data:image/png;base64,"+tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := c.EnqueueInboxContext(t.Context(), InboxRequest{Submit: "inspect", Attachments: []SubmissionAttachment{{ClientAttachmentID: "queued", DraftID: draft.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	digest := draft.Ref.Content.Digest
	path := filepath.Join(c.attachmentService().Store().Root(), "objects", digest[:2], digest[2:4], digest)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.RunInboxTurn(t.Context(), receipt.ItemID); err == nil {
		t.Fatal("corrupt queued image executed")
	}
	st, err := c.ensureInbox()
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.ReadItem(receipt.ItemID)
	if err != nil || meta.State != sessioninbox.StateBlocked {
		t.Fatalf("queue state=%v err=%v", meta.State, err)
	}
	if len(p.requests) != 0 {
		t.Fatal("corrupt queued image reached provider")
	}
}

func TestAttachmentFingerprintIgnoresReboundCredentials(t *testing.T) {
	a := SubmissionRequest{Input: "inspect @draft:old", Display: "![photo](draft:old)", DraftIDs: []string{"old"}, Attachments: []SubmissionAttachment{{ClientAttachmentID: "photo", DraftID: "old"}}}
	b := cloneSubmissionRequest(a)
	b.Input, b.Display, b.DraftIDs, b.Attachments[0].DraftID = "inspect @draft:new", "![photo](draft:new)", []string{"new"}, "new"
	if canonicalSubmissionFingerprint(a) != canonicalSubmissionFingerprint(b) {
		t.Fatal("transport credential changed request identity")
	}
	if a.Attachments[0].DraftID != "old" {
		t.Fatal("fingerprinting mutated caller request")
	}
	b.Attachments[0].ClientAttachmentID = "different"
	if canonicalSubmissionFingerprint(a) == canonicalSubmissionFingerprint(b) {
		t.Fatal("logical attachment identity was ignored")
	}
}

func TestQueueReceiptSurvivesCredentialRenewalAndRelease(t *testing.T) {
	root := t.TempDir()
	c := newOwnedTestController(t, Options{WorkspaceRoot: root, SessionPath: filepath.Join(root, "session.jsonl")})
	draft, err := c.StageImage(t.Context(), "queue.png", "image/png", "data:image/png;base64,"+tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	req := InboxRequest{Submit: "inspect", Display: "![photo](draft:" + draft.ID + ")", Idempotency: "queue-stable", Attachments: []SubmissionAttachment{{ClientAttachmentID: "photo", DraftID: draft.ID}}}
	first, err := c.EnqueueInboxContext(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	c.ReleaseDraftImage(draft.ID)
	req.Attachments[0].DraftID = "replacement-credential"
	req.Display = "![photo](draft:replacement-credential)"
	again, err := c.EnqueueInboxContext(t.Context(), req)
	if err != nil || first.ItemID != again.ItemID {
		t.Fatalf("retry=%+v err=%v, want %s", again, err, first.ItemID)
	}
	req.Submit = "different user intent"
	if _, err := c.EnqueueInboxContext(t.Context(), req); err == nil {
		t.Fatal("different queue intent reused receipt")
	}
}
