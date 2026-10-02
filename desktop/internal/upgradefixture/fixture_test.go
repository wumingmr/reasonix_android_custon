package upgradefixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/desktop/internal/workspacestate"
)

func TestEncodeLegacyHistoryEscapesJSONContent(t *testing.T) {
	want := []legacyMessage{
		{Role: "user", Content: "quote \" slash \\ newline\n中文 %20 #"},
		{Role: "assistant", Content: "second line"},
	}
	body, err := encodeLegacyHistory(want...)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("encoded lines = %d, want %d: %q", len(lines), len(want), body)
	}
	for i, line := range lines {
		var got legacyMessage
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("decode line %d: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("line %d = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestVerifyLegacyHistoryChecksContentAfterByteRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	body, err := encodeLegacyHistory(
		legacyMessage{Role: "user", Content: fixtureQuestion},
		legacyMessage{Role: "assistant", Content: fixtureText},
	)
	if err != nil {
		t.Fatal(err)
	}
	// A normal shutdown may rewrite the active legacy checkpoint. Byte changes
	// are acceptable only while the authored conversation stays intact.
	rewritten := strings.ReplaceAll(string(body), "\n", " \n")
	if rewritten == string(body) {
		t.Fatal("fixture did not change legacy bytes")
	}
	if err := os.WriteFile(path, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyLegacyHistory(path, fixtureQuestion, fixtureText); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(rewritten, fixtureText, "different answer", 1)
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyLegacyHistory(path, fixtureQuestion, fixtureText); err == nil {
		t.Fatal("changed authored content passed verification")
	}
}

func TestRunRestoresEnvironmentOnSuccessAndFailure(t *testing.T) {
	for _, key := range []string{"REASONIX_HOME", "REASONIX_STATE_HOME", "REASONIX_CACHE_HOME"} {
		t.Setenv(key, "untouched-"+key)
	}
	home := filepath.Join(t.TempDir(), "isolated")
	report := filepath.Join(t.TempDir(), "fixture.json")
	for _, mode := range []string{"create", "verify", "invalid"} {
		err := Run(mode, home, report, "first")
		if mode == "create" && err != nil {
			t.Fatal(err)
		}
		if mode != "create" && err == nil {
			t.Fatalf("%s must fail before app startup", mode)
		}
		for _, key := range []string{"REASONIX_HOME", "REASONIX_STATE_HOME", "REASONIX_CACHE_HOME"} {
			if got := os.Getenv(key); got != "untouched-"+key {
				t.Fatalf("%s leaked %s=%q", mode, key, got)
			}
		}
	}
}

func TestVerifyPreparedImportRequiresOneCommittedLegacyImport(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "sessions", fixtureSessionID+".jsonl")
	report := fixtureReport{LegacyPath: legacy}
	prepared := func() *workspacestate.State {
		return &workspacestate.State{
			Workspaces: map[string]workspacestate.Workspace{workspacestate.GlobalWorkspaceID: {SessionIDs: []string{"s1"}}},
			SourceMappings: map[string]workspacestate.SourceMapping{"k": {
				Path: filepath.Join(filepath.Dir(legacy), ".", filepath.Base(legacy)), Format: "legacy", SessionID: "s1", WorkspaceID: workspacestate.GlobalWorkspaceID,
			}},
			PendingOperations: map[string]workspacestate.Operation{"import": {Phase: "committed"}},
		}
	}
	if id, err := verifyPreparedImport(prepared(), report); err != nil || id != "s1" {
		t.Fatalf("committed legacy import = %q, %v", id, err)
	}
	for name, corrupt := range map[string]func(*workspacestate.State){
		"not imported": func(s *workspacestate.State) { s.SourceMappings = nil },
		"open operation": func(s *workspacestate.State) {
			s.PendingOperations["import"] = workspacestate.Operation{Phase: "content_ready"}
		},
		"canonical format": func(s *workspacestate.State) {
			m := s.SourceMappings["k"]
			m.Format = "canonical"
			s.SourceMappings["k"] = m
		},
		"other source": func(s *workspacestate.State) {
			m := s.SourceMappings["k"]
			m.Path += ".other"
			s.SourceMappings["k"] = m
		},
		"extra session": func(s *workspacestate.State) {
			s.Workspaces[workspacestate.GlobalWorkspaceID] = workspacestate.Workspace{SessionIDs: []string{"s1", "s2"}}
		},
		"session elsewhere": func(s *workspacestate.State) {
			s.Workspaces[workspacestate.GlobalWorkspaceID] = workspacestate.Workspace{SessionIDs: []string{"s2"}}
		},
	} {
		state := prepared()
		corrupt(state)
		if _, err := verifyPreparedImport(state, report); err == nil {
			t.Errorf("%s passed prepared verification", name)
		}
	}
}
