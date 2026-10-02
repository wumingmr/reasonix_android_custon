package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/session"
)

type workspaceInfoReaderFunc func(context.Context, session.SessionRef) (session.SessionInfo, error)

func (f workspaceInfoReaderFunc) Stat(ctx context.Context, ref session.SessionRef) (session.SessionInfo, error) {
	return f(ctx, ref)
}

func TestProjectTopicSnapshotAllowsContinuousSessionWrites(t *testing.T) {
	for _, pinnedOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("pinned=%v", pinnedOnly), func(t *testing.T) {
			app, root, refs := canonicalOrganizationFixture(t, "a", "b")
			service := app.desktopSessionService("")
			if pinnedOnly {
				if err := app.workspaceRegistry().UpdatePresentation(t.Context(), []string{"a", "b"}, nil, &pinnedOnly); err != nil {
					t.Fatal(err)
				}
			}
			reads := 0
			observed := map[string]string{}
			reader := workspaceInfoReaderFunc(func(ctx context.Context, ref session.SessionRef) (session.SessionInfo, error) {
				info, err := service.Query().Stat(ctx, ref)
				if err != nil {
					return info, err
				}
				reads++
				observed[ref.SessionID] = info.Title
				// Force a real owner write after every metadata observation. A
				// double collect can never settle, regardless of retry count.
				if err := service.SetTitle(ctx, ref, fmt.Sprintf("Live write %d", reads)); err != nil {
					t.Fatal(err)
				}
				return info, nil
			})
			req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1, pinnedOnly: pinnedOnly}
			for range 3 {
				page, err := app.readProjectTopicPage(req, reader)
				if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
					t.Fatalf("live first page = %+v, %v", page, err)
				}
				row := page.Items[0]
				if title := observed[row.Session.SessionID]; title != "" && row.Label != title {
					t.Fatalf("page mixed captured and later metadata: %+v, observed %q", row, title)
				}
				continuation := req
				continuation.Cursor = page.NextCursor
				second, err := app.ListProjectTopics(continuation)
				if err != nil || len(second.Items) != 1 || second.Items[0].Key == row.Key || second.SnapshotID != page.SnapshotID {
					t.Fatalf("write disturbed frozen continuation: %+v %v", second, err)
				}
			}
			if reads != 3*len(refs) {
				t.Fatalf("metadata reads = %d, want one observation per session per page", reads)
			}
			fresh, err := app.ListProjectTopics(req)
			if err != nil || len(fresh.Items) != 1 {
				t.Fatalf("refresh = %+v, %v", fresh, err)
			}
			info, err := service.Query().Stat(t.Context(), *fresh.Items[0].Session)
			if err != nil || fresh.Items[0].Label != info.Title {
				t.Fatalf("refresh did not observe latest title: %+v, %+v, %v", fresh, info, err)
			}
		})
	}
}

func TestProjectTopicCursorIgnoresUnrelatedWorkspaceMutation(t *testing.T) {
	app, root, _ := canonicalOrganizationFixture(t, "a", "b")
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1}
	first, err := app.ListProjectTopics(req)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	if _, err := app.ensureDesktopWorkspace(t.Context(), "project", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	second, err := app.ListProjectTopics(req)
	if err != nil || len(second.Items) != 1 || second.Items[0].Key == first.Items[0].Key {
		t.Fatalf("unrelated workspace invalidated pagination: %+v, %v", second, err)
	}
}

