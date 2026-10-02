package draftstate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store := New(filepath.Join(t.TempDir(), "drafts.sqlite"))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestDraftPersistsAndRestoresAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "drafts.sqlite")
	first := New(path)
	draft, created, err := first.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{"model":"a"}`)
	if err != nil || !created {
		t.Fatalf("Open() = created %v, err %v", created, err)
	}
	saved, err := first.Save(ctx, draft.ID, draft.Revision, `{"text":"hello"}`, `{"model":"b"}`, false)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := first.SetRestore(ctx, draft.ID); err != nil {
		t.Fatalf("SetRestore() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second := New(path)
	t.Cleanup(func() { _ = second.Close() })
	restored, err := second.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if restored.ID != draft.ID || restored.Revision != saved.Revision || restored.ContentJSON != `{"text":"hello"}` {
		t.Fatalf("restored = %+v, want draft %q revision %d", restored, draft.ID, saved.Revision)
	}
}

func TestOpenReusesOneActiveDraftPerWorkspaceAcrossStores(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "drafts.sqlite")
	stores := []*Store{New(path), New(path)}
	for _, store := range stores {
		defer store.Close()
	}
	var wg sync.WaitGroup
	results := make(chan Draft, len(stores))
	errs := make(chan error, len(stores))
	for index, store := range stores {
		wg.Add(1)
		go func(index int, store *Store) {
			defer wg.Done()
			draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-"+string(rune('a'+index)), `{}`)
			if err != nil {
				errs <- err
				return
			}
			results <- draft
		}(index, store)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Open() error = %v", err)
		}
	}
	var id string
	for draft := range results {
		if id == "" {
			id = draft.ID
		}
		if draft.ID != id {
			t.Fatalf("concurrent drafts = %q and %q", id, draft.ID)
		}
	}
}

func TestSaveConflictPreservesConflictCopy(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(ctx, draft.ID, draft.Revision, `{"text":"saved"}`, `{}`, false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Save(ctx, draft.ID, draft.Revision, `{"text":"local"}`, `{}`, false)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Save() error = %v, want conflict", err)
	}
	if current.Revision != saved.Revision || current.ContentJSON != saved.ContentJSON {
		t.Fatalf("conflict current = %+v", current)
	}
	var count int
	if err := store.withDB(ctx, func(db *sql.DB) error {
		return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conflicts WHERE draft_id=? AND content_json=?`, draft.ID, `{"text":"local"}`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conflict copies = %d, want 1", count)
	}
}

func TestForceSaveStillUsesRevisionCAS(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(ctx, draft.ID, draft.Revision, `{"text":"other window"}`, `{}`, false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Save(ctx, draft.ID, draft.Revision, `{"text":"local"}`, `{}`, true)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("force Save() error = %v, want conflict", err)
	}
	if current.Revision != saved.Revision || current.ContentJSON != saved.ContentJSON {
		t.Fatalf("force save overwrote unseen revision: %+v", current)
	}
}

func TestOpenDoesNotChangeRestoreTarget(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	first, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRestore(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(ctx, "workspace-b", "project", "/workspace/b", "draft-b", `{}`); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Restore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != first.ID {
		t.Fatalf("restore target = %q, want %q", restored.ID, first.ID)
	}
}

func TestUnknownSchemaVersionRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drafts.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store := New(path)
	_, _, err = store.Open(context.Background(), "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Open() error = %v, want unsupported version", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("unknown database header or data was modified")
	}
}

func TestBeginOperationIsIdempotentAndRejectsDifferentPayload(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	base := Operation{ID: "operation-a", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session-a", TopicID: "topic-a", SubmissionID: "submission-a", Fingerprint: "same", RequestJSON: `{"input":"hello"}`}
	first, created, err := store.BeginOperation(ctx, base)
	if err != nil || !created {
		t.Fatalf("BeginOperation() = created %v, err %v", created, err)
	}
	retry := base
	retry.ID, retry.SessionID, retry.TopicID, retry.SubmissionID = "operation-b", "session-b", "topic-b", "submission-b"
	second, created, err := store.BeginOperation(ctx, retry)
	if err != nil || created {
		t.Fatalf("retry = created %v, err %v", created, err)
	}
	if second.ID != first.ID || second.SessionID != first.SessionID || second.TopicID != first.TopicID || second.SubmissionID != first.SubmissionID {
		t.Fatalf("retry operation = %+v, want %+v", second, first)
	}
	retry.Fingerprint = "different"
	if _, _, err := store.BeginOperation(ctx, retry); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("different payload error = %v", err)
	}
}

func TestConvertedDraftRejectsStaleSave(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := store.BeginOperation(ctx, Operation{ID: "operation-a", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session-a", SubmissionID: "submission-a", Fingerprint: "same", RequestJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetOperationPhase(ctx, op.ID, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Convert(ctx, draft.ID, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, draft.ID, draft.Revision, `{"text":"late"}`, `{}`, false); !errors.Is(err, ErrConverted) {
		t.Fatalf("stale Save() error = %v, want converted", err)
	}
	active, err := store.ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active drafts = %+v, want none", active)
	}
}

