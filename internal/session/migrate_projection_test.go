package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func legacyCompactionFixture(t *testing.T) (string, []provider.Message, []provider.Message) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions", "legacy.jsonl")
	s := agent.NewSession("system")
	for i := range 24 {
		s.Add(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("task %d", i)})
		s.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("x", 2_000)})
	}
	s.Add(provider.Message{Role: provider.RoleUser, Content: "continue"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "ready"})
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	exec := agent.New(testutil.NewMock("migration", testutil.Turn{Text: "durable summary"}), tool.NewRegistry(), s, agent.Options{
		SessionPath: path, ContextWindow: 10_000, CompactRatio: .8,
	}, event.Discard)
	if err := exec.CompactNow(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	return path, s.Snapshot(), provider.ModelMessages(exec.ModelHistorySnapshot())
}

func migratedProjection(t *testing.T, dir string) Projection {
	t.Helper()
	commits, err := Replay(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := Project(commits)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func TestMigrateLegacyPreservesValidContextProjection(t *testing.T) {
	path, canonical, compacted := legacyCompactionFixture(t)
	result, err := MigrateLegacy(t.Context(), path, filepath.Join(t.TempDir(), "sessions-v5"))
	if err != nil {
		t.Fatal(err)
	}
	projection := migratedProjection(t, result.TargetDir)
	if !reflect.DeepEqual(projection.Messages, canonical) {
		t.Fatal("migration changed canonical history")
	}
	if !reflect.DeepEqual(projection.ModelMessages, compacted) {
		t.Fatal("migration discarded the valid compacted model projection")
	}
}

func TestMigrateLegacyIgnoresInvalidProjectionWithDiagnostic(t *testing.T) {
	path, canonical, _ := legacyCompactionFixture(t)
	state, ok, err := agent.LoadCompactionState(path)
	if err != nil || !ok {
		t.Fatalf("load sidecar: ok=%v err=%v", ok, err)
	}
	state.Projection.CoveredPrefixHash = "does-not-match-canonical-history"
	if err := agent.SaveCompactionState(path, state); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateLegacy(t.Context(), path, filepath.Join(t.TempDir(), "sessions-v5"))
	if err != nil {
		t.Fatal(err)
	}
	projection := migratedProjection(t, result.TargetDir)
	if !reflect.DeepEqual(projection.ModelMessages, provider.ModelMessages(canonical)) {
		t.Fatal("invalid projection replaced the canonical model history")
	}
	found := false
	if err := VisitCommits(t.Context(), result.TargetDir, func(commit Commit) error {
		for _, item := range commit.Events {
			if item.Kind != "diagnostic" {
				continue
			}
			var payload map[string]string
			if err := json.Unmarshal(item.Payload, &payload); err != nil {
				return err
			}
			found = found || payload["code"] == "legacy_context_projection_ignored"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("invalid legacy projection did not leave a diagnostic")
	}
}

func TestMigrateLegacyRepairsOnlyPristinePublishedTarget(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprintf("advanced=%v", advanced), func(t *testing.T) {
			path, canonical, compacted := legacyCompactionFixture(t)
			targetRoot := filepath.Join(t.TempDir(), "sessions-v5")
			oldImporter, err := freezeLegacyHead(t.Context(), path, "", false)
			if err != nil {
				t.Fatal(err)
			}
			oldImporter.modelMessages = nil
			first, err := oldImporter.publish(t.Context(), targetRoot, CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if advanced {
				target, err := OpenWithOptions(first.TargetDir, first.TargetID, OpenOptions{ExternalHistory: true})
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "new-work", Role: provider.RoleUser, Content: "new work"}})
				if _, err := target.Append(t.Context(), Batch{OperationID: "new-work", Events: []Event{{Kind: "message/complete", Payload: raw}}}); err != nil {
					t.Fatal(err)
				}
				if _, err := target.Flush(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := target.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
			}

			currentImporter, err := freezeLegacyHead(t.Context(), path, "", false)
			if err != nil {
				t.Fatal(err)
			}
			second, err := currentImporter.publish(t.Context(), targetRoot, CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !second.Reused {
				t.Fatal("existing deterministic target was not reused")
			}
			projection := migratedProjection(t, second.TargetDir)
			if advanced {
				if reflect.DeepEqual(projection.ModelMessages, compacted) || len(projection.Messages) != len(canonical)+1 {
					t.Fatal("repair overwrote a target that already contained new work")
				}
				return
			}
			if !reflect.DeepEqual(projection.ModelMessages, compacted) {
				t.Fatal("pristine legacy target was not repaired with its valid projection")
			}
		})
	}
}
