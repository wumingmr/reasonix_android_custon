package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

func recordCanonicalUsage(t *testing.T, tab *WorkspaceTab, requests int) {
	t.Helper()
	tab.sink.recordUsageTelemetry(event.Event{
		Usage:   &provider.Usage{PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140, RequestCount: requests},
		Pricing: &provider.Pricing{CacheHit: 1, Input: 2, Output: 3, Currency: "¥"},
	})
}

func TestCanonicalSessionTelemetrySurvivesSwitchingAway(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	sourceRef := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}

	recordCanonicalUsage(t, tab, 3)
	before := tab.telemetrySnapshot().Usage
	if before.RequestCount != 3 || before.SessionCost <= 0 {
		t.Fatalf("recorded usage = %+v", before)
	}

	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatalf("open other session: %v", err)
	}
	if got := tab.telemetrySnapshot().Usage; got.RequestCount != 0 {
		t.Fatalf("other session inherited usage: %+v", got)
	}
	if _, err := app.OpenSession(sourceRef); err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	got := tab.telemetrySnapshot().Usage
	if got.RequestCount != before.RequestCount || got.PromptTokens != before.PromptTokens ||
		got.CompletionTokens != before.CompletionTokens || got.SessionCost != before.SessionCost {
		t.Fatalf("usage after switching back = %+v, want %+v", got, before)
	}
}

func TestCanonicalSessionTelemetryToleratesCorruptStore(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	sourceRef := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}
	recordCanonicalUsage(t, tab, 2)

	path := canonicalTelemetryPath(sourceRef.SessionID)
	if path == "" {
		t.Fatal("no telemetry path for a canonical session")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenSession(sourceRef); err != nil {
		t.Fatalf("corrupt telemetry must not fail the switch: %v", err)
	}
	if got := tab.telemetrySnapshot().Usage.RequestCount; got != 0 {
		t.Fatalf("corrupt store yielded request count %d, want 0", got)
	}
}

func TestCanonicalTelemetryPathRejectsUnsafeIDs(t *testing.T) {
	isolateDesktopUserDirs(t)
	for _, id := range []string{"", "  ", "../escape", "a/b", `a\b`, ".", "a\x00b", ".hidden", "trailing.", strings.Repeat("a", 256), "CON", "nul.x", "COM1", "a:b"} {
		if got := canonicalTelemetryPath(id); got != "" {
			t.Fatalf("canonicalTelemetryPath(%q) = %q, want empty", id, got)
		}
	}
	if got := canonicalTelemetryPath("session-A"); filepath.Base(got) != "session-A.telemetry.json" {
		t.Fatalf("path = %q", got)
	}
}

func TestCanonicalSessionReadTelemetrySurvivesSwitchingAway(t *testing.T) {
	app, tab, target, _, _ := canonicalWorkspaceOpenFixture(t)
	sourceRef := session.SessionRef{HostID: localDesktopHostID, SessionID: tab.SessionID}

	tab.sink.recordReadTelemetry(event.Event{Tool: event.Tool{Name: "read_file", Args: `{"path":"README.md"}`}})
	if got := tab.telemetrySnapshot().ReadFiles; len(got) != 1 {
		t.Fatalf("recorded read files = %+v", got)
	}
	if _, err := app.OpenSession(target.Ref()); err != nil {
		t.Fatal(err)
	}
	if got := tab.telemetrySnapshot().ReadFiles; len(got) != 0 {
		t.Fatalf("other session inherited reads: %+v", got)
	}
	if _, err := app.OpenSession(sourceRef); err != nil {
		t.Fatal(err)
	}
	if got := tab.telemetrySnapshot().ReadFiles; len(got) != 1 || got[0].Path != "README.md" {
		t.Fatalf("reads after switching back = %+v", got)
	}
}