func TestTerminalOperationReusesReservedSession(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.BeginOperation(ctx, Operation{ID: "operation-a", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session-a", TopicID: "topic-a", SubmissionID: "submission-a", Fingerprint: "first", RequestJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.TransitionOperationPhase(ctx, first.ID, []string{"reserved"}, "cancelled", ""); err != nil || !claimed {
		t.Fatalf("cancel = claimed %v, err %v", claimed, err)
	}
	second, created, err := store.BeginOperation(ctx, Operation{ID: "operation-b", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session-b", TopicID: "topic-b", SubmissionID: "submission-b", Fingerprint: "second", RequestJSON: `{}`})
	if err != nil || !created {
		t.Fatalf("second = created %v, err %v", created, err)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("second session = %q, want reserved %q", second.SessionID, first.SessionID)
	}
	if second.TopicID != first.TopicID {
		t.Fatalf("second topic = %q, want reserved %q", second.TopicID, first.TopicID)
	}
}

func TestVersionOneDatabaseMigratesOperationTopic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drafts.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE operations (id TEXT PRIMARY KEY, draft_id TEXT NOT NULL, workspace_id TEXT NOT NULL, draft_revision INTEGER NOT NULL, session_id TEXT NOT NULL, submission_id TEXT NOT NULL, fingerprint TEXT NOT NULL, request_json TEXT NOT NULL, phase TEXT NOT NULL, error TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO operations VALUES('op','draft','workspace',1,'session','submission','fingerprint','{}','reserved','',1,1)`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := New(path)
	t.Cleanup(func() { _ = store.Close() })
	op, err := store.Operation(context.Background(), "op")
	if err != nil {
		t.Fatal(err)
	}
	if op.TopicID != "" {
		t.Fatalf("migrated topic = %q, want empty reservation", op.TopicID)
	}
	var version int
	if err := store.withDB(context.Background(), func(db *sql.DB) error { return db.QueryRow(`PRAGMA user_version`).Scan(&version) }); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
	}
	assigned, err := store.EnsureOperationTopic(context.Background(), op.ID, "topic")
	if err != nil || assigned.TopicID != "topic" {
		t.Fatalf("assigned = %+v, err %v", assigned, err)
	}
}

func TestDispatchClaimWinsOrCancelWinsButNeverBoth(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := store.BeginOperation(ctx, Operation{ID: "operation-a", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session-a", SubmissionID: "submission-a", Fingerprint: "same", RequestJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimOperationPhase(ctx, op.ID, []string{"reserved"}, "starting"); err != nil || !claimed {
		t.Fatalf("start = claimed %v, err %v", claimed, err)
	}

	start := make(chan struct{})
	type outcome struct {
		phase   string
		claimed bool
		err     error
	}
	results := make(chan outcome, 2)
	for _, transition := range []struct {
		to   string
		from []string
	}{
		{to: "dispatching", from: []string{"starting"}},
		{to: "cancelled", from: []string{"reserved", "starting", "runtime_failed", "resume_required"}},
	} {
		go func() {
			<-start
			result, claimed, err := store.TransitionOperationPhase(ctx, op.ID, transition.from, transition.to, "")
			results <- outcome{phase: result.Phase, claimed: claimed, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("transition errors = %v, %v", first.err, second.err)
	}
	if first.claimed == second.claimed {
		t.Fatalf("claims = %v, %v; want exactly one", first, second)
	}
	if first.phase != second.phase || (first.phase != "dispatching" && first.phase != "cancelled") {
		t.Fatalf("final phases = %q, %q", first.phase, second.phase)
	}
}

func TestAcceptAndConvertIsAtomicAndBlocksStaleSave(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := store.BeginOperation(ctx, Operation{ID: "operation-a", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session-a", SubmissionID: "submission-a", Fingerprint: "same", RequestJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimOperationPhase(ctx, op.ID, []string{"reserved"}, "starting"); err != nil || !claimed {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimOperationPhase(ctx, op.ID, []string{"starting"}, "dispatching"); err != nil || !claimed {
		t.Fatal(err)
	}
	accepted, err := store.AcceptAndConvert(ctx, draft.ID, op.ID)
	if err != nil || accepted.Phase != "accepted" {
		t.Fatalf("accept = %+v, err %v", accepted, err)
	}
	if _, err := store.Save(ctx, draft.ID, draft.Revision, `{"text":"late"}`, `{}`, false); !errors.Is(err, ErrConverted) {
		t.Fatalf("stale save error = %v", err)
	}
}

func TestAcceptAndConvertRejectsNonActiveDraftWithoutAcceptingOperation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	draft, _, err := store.Open(ctx, "workspace-a", "project", "/workspace/a", "draft-a", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := store.BeginOperation(ctx, Operation{ID: "operation-a", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session-a", SubmissionID: "submission-a", Fingerprint: "same", RequestJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimOperationPhase(ctx, op.ID, []string{"reserved"}, "dispatching"); err != nil || !claimed {
		t.Fatalf("dispatch claim = %v, err %v", claimed, err)
	}
	if err := store.withDB(ctx, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx, `UPDATE drafts SET status='discarded' WHERE id=?`, draft.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptAndConvert(ctx, draft.ID, op.ID); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("AcceptAndConvert() error = %v, want operation conflict", err)
	}
	unchanged, err := store.Operation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Phase != "dispatching" {
		t.Fatalf("operation phase = %q, want dispatching", unchanged.Phase)
	}
}
