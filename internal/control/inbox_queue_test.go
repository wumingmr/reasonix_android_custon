package control

import (
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/sessioninbox"
)

func TestInboxQueueFullBodySavePreservesEnvelopeAndSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "first.jsonl")
	c := newOwnedTestController(t, Options{SessionDir: dir, SessionPath: path, Sink: event.Discard})
	defer c.Close()
	if err := c.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("正文", 150) + "\n尾部"
	receipt, err := c.EnqueueInbox(InboxRequest{Submit: body, Idempotency: "original"})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := c.ensureInbox()
	meta, env, _ := st.ReadItem(receipt.ItemID)
	env.FrozenRefBlock = "frozen reference bytes"
	env.Extra = map[string]string{"owned": "keep"}
	env.Attachments = []string{"report.txt"}
	if _, err := st.UpdateItemIfVersion(meta.ID, env, sessioninbox.ContentVersion(meta)); err != nil {
		t.Fatal(err)
	}
	read, err := c.InboxQueue(path, InboxQueueRequest{Kind: "read", ItemID: meta.ID})
	if err != nil || read.Edit == nil || read.Edit.Text != body {
		t.Fatalf("full read: %+v %v", read, err)
	}
	newBody := "  " + body + "\n修改  "
	result, err := c.InboxQueue(path, InboxQueueRequest{Kind: "edit", ItemID: meta.ID, ContentVersion: read.Edit.ContentVersion, Text: newBody})
	if err != nil || result.Outcome != "applied" {
		t.Fatalf("save: %+v %v", result, err)
	}
	_, saved, _ := st.ReadItem(meta.ID)
	if saved.SubmitText != newBody || saved.FrozenRefBlock != env.FrozenRefBlock || saved.Extra["owned"] != "keep" || len(saved.Attachments) != 1 || saved.Idempotency != "original" {
		t.Fatalf("lost envelope: %+v", saved)
	}
	if !result.Snapshot.Paused || result.Snapshot.Items[0].ID != meta.ID {
		t.Fatal("save changed queue policy")
	}
	conflict, _ := c.InboxQueue(path, InboxQueueRequest{Kind: "edit", ItemID: meta.ID, ContentVersion: read.Edit.ContentVersion, Text: "stale"})
	if conflict.Reason != "content_changed" {
		t.Fatal(conflict)
	}
	c.SetSessionPath(filepath.Join(dir, "second.jsonl"))
	stale, err := c.InboxQueue(path, InboxQueueRequest{Kind: "pause", Paused: true})
	if err != nil || stale.Reason != "session_changed" || stale.Snapshot.SessionPath != "" {
		t.Fatalf("wrong-session result: %+v %v", stale, err)
	}
}

func TestInboxQueueDispatchExecutesSavedTextInMovedOrder(t *testing.T) {
	c, runner, done := newInboxDispatchController(t)
	if err := c.SetInboxPaused(true); err != nil {
		t.Fatal(err)
	}
	a, err := c.EnqueueInbox(InboxRequest{Submit: "first"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.EnqueueInbox(InboxRequest{Submit: "second"})
	if err != nil {
		t.Fatal(err)
	}
	path := c.SessionPath()
	read, _ := c.InboxQueue(path, InboxQueueRequest{Kind: "read", ItemID: b.ItemID})
	saved, err := c.InboxQueue(path, InboxQueueRequest{Kind: "edit", ItemID: b.ItemID, Text: "second edited", ContentVersion: read.Edit.ContentVersion})
	if err != nil || saved.Outcome != "applied" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	moved, err := c.InboxQueue(path, InboxQueueRequest{Kind: "move", ItemID: b.ItemID, BeforeItemID: &a.ItemID, QueueRevision: saved.Snapshot.Revision})
	if err != nil || moved.Outcome != "applied" {
		t.Fatalf("move: %+v %v", moved, err)
	}
	if err := c.SetInboxPaused(false); err != nil {
		t.Fatal(err)
	}
	if got := waitForInboxDispatch(t, c, runner); got != "second edited" {
		t.Fatalf("first dispatched %q", got)
	}
	waitForInboxTurnDone(t, c, done)
	if got := waitForInboxDispatch(t, c, runner); got != "first" {
		t.Fatalf("second dispatched %q", got)
	}
	waitForInboxTurnDone(t, c, done)
}
