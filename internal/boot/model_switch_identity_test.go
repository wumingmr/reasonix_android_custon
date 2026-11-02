package boot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// A model switch is not a session switch. When the outgoing controller owns a
// canonical identity, the rebuilt one must attach to that exact identity: the
// two controllers share one background scope, and activation refuses two
// controllers that claim the same background ownership for different sessions.
// Before this was fixed, every /model switch on a canonical session failed with
// "background ownership cannot cross sessions" and left the new model inactive
// until the user quit and resumed.
func TestModelSwitchKeepsTheCanonicalSessionIdentity(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	workspace := filepath.Join(dir, "ws")
	svc, err := session.NewService("local", session.NewFilesystemPersistence(filepath.Join(dir, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	old, err := Build(context.Background(), Options{
		Model: "deepseek-flash/deepseek-flash", WorkspaceRoot: workspace,
		SessionService: svc, Sink: event.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Close)
	old.EnsureSessionPath()
	old.AdoptHistory([]provider.Message{{Role: provider.RoleUser, Content: "hi"}}, old.SessionPath())
	if err := old.Snapshot(); err != nil {
		t.Fatal(err)
	}
	before, ok := old.SessionRef()
	if !ok {
		t.Fatal("fixture did not establish a canonical identity")
	}

	scope, finish, abort, err := control.ReserveBackgroundReplacement(old)
	if err != nil {
		t.Fatalf("reserve replacement: %v", err)
	}
	defer abort()

	rebuilt, err := Build(context.Background(), Options{
		Model: "deepseek-pro/deepseek-v4-pro", WorkspaceRoot: workspace,
		SessionService: svc, Sink: event.Discard, BackgroundScope: scope,
	})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	t.Cleanup(rebuilt.Close)

	// This is the line the CLI's buildController performs.
	if _, err := rebuilt.OpenSession(context.Background(), before); err != nil {
		t.Fatalf("attach rebuilt controller to the existing session: %v", err)
	}
	if err := finish(rebuilt); err != nil {
		t.Fatalf("finish replacement: %v", err)
	}

	after, ok := rebuilt.SessionRef()
	if !ok || after.SessionID != before.SessionID {
		t.Fatalf("identity changed: %q -> %q", before.SessionID, after.SessionID)
	}
	if err := control.ActivateControllerReplacement(old, rebuilt); err != nil {
		t.Fatalf("activation: %v", err)
	}
	if rebuilt.ModelRef() == old.ModelRef() {
		t.Fatalf("the switch did not change the model: %q", rebuilt.ModelRef())
	}

	// The carried conversation is written only after activation, because a
	// canonical session fences that commit on execution ownership. Before this
	// ordering, the CLI's write was silently dropped and the switched-to
	// profile's system prompt never reached the new controller.
	carry := []provider.Message{
		{Role: provider.RoleSystem, Content: "rebuilt-profile-prompt"},
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleUser, Content: "unsaved-before-switch"},
	}
	rebuilt.AdoptHistory(carry, old.SessionPath())
	var hasPrompt, hasUnsaved bool
	for _, m := range rebuilt.History() {
		if m.Role == provider.RoleSystem && strings.Contains(m.Content, "rebuilt-profile-prompt") {
			hasPrompt = true
		}
		if strings.Contains(m.Content, "unsaved-before-switch") {
			hasUnsaved = true
		}
	}
	if !hasPrompt || !hasUnsaved {
		t.Fatalf("carry lost after activation: prompt=%v unsaved=%v", hasPrompt, hasUnsaved)
	}
}

// A legacy path-only session has no canonical identity to preserve, so the
// rebuild continues on its path exactly as before.
func TestModelSwitchFallsBackToThePathForLegacySessions(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	old, err := Build(context.Background(), Options{
		Model: "deepseek-flash/deepseek-flash", WorkspaceRoot: filepath.Join(dir, "ws"),
		Sink: event.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(old.Close)
	if _, ok := old.SessionRef(); ok {
		t.Fatal("fixture unexpectedly owns a canonical identity")
	}
	// ContinueSessionPath keeps an established path, which is what the legacy
	// branch relies on; a canonical session reports "" and takes the other path.
	prev := filepath.Join(dir, "legacy.jsonl")
	if got := agent.ContinueSessionPath(prev, dir, "m"); got != prev {
		t.Fatalf("ContinueSessionPath(%q) = %q", prev, got)
	}
}
