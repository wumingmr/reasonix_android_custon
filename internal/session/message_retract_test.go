package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestMessageRetractStrictSchema(t *testing.T) {
	for _, payload := range []string{`{}`, `{"messageIds":null}`, `{"messageIds":[]}`, `{"messageIds":[""]}`, `{"messageIds":["   "]}`, `{"messageIds":["a","a"]}`, `{"messageIds":["a"],"typo":true}`, `{"messageIds":"a"}`} {
		t.Run(payload, func(t *testing.T) {
			_, err := Project([]Commit{{Events: []Event{{Kind: "message/retract", Payload: json.RawMessage(payload)}}}})
			if !errors.Is(err, ErrDamagedStore) {
				t.Fatalf("invalid retract payload accepted: %s, err=%v", payload, err)
			}
		})
	}
}

func TestMessageRetractProjectionAndRestore(t *testing.T) {
	messageEvent := func(kind, id string) Event {
		payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: id}})
		if err != nil {
			t.Fatal(err)
		}
		return Event{Kind: kind, Payload: payload}
	}
	commits := []Commit{{Events: []Event{messageEvent("message/complete", "retained"), messageEvent("message/complete", "removed")}}, {Events: []Event{{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["removed"],"reason":"synthetic-turn-interrupted"}`)}}}}
	check := func(want []string) {
		t.Helper()
		p, err := Project(commits)
		if err != nil {
			t.Fatal(err)
		}
		for name, messages := range map[string][]provider.Message{"history": p.Messages, "model": p.ModelMessages} {
			ids := make([]string, len(messages))
			for i := range messages {
				ids[i] = messages[i].ID
			}
			if !idsEqual(ids, want) {
				t.Fatalf("%s ids=%v, want=%v", name, ids, want)
			}
		}
	}
	check([]string{"retained"})
	commits = append(commits, commits[1])
	check([]string{"retained"})
	commits = append(commits, Commit{Events: []Event{messageEvent("message/upsert", "removed")}})
	check([]string{"retained", "removed"})
}

func TestMessageRetractRestoresOriginalTurnAfterReopen(t *testing.T) {
	t.Run("checkpoint", func(t *testing.T) { testRetractedTurnRestore(t, false) })
	t.Run("log-replay", func(t *testing.T) { testRetractedTurnRestore(t, true) })
}

func testRetractedTurnRestore(t *testing.T, removeCache bool) {
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	user := json.RawMessage(`{"message":{"id":"input","role":"user","content":"restored input"}}`)
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "original", TurnID: "original-turn", Events: []Event{
		{Kind: "turn/start", Payload: json.RawMessage(`{}`)},
		{Kind: "message/complete", Payload: user},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "retract", Events: []Event{{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["input"]}`)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	ref := runtime.Ref()
	if err := service.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if removeCache {
		if err := os.RemoveAll(recoveryCacheDir(filepath.Join(root, ref.SessionID))); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := service.EnsureExecution(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Release(t.Context())
	runtime, _ = service.Runtime(ref)
	// Repair batches intentionally have no live turn identity.
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "restore", Events: []Event{{Kind: "message/upsert", Payload: user}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := runtime.Session().Snapshot().Projection
	if p.HiddenTurns["original-turn"] || len(p.TranscriptInputs) != 1 || p.TranscriptInputs[0].TurnID != "original-turn" {
		t.Fatalf("restored input lost its original turn: hidden=%v inputs=%+v", p.HiddenTurns, p.TranscriptInputs)
	}
	info, err := service.Query().Stat(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if info.Turns != 1 || info.Preview != "restored input" || runtime.Session().RecentSnapshot().TotalTurns != 1 {
		t.Fatalf("restored metadata disagrees: info=%+v recent=%+v", info, runtime.Session().RecentSnapshot())
	}
}

func TestRecoveredLocalOnlyMessageClearsOnlyItsClosedReplyAnchor(t *testing.T) {
	_, _, runtime := newSourceService(t, "recovery-anchor")
	appendCompletedTurn(t, runtime, "first", "first-reply")
	appendCompletedTurn(t, runtime, "second", "second-reply")
	payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "second-reply", Role: provider.RoleTool, LocalOnly: true}})
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "repair", Events: []Event{{Kind: "message/upsert", Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
	p := runtime.Session().Snapshot().Projection
	if p.Turns[0].MessageID != "first-reply" || p.Turns[1].MessageID != "" {
		t.Fatalf("invalid recovery anchors: %+v", p.Turns)
	}
}

func TestMessageRetractHistorySearchAndRestart(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		name := "rebuild"
		if incremental {
			name = "incremental"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sessions")
			service, err := NewService("local", NewFilesystemPersistence(root))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
			runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "retracted"})
			if err != nil {
				t.Fatal(err)
			}
			appendWindowMessages(t, runtime, "retained", "removed", "tail")
			query, ref := service.Query(), runtime.Ref()
			read := func(req HistoryWindowRequest) HistoryWindowPage {
				t.Helper()
				if _, _, err := query.prepareHistoryIndex(t.Context(), ref); err != nil {
					t.Fatal(err)
				}
				page, err := query.ReadHistoryWindow(t.Context(), ref, req)
				if err != nil || page.Status != "ready" {
					t.Fatalf("history: %+v, %v", page, err)
				}
				return page
			}
			var oldCursor string
			if incremental {
				oldCursor = read(HistoryWindowRequest{Anchor: "newest", Limit: 1}).OlderCursor
				if page := searchHistoryReady(t, query, ref, "body", "", 10); len(page.Hits) != 3 {
					t.Fatalf("initial search: %+v", page)
				}
			}
			appendEvent := func(op, kind, payload string) {
				t.Helper()
				if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: op, Events: []Event{{Kind: kind, Payload: json.RawMessage(payload)}}}); err != nil {
					t.Fatal(err)
				}
				if _, err := runtime.Session().Flush(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			appendEvent("retract", "message/retract", `{"messageIds":["removed"],"reason":"synthetic-turn-interrupted"}`)
			page := read(HistoryWindowRequest{Anchor: "newest"})
			if !idsEqual(windowIDs(t, page), []string{"retained", "tail"}) {
				t.Fatalf("retracted ids=%v", windowIDs(t, page))
			}
			if found := searchHistoryReady(t, query, ref, "removed", "", 10); len(found.Hits) != 0 {
				t.Fatalf("retracted search: %+v", found)
			}
			if incremental {
				old := read(HistoryWindowRequest{Anchor: "cursor", Cursor: oldCursor, Limit: 10})
				if !idsEqual(windowIDs(t, old), []string{"retained", "removed"}) {
					t.Fatalf("fixed snapshot changed: %v", windowIDs(t, old))
				}
			}
			appendEvent("retract-again", "message/retract", `{"messageIds":["removed"]}`)
			read(HistoryWindowRequest{Anchor: "newest"})
			appendEvent("restore", "message/upsert", `{"message":{"id":"removed","role":"user","content":"restored body-removed"}}`)
			checkRestored := func() {
				t.Helper()
				page := read(HistoryWindowRequest{Anchor: "newest"})
				if !idsEqual(windowIDs(t, page), []string{"retained", "tail", "removed"}) {
					t.Fatalf("restored ids=%v", windowIDs(t, page))
				}
				if page.Messages[2].Version != 2 {
					t.Fatalf("restored version=%d, want=2", page.Messages[2].Version)
				}
				if found := searchHistoryReady(t, query, ref, "restored", "", 10); len(found.Hits) != 1 {
					t.Fatalf("restored search: %+v", found)
				}
			}
			checkRestored()
			if err := service.CloseAll(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, removeCache := range []bool{false, true} {
				if removeCache {
					if err := os.RemoveAll(filepath.Join(root, ".query-cache")); err != nil {
						t.Fatal(err)
					}
				}
				reopened, err := NewService("local", NewFilesystemPersistence(root))
				if err != nil {
					t.Fatal(err)
				}
				query = reopened.Query()
				checkRestored()
				if err := reopened.CloseAll(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMessageRetractRenumbersTurnsWithoutChangingOldSnapshot(t *testing.T) {
	for _, removeID := range []string{"u1", "u2"} {
		for _, withAnswer := range []bool{false, true} {
			name := removeID
			if withAnswer {
				name += "-with-answer"
			}
			t.Run(name, func(t *testing.T) {
				service, err := NewService("local", NewFilesystemPersistence(filepath.Join(t.TempDir(), "sessions")))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
				runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "turn-renumber"})
				if err != nil {
					t.Fatal(err)
				}
				appendMessage := func(op, kind, id string, role provider.Role) {
					t.Helper()
					payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: role, Content: "body-" + id}})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: op, Events: []Event{{Kind: kind, Payload: payload}}}); err != nil {
						t.Fatal(err)
					}
				}
				for _, id := range []string{"u1", "a1", "u2", "a2", "u3", "a3"} {
					role := provider.RoleUser
					if id[0] == 'a' {
						role = provider.RoleAssistant
					}
					appendMessage(id, "message/complete", id, role)
				}
				read := func(req HistoryWindowRequest) HistoryWindowPage {
					t.Helper()
					if _, err := runtime.Session().Flush(t.Context()); err != nil {
						t.Fatal(err)
					}
					if _, _, err := service.Query().prepareHistoryIndex(t.Context(), runtime.Ref()); err != nil {
						t.Fatal(err)
					}
					page, err := service.Query().ReadHistoryWindow(t.Context(), runtime.Ref(), req)
					if err != nil || page.Status != "ready" {
						t.Fatalf("page=%+v err=%v", page, err)
					}
					return page
				}
				old := read(HistoryWindowRequest{Anchor: "newest", Limit: 1})
				if old.TotalTurns != 3 || old.Messages[0].VisibleTurn != 3 {
					t.Fatalf("initial turns=%+v", old)
				}
				ids := []string{removeID}
				if withAnswer {
					ids = append(ids, "a"+removeID[1:])
				}
				payload, _ := json.Marshal(map[string]any{"messageIds": ids})
				if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "retract-user", Events: []Event{{Kind: "message/retract", Payload: payload}}}); err != nil {
					t.Fatal(err)
				}
				page := read(HistoryWindowRequest{Anchor: "newest"})
				if page.TotalTurns != 2 {
					t.Fatalf("current total turns=%d want=2", page.TotalTurns)
				}
				turn := 0
				for _, m := range page.Messages {
					if m.Role == string(provider.RoleUser) {
						turn++
					}
					if m.VisibleTurn != turn {
						t.Fatalf("message %s turn=%d want=%d", m.MessageID, m.VisibleTurn, turn)
					}
				}
				past := read(HistoryWindowRequest{Anchor: "cursor", Cursor: old.OlderCursor, Limit: 10})
				if past.TotalTurns != 3 || !idsEqual(windowIDs(t, past), []string{"u1", "a1", "u2", "a2", "u3"}) {
					t.Fatalf("old snapshot changed: %+v", past)
				}
				for i, m := range past.Messages {
					if m.VisibleTurn != i/2+1 {
						t.Fatalf("old snapshot %s turn=%d", m.MessageID, m.VisibleTurn)
					}
				}
				appendMessage("restore-user", "message/upsert", removeID, provider.RoleUser)
				restored := read(HistoryWindowRequest{Anchor: "newest"})
				last := restored.Messages[len(restored.Messages)-1]
				if restored.TotalTurns != 3 || last.MessageID != removeID || last.VisibleTurn != 3 || last.Version != 2 {
					t.Fatalf("restored user=%+v total=%d", last, restored.TotalTurns)
				}
			})
		}
	}
}

