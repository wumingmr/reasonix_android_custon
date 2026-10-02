package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// These files were emitted by the actual tagged writers, including their event
// logs, digests and external content objects. SOURCE.json pins the provenance;
// no fixture is constructed using the current writer's idea of an old format.
func taggedHistoryFixture(t *testing.T, version string) string {
	t.Helper()
	root := filepath.Join("testdata", "session-history-upgrade", version)
	var source struct {
		Tag, Commit string
		Files       map[string]string
	}
	readTaggedJSON(t, filepath.Join(root, "SOURCE.json"), &source)
	if source.Tag != "desktop-v"+version || len(source.Commit) != 40 || len(source.Files) == 0 {
		t.Fatal("missing historical source provenance")
	}
	for name, digest := range source.Files {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != digest {
			t.Fatalf("historical fixture changed: %s", name)
		}
	}
	return root
}

func readTaggedJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, value); err != nil {
		t.Fatal(err)
	}
}

func copyTaggedDirectory(t *testing.T, source, destination string) map[string][]byte {
	t.Helper()
	before := map[string][]byte{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		before[target] = body
		return os.WriteFile(target, body, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return before
}

func assertTaggedMessages(t *testing.T, got, want []provider.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("historical messages: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			// Do not print the large tool output on failure.
			g, _ := json.Marshal(got[i])
			w, _ := json.Marshal(want[i])
			t.Fatalf("message %d (%s) changed: got sha256=%x want=%x", i, want[i].ID, sha256.Sum256(g), sha256.Sum256(w))
		}
	}
}

func assertTaggedPages(t *testing.T, app *App, ref session.SessionRef, want []provider.Message) {
	t.Helper()
	query := app.desktopSessionService("").Query()
	// Await the public synchronous projection query. This matrix validates
	// durable reachability; asynchronous cold-read latency has separate tests.
	if _, err := query.HistoryShape(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	request := session.HistoryWindowRequest{Anchor: "newest", Limit: 17}
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages > len(want) {
			t.Fatal("history cursor failed to advance")
		}
		page, err := query.ReadHistoryWindow(t.Context(), ref, request)
		if err != nil || page.Status != "ready" || len(page.Messages) > 17 {
			t.Fatalf("historical page: %s %v", page.Status, err)
		}
		for _, message := range page.Messages {
			if seen[message.MessageID] {
				t.Fatalf("duplicate history identity %s", message.MessageID)
			}
			seen[message.MessageID] = true
		}
		if !page.HasOlder {
			break
		}
		if page.OlderCursor == "" {
			t.Fatal("unreachable older history")
		}
		request = session.HistoryWindowRequest{Anchor: "cursor", Cursor: page.OlderCursor, Limit: 17}
	}
	for _, message := range want {
		if message.Role != provider.RoleSystem && !seen[message.ID] {
			t.Fatalf("historical message unreachable: %s", message.ID)
		}
	}
}

func TestTaggedHistory1383To1385(t *testing.T)   { taggedHistoryReleaseUpgradeMatrix(t, 3, 5) }
func TestTaggedHistory1386To1387(t *testing.T)   { taggedHistoryReleaseUpgradeMatrix(t, 6, 7) }
func TestTaggedHistory1388To1389(t *testing.T)   { taggedHistoryReleaseUpgradeMatrix(t, 8, 9) }
func TestTaggedHistory13810To13811(t *testing.T) { taggedHistoryReleaseUpgradeMatrix(t, 10, 11) }

func taggedHistoryReleaseUpgradeMatrix(t *testing.T, first, last int) {
	t.Helper()
	for release := first; release <= last; release++ {
		version := fmt.Sprintf("1.38.%d", release)
		for _, scope := range []string{"global", "project"} {
			t.Run(version+"/"+scope, func(t *testing.T) {
				isolateDesktopUserDirs(t)
				fixture := taggedHistoryFixture(t, version)
				legacyRoot, canonicalRoot := config.SessionDir(), config.SessionStoreDir()
				if scope == "project" {
					workspace := filepath.Join(t.TempDir(), "历史项目 # % 中文")
					if err := os.MkdirAll(workspace, 0700); err != nil {
						t.Fatal(err)
					}
					legacyRoot, canonicalRoot = desktopSessionDir(workspace), config.ProjectSessionStoreDir(workspace)
					if err := saveProjectsFile(desktopProjectFile{Projects: []desktopProject{{Root: workspace}}}); err != nil {
						t.Fatal(err)
					}
				}
				before := copyTaggedDirectory(t, filepath.Join(fixture, "legacy"), legacyRoot)
				if release >= 8 {
					maps.Copy(before, copyTaggedDirectory(t, filepath.Join(fixture, "canonical"), canonicalRoot))
				}
				var identities map[string]string
				for restart := range 3 {
					app := NewApp()
					t.Cleanup(app.closeSessionServices)
					t.Cleanup(func() { _ = app.sessionUIStore().Close() })
					if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
						t.Fatal(err)
					}
					ledger, err := readDesktopMigrationLedger()
					if err != nil {
						t.Fatal(err)
					}
					current := map[string]string{}
					for _, name := range []string{"complete", "branch", "interrupted", "canonical-complete", "canonical-compacted", "canonical-interrupted"} {
						canonical := len(name) > 10 && name[:10] == "canonical-"
						if canonical && release < 8 {
							continue
						}
						key := desktopLegacyMigrationKey(filepath.Join(legacyRoot, name+".jsonl"))
						expected := name + "-expected.json"
						if canonical {
							key, expected = desktopCanonicalMigrationKey(canonicalRoot, name), "canonical-expected.json"
						}
						record := ledger.Records[key]
						if record.Status != "completed" || record.TargetSessionID == "" {
							t.Fatalf("%s failed adoption: %+v", name, record)
						}
						current[name] = record.TargetSessionID
						ref := session.SessionRef{HostID: localDesktopHostID, SessionID: record.TargetSessionID}
						snapshot, err := app.desktopSessionService("").Query().Snapshot(t.Context(), ref)
						if err != nil {
							t.Fatal(err)
						}
						var messages []provider.Message
						readTaggedJSON(t, filepath.Join(fixture, expected), &messages)
						assertTaggedMessages(t, snapshot.Projection.Messages, messages)
						if canonical && !bytes.Contains(snapshot.Projection.GoalState, []byte(`"futureGoal":{"keep":true}`)) {
							t.Fatal("canonical goal state lost")
						}
						if name == "canonical-compacted" && (len(snapshot.Projection.ModelMessages) != 1 || snapshot.Projection.ModelMessages[0].Content != "Persisted compaction summary") {
							t.Fatal("compaction replaced by full history on upgrade")
						}
						assertTaggedPages(t, app, ref, messages)
						input, err := app.GetSessionComposerState(ref)
						if err != nil || input.HistoryChanged {
							t.Fatalf("input recovery incorrectly blocked: %+v %v", input, err)
						}
						if restart == 0 {
							_, err = app.SaveSessionComposerState(SessionComposerSaveRequest{Ref: ref, ExpectedRevision: input.Revision, ContentVersion: 1, ContentJSON: `{"text":"new unsent input on historical session"}`})
							if err != nil {
								t.Fatal(err)
							}
						} else if input.ContentJSON != `{"text":"new unsent input on historical session"}` {
							t.Fatal("historical session input lost on restart")
						}
					}
					if restart > 0 && !reflect.DeepEqual(identities, current) {
						t.Fatal("restart remapped historical sessions")
					}
					identities = current
					assertMigrationSourceSnapshot(t, before)
					app.closeSessionServices()
					if err := app.sessionUIStore().Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
