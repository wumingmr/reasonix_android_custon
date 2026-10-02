package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"reasonix/desktop/internal/sessionui"
	"reasonix/internal/provider"
)

// Opt-in integration gate: executables come from the tagged fixture generator,
// not from a current implementation pretending to be a previous reader.
func TestTaggedPreviousWriterRoundtrip(t *testing.T) {
	writers := os.Getenv("REASONIX_TAGGED_WRITERS")
	if writers == "" {
		t.Skip("run generate-session-history-fixtures.py --writers-dir and set REASONIX_TAGGED_WRITERS")
	}
	for _, version := range []string{"1.38.10", "1.38.11"} {
		t.Run(version, func(t *testing.T) {
			a, ref := lifecycleFixture(t)
			binding, err := a.desktopSessionService("").Open(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			appendSessionTestMessage(t, binding.Runtime(), "new-writer", provider.Message{ID: "new-user", Role: provider.RoleUser, Content: "New-version history before downgrade"})
			if err := binding.Release(t.Context()); err != nil {
				t.Fatal(err)
			}
			saved, err := a.SaveSessionComposerState(SessionComposerSaveRequest{Ref: ref, ExpectedRevision: "0", ContentVersion: 1, ContentJSON: `{"text":"input retained while old binary runs"}`})
			if err != nil {
				t.Fatal(err)
			}
			path := a.sessionUIStore().Path()
			if err := a.sessionUIStore().Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			a.closeSessionServices()
			sources := migrationSourceSnapshot(t, canonicalMigrationSourceFiles(a.desktopSessions.root, ref.SessionID))
			command := exec.CommandContext(t.Context(), filepath.Join(writers, version), filepath.Join(a.desktopSessions.root, ref.SessionID), ref.SessionID)
			output, err := command.Output()
			if err != nil {
				t.Fatalf("tagged writer failed: %v\n%s", err, output)
			}
			var result struct{ Status string }
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("previous writer changed the independent UI database")
			}
			a.sessionUI = sessionui.New(path)
			t.Cleanup(func() { _ = a.sessionUIStore().Close() })
			recovered, err := a.GetSessionComposerState(ref)
			if version == "1.38.10" {
				// #10545 raised physical storage revision 2 to 3. Registry v3
				// readability alone never promised whole-session downgrade safety.
				if result.Status != "unsupported_version" || err != nil || recovered.HistoryChanged || recovered.ContentJSON != saved.ContentJSON {
					t.Fatalf("unsafe old-reader rejection: %+v %+v %v", result, recovered, err)
				}
				assertMigrationSourceSnapshot(t, sources)
				return
			}
			if result.Status != "continued" {
				t.Fatalf("old reader could not continue: %+v", result)
			}
			if err != nil || !recovered.HistoryChanged || recovered.ContentJSON != saved.ContentJSON {
				t.Fatalf("unsafe reupgrade after real old write: %+v %v", recovered, err)
			}
			if _, err := a.BeginSessionComposerSubmission(ref, recovered.Revision, "must-not-send", `{}`); err == nil {
				t.Fatal("stale input became sendable without review")
			}
			history, err := a.desktopSessionService("").Query().History(t.Context(), ref)
			if err != nil || len(history) != 3 || history[0].ID != "user" || history[1].ID != "new-user" || history[2].ID != "old-version-user" {
				t.Fatalf("roundtrip history changed: %d messages %v", len(history), err)
			}
		})
	}
}
