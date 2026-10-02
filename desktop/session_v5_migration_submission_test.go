package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// Submission receipts introduced by #10392 are host metadata. Migration must
// preserve the event bytes, keep receipt scope tied to the session identity,
// and keep both title/history consumers and model messages compatible.
func TestDesktopV5UpgradeSubmissionIdentity(t *testing.T) {
	for _, collision := range []bool{false, true} {
		name := "same-identity"
		if collision {
			name = "independent-conflict-identity"
		}
		t.Run(name, func(t *testing.T) {
			isolateDesktopUserDirs(t)
			root := config.SessionStoreDir()
			id := "accepted-source"
			writer, err := session.CreateStore(filepath.Join(root, id), id)
			if err != nil {
				t.Fatal(err)
			}
			receipt := session.SubmissionReceipt{SessionID: id, SubmissionID: "send", Fingerprint: "request-fingerprint", TurnID: "turn", MessageID: "user"}
			receiptBody, _ := json.Marshal(receipt)
			message := provider.Message{ID: "user", Role: provider.RoleUser, Content: "question for the title"}
			messageBody, _ := json.Marshal(map[string]any{"message": message})
			if _, err := writer.Append(t.Context(), session.Batch{OperationID: "accept", TurnID: "turn", Events: []session.Event{
				{Kind: "turn/start"},
				{Kind: "submission/accepted", Optional: true, Payload: receiptBody},
				{Kind: "message/complete", Payload: messageBody},
				{Kind: "turn/end", Payload: json.RawMessage(`{"status":"completed"}`)},
			}}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := migrationSourceSnapshot(t, canonicalMigrationSourceFiles(root, id))
			app := NewApp()
			t.Cleanup(app.closeSessionServices)
			if collision {
				other, err := app.desktopSessionService("").Create(t.Context(), session.CreateOptions{SessionID: id})
				if err != nil {
					t.Fatal(err)
				}
				appendMigrationTestMessage(t, app.desktopSessionService(""), other.Ref(), "unrelated existing work")
			}
			if err := app.migrateDesktopSessionsV5(t.Context()); err != nil {
				t.Fatal(err)
			}
			ledger, err := readDesktopMigrationLedger()
			if err != nil {
				t.Fatal(err)
			}
			targetID := ledger.Records[desktopCanonicalMigrationKey(root, id)].TargetSessionID
			if (targetID != id) != collision {
				t.Fatalf("unexpected identity: %q", targetID)
			}
			ref := session.SessionRef{HostID: localDesktopHostID, SessionID: targetID}
			query := app.desktopSessionService("").Query()
			// This uses #10389's new canonical title reader and waits for the
			// same history index that #10392 annotates with submission IDs.
			titles, err := query.TitleMessages(t.Context(), ref, 3)
			if err != nil || len(titles) != 1 || titles[0].Content != message.Content {
				t.Fatalf("migrated title input: %+v, %v", titles, err)
			}
			page, err := query.ReadHistoryWindow(t.Context(), ref, session.HistoryWindowRequest{Anchor: "newest", Limit: 10})
			if err != nil || page.Status != "ready" || len(page.Messages) != 1 {
				t.Fatalf("migrated history: %+v, %v", page, err)
			}
			wantSubmission := "send"
			if collision {
				wantSubmission = ""
			}
			if page.Messages[0].SubmissionID != wantSubmission {
				t.Fatalf("submission scope: %+v", page.Messages[0])
			}
			snapshot, err := query.Snapshot(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			receipts, _ := json.Marshal(snapshot.Projection.Submissions)
			if !bytes.Contains(receipts, receiptBody) {
				t.Fatalf("source receipt lost: %s", receipts)
			}
			model, _ := json.Marshal(snapshot.Projection.Messages)
			if bytes.Contains(model, []byte("submissionId")) || bytes.Contains(model, []byte("fingerprint")) {
				t.Fatal("submission metadata entered model messages")
			}
			assertMigrationSourceSnapshot(t, before)
			assertLineageRestart(t, app, 1)
		})
	}
}
