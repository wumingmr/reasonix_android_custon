package draftstate

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// Structural schema fixtures complement tagged v4 databases: no public tag in
// 1.38.3–11 emitted schemas 1–3, but the existing migration contract accepts them.
func TestEveryPreviousDraftSchemaPreservesWALAndOpaqueRecords(t *testing.T) {
	for version := 1; version <= 3; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "drafts.sqlite")
			initial := New(path)
			draft, _, err := initial.Open(t.Context(), "workspace", "project", "/synthetic/project", "draft", `{"model":"frozen","futureSetting":{"keep":true}}`)
			if err != nil {
				t.Fatal(err)
			}
			const content = `{"text":"unsent","attachments":[{"path":"missing-original-file"}],"futureContent":[1,2,3]}`
			draft, err = initial.Save(t.Context(), draft.ID, draft.Revision, content, draft.SettingsJSON, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := initial.Save(t.Context(), draft.ID, draft.Revision-1, `{"text":"conflict copy"}`, draft.SettingsJSON, false); err == nil {
				t.Fatal("fixture must contain a conflict")
			}
			const request = `{"snapshotVersion":3,"settings":{"model":"frozen"},"futureRequest":true}`
			_, _, err = initial.BeginOperation(t.Context(), Operation{ID: "operation", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session", TopicID: "topic", SubmissionID: "submission", Fingerprint: "fingerprint", RequestJSON: request})
			if err != nil {
				t.Fatal(err)
			}
			if err := initial.SetRestore(t.Context(), draft.ID); err != nil {
				t.Fatal(err)
			}
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			statements := []string{`PRAGMA wal_autocheckpoint=0`, `DROP INDEX operations_by_request`, `ALTER TABLE operations DROP COLUMN request_id`, `ALTER TABLE operations DROP COLUMN source_digest`, `ALTER TABLE operations DROP COLUMN operation_revision`, `ALTER TABLE operations DROP COLUMN execution_json`}
			if version == 1 {
				statements = append(statements, `ALTER TABLE operations DROP COLUMN topic_id`)
			}
			statements = append(statements, fmt.Sprintf("PRAGMA user_version=%d", version), `CREATE TABLE future_data(value TEXT)`, `INSERT INTO future_data VALUES('committed only in WAL')`)
			for _, statement := range statements {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				upgraded := New(path)
				got, err := upgraded.Restore(t.Context())
				if err != nil || got.ID != draft.ID || got.ContentJSON != content || got.SettingsJSON != draft.SettingsJSON || got.Revision != draft.Revision {
					t.Fatalf("draft changed across upgrade: %+v %v", got, err)
				}
				op, err := upgraded.Operation(t.Context(), "operation")
				if err != nil || op.SessionID != "session" || op.SubmissionID != "submission" || op.RequestJSON != request || op.Phase != "reserved" {
					t.Fatalf("operation changed across upgrade: %+v %v", op, err)
				}
				if version > 1 && op.TopicID != "topic" {
					t.Fatal("existing topic identity changed")
				}
				if err := upgraded.Close(); err != nil {
					t.Fatal(err)
				}
			}
			backup, err := sql.Open("sqlite", path+".pre-v4.sqlite")
			if err != nil {
				t.Fatal(err)
			}
			defer backup.Close()
			var backupVersion int
			if err := backup.QueryRow(`PRAGMA user_version`).Scan(&backupVersion); err != nil || backupVersion != version {
				t.Fatalf("backup version=%d %v", backupVersion, err)
			}
			for _, reader := range []*sql.DB{db, backup} {
				var marker, conflict string
				if err := reader.QueryRow(`SELECT value FROM future_data`).Scan(&marker); err != nil || marker != "committed only in WAL" {
					t.Fatalf("opaque WAL data lost: %q %v", marker, err)
				}
				if err := reader.QueryRow(`SELECT content_json FROM conflicts`).Scan(&conflict); err != nil || conflict != `{"text":"conflict copy"}` {
					t.Fatalf("conflict copy lost: %q %v", conflict, err)
				}
			}
		})
	}
}