func TestMessageRetractCatalogAndRecentFollowVisibleHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	service, err := NewService("local", NewFilesystemPersistence(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseAll(context.Background()) })
	runtime, err := service.Create(t.Context(), CreateOptions{SessionID: "recent-retraction"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		user, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id, Role: provider.RoleUser, Content: id + " question"}})
		answer, _ := json.Marshal(map[string]any{"message": provider.Message{ID: id + "-answer", Role: provider.RoleAssistant, Content: id + " answer"}})
		if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: id, TurnID: id + "-turn", Events: []Event{{Kind: "turn/start", Payload: json.RawMessage(`{}`)}, {Kind: "message/complete", Payload: user}, {Kind: "message/complete", Payload: answer}, {Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Session().RecentSnapshot(); got.TotalTurns != 2 {
		t.Fatalf("initial recent turns=%d", got.TotalTurns)
	}
	if _, err := runtime.Session().Append(t.Context(), Batch{OperationID: "remove-first", Events: []Event{{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["first","first-answer"]}`)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	check := func(label string, recent RecentSnapshot, info SessionInfo) {
		t.Helper()
		if recent.TotalTurns != 1 {
			t.Errorf("%s recent total=%d want=1", label, recent.TotalTurns)
		}
		if info.Turns != 1 || info.Preview != "second question" {
			t.Errorf("%s catalog turns=%d preview=%q", label, info.Turns, info.Preview)
		}
		ids := make([]string, 0, len(recent.Entries))
		for _, m := range recent.Entries {
			ids = append(ids, m.MessageID)
			if m.VisibleTurn != 1 {
				t.Errorf("%s recent %s visible turn=%d", label, m.MessageID, m.VisibleTurn)
			}
		}
		if !idsEqual(ids, []string{"second", "second-answer"}) {
			t.Errorf("%s recent ids=%v", label, ids)
		}
	}
	info, err := service.Query().Stat(t.Context(), runtime.Ref())
	if err != nil {
		t.Fatal(err)
	}
	check("live", runtime.Session().RecentSnapshot(), info)
	targets := ForkTargets(runtime.Session().Snapshot().Projection)
	if len(targets.Targets) != 1 || targets.Targets[0].TurnID != "second-turn" || targets.Targets[0].TurnNumber != 1 || targets.Targets[0].MessageID != "second-answer" {
		t.Fatalf("withdrawn turn remained forkable or numbering changed: %+v", targets)
	}
	if seq, reason, err := ForkSequence(runtime.Session().Snapshot().Projection, "first-turn"); err != nil || seq != 0 || reason != ForkHistoryUnverifiable {
		t.Fatalf("withdrawn turn fork: seq=%d reason=%s err=%v", seq, reason, err)
	}
	if seq, reason, err := ForkSequence(runtime.Session().Snapshot().Projection, "second-turn"); err != nil || seq == 0 || reason != ForkAvailable {
		t.Fatalf("remaining turn fork: seq=%d reason=%s err=%v", seq, reason, err)
	}
	ref := runtime.Ref()
	if err := service.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	view, err := service.OpenSession(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	info, err = service.Query().Stat(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	check("cold", view.Recent, info)
	// Reopen a writer and remove every remaining input. Neither a cached
	// first-preview fallback nor old turn boundaries may resurrect the list
	// preview or leave a phantom visible turn.
	binding, err := service.EnsureExecution(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	live, ok := service.Runtime(ref)
	if !ok {
		t.Fatal("reopened runtime missing")
	}
	if _, err := live.Session().Append(t.Context(), Batch{OperationID: "remove-all-inputs", Events: []Event{{Kind: "message/retract", Payload: json.RawMessage(`{"messageIds":["second","second-answer"]}`)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Session().Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := binding.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkEmpty := func(label string, recent RecentSnapshot, info SessionInfo) {
		t.Helper()
		if recent.TotalTurns != 0 || len(recent.Entries) != 0 {
			t.Errorf("%s recent resurrected empty history: %+v", label, recent)
		}
		if info.Turns != 0 || info.Preview != "" {
			t.Errorf("%s catalog resurrected empty history: turns=%d preview=%q", label, info.Turns, info.Preview)
		}
	}
	info, err = service.Query().Stat(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	checkEmpty("empty-live", live.Session().RecentSnapshot(), info)
	checkNoForks := func(projection Projection) {
		t.Helper()
		if targets := ForkTargets(projection); len(targets.Targets) != 0 {
			t.Fatalf("empty history exposes fork targets: %+v", targets)
		}
		for _, id := range []string{"first-turn", "second-turn"} {
			if seq, reason, err := ForkSequence(projection, id); err != nil || seq != 0 || reason != ForkHistoryUnverifiable {
				t.Fatalf("empty history fork %s: seq=%d reason=%s err=%v", id, seq, reason, err)
			}
		}
	}
	checkNoForks(live.Session().Snapshot().Projection)
	if err := service.Close(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	for _, removeCache := range []bool{false, true} {
		label := "empty-cached"
		if removeCache {
			label = "empty-rebuilt"
			for _, dir := range []string{filepath.Join(root, ".query-cache", ref.SessionID), recoveryCacheDir(filepath.Join(root, ref.SessionID))} {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
		}
		// EnsureExecution deterministically completes checkpoint rebuild rather
		// than racing a cold reader's background preparation notification.
		binding, err := service.EnsureExecution(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		reopened, ok := service.Runtime(ref)
		if !ok {
			t.Fatal("reopened runtime missing")
		}
		info, err := service.Query().Stat(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		checkEmpty(label, reopened.Session().RecentSnapshot(), info)
		checkNoForks(reopened.Session().Snapshot().Projection)
		if err := binding.Release(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := service.Close(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
		cold, err := service.OpenSession(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		info, err = service.Query().Stat(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		checkEmpty(label+"-cold", cold.Recent, info)
	}
}