func TestProjectTopicCursorFreezesNewResult(t *testing.T) {
	app, root, refs := canonicalOrganizationFixture(t, "a", "b")
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1}
	first, err := app.ListProjectTopics(req)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	runtime, ok := app.desktopSessionService("").Runtime(refs["a"])
	if !ok {
		t.Fatal("fixture runtime missing")
	}
	payload, err := json.Marshal(map[string]any{"message": provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "Build finished"}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := runtime.Session().Append(t.Context(), session.Batch{OperationID: "build", TurnID: "build", Events: []session.Event{
		{Kind: "turn/start"},
		{Kind: "message/complete", Payload: payload},
		{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	second, err := app.ListProjectTopics(req)
	if err != nil || second.SnapshotID != first.SnapshotID || len(second.Items) != 1 || second.Items[0].Key == first.Items[0].Key {
		t.Fatalf("new result disturbed snapshot: %+v %v", second, err)
	}
	req.Cursor, req.Limit = "", 10
	fresh, err := app.ListProjectTopics(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range fresh.Items {
		if row.Session.SessionID == "a" && row.ResultSequence == commit.FirstSequence+uint64(commit.EventCount)-1 {
			return
		}
	}
	t.Fatalf("refresh omitted the new result: %+v", fresh)
}

func TestProjectTopicCursorIgnoresUnrelatedCatalogScan(t *testing.T) {
	app, root, _ := canonicalOrganizationFixture(t, "a", "b")
	otherRoot := t.TempDir()
	installSessionCatalogForTest(t, app, otherRoot, "project", otherRoot)
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1}
	first, err := app.ListProjectTopics(req)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	before := app.currentSessionCatalogStatus().Revision
	writeTopicSession(t, otherRoot, "other.jsonl", "other-topic", "Other project", otherRoot)
	reconcileSessionCatalogForTest(t, app, otherRoot, "project", otherRoot)
	if app.currentSessionCatalogStatus().Revision <= before {
		t.Fatal("fixture did not advance catalog revision")
	}
	req.Cursor = first.NextCursor
	second, err := app.ListProjectTopics(req)
	if err != nil || len(second.Items) != 1 || second.Items[0].Key == first.Items[0].Key {
		t.Fatalf("unrelated catalog scan invalidated pagination: %+v, %v", second, err)
	}
}

func TestProjectTopicSnapshotPreservesCapturedOrganization(t *testing.T) {
	app, root, refs := canonicalOrganizationFixture(t, "a", "b", "c")
	keys := []string{}
	for _, id := range []string{"a", "b", "c"} {
		ref := refs[id]
		keys = append(keys, projectNodeSessionKey(ProjectNode{Session: &ref}))
	}
	if err := app.ReorderSessions("project", root, keys); err != nil {
		t.Fatal(err)
	}
	changed := false
	reader := workspaceInfoReaderFunc(func(ctx context.Context, ref session.SessionRef) (session.SessionInfo, error) {
		if !changed {
			changed = true
			if err := app.ReorderSessions("project", root, []string{keys[2], keys[0], keys[1]}); err != nil {
				t.Fatal(err)
			}
		}
		return app.desktopSessionService("").Query().Stat(ctx, ref)
	})
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1}
	first, err := app.readProjectTopicPage(req, reader)
	if err != nil || len(first.Items) != 1 || first.Items[0].Session.SessionID != "a" {
		t.Fatalf("captured order = %+v, %v", first, err)
	}
	req.Cursor = first.NextCursor
	second, err := app.ListProjectTopics(req)
	if err != nil || len(second.Items) != 1 || second.Items[0].Session.SessionID != "b" {
		t.Fatalf("reorder disturbed captured order: %+v %v", second, err)
	}
	req.Cursor = ""
	fresh, err := app.ListProjectTopics(req)
	if err != nil || len(fresh.Items) != 1 || fresh.Items[0].Session.SessionID != "c" {
		t.Fatalf("refreshed order = %+v, %v", fresh, err)
	}
}

func TestProjectTopicSnapshotRejectsArchivedMember(t *testing.T) {
	app, root, refs := canonicalOrganizationFixture(t, "a", "b", "c")
	req := ProjectTopicPageRequest{Scope: "project", WorkspaceRoot: root, Limit: 1}
	page, err := app.ListProjectTopics(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.workspaceRegistry().ArchiveSession(t.Context(), refs["b"].SessionID); err != nil {
		t.Fatal(err)
	}
	req.Cursor = page.NextCursor
	if _, err := app.ListProjectTopics(req); err == nil {
		t.Fatal("archived member accepted by frozen read")
	}
	req.Cursor = ""
	req.Limit = 10
	fresh, err := app.ListProjectTopics(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range fresh.Items {
		if row.Session != nil && row.Session.SessionID == "b" {
			t.Fatal("archived row resurrected")
		}
	}
}
