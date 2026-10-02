package main

import (
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/session"
)

func TestFormalComposerTargetPreservesFileBrowserAndIdentity(t *testing.T) {
	a := newManualSessionTestApp(t)
	view, err := a.BeginManualSessionCreation(ManualSessionCreationRequest{OperationID: "target-wiring-session", Scope: "global"})
	if err != nil {
		t.Fatal(err)
	}
	a.manualCreationTasks.Wait()
	meta := a.metaForDraftSession(view.Ref.SessionID)
	if meta == nil {
		t.Fatal("missing created tab")
	}
	_, api := a.tabAndCtrlByID(meta.ID)
	ctrl := api.(*control.Controller)
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "outside.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	token, _, err := ctrl.RegisterExternalFolderRef(external)
	if err != nil {
		t.Fatal(err)
	}
	target := ComposerTarget{Kind: "session", TabID: meta.ID, Session: &view.Ref}
	listed := a.ListDirForTarget(target, token+"/")
	if len(listed) != 1 || listed[0].Name != "outside.txt" {
		t.Fatalf("lost registered external directory: %+v", listed)
	}
	found := false
	for _, hit := range a.SearchFileRefsForTarget(target, "outside") {
		if hit.Path == token+"/outside.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("external search omitted formal session controller")
	}

	captured, err := a.CaptureAttachmentTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	a.ReleaseAttachmentTarget(captured.Token)
	wrong := session.SessionRef{HostID: "local", SessionID: "other-session"}
	target.Session = &wrong
	if _, err := a.CaptureAttachmentTarget(target); err == nil {
		t.Fatal("mismatched SessionRef accepted for image staging")
	}
	if _, _, err := a.composerTargetWorkspace(target); err == nil {
		t.Fatal("mismatched SessionRef accepted for file browsing")
	}

	// Recovery can read a persisted path without relying on a resident tab.
	data := "data:image/png;base64," + desktopTinyPNG
	path, err := a.SavePastedImageForComposerTarget(ComposerTarget{Kind: "session", Session: &view.Ref}, data)
	if err != nil {
		t.Fatal(err)
	}
	if preview, err := a.AttachmentDataURLForComposerTarget(ComposerTarget{Kind: "session", Session: &view.Ref}, path); err != nil || preview != data {
		t.Fatalf("durable image source: %q %v", preview, err)
	}
}
