package draftstate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestWorkerLeaseIndependentProcess(t *testing.T) {
	if path := os.Getenv("REASONIX_DRAFT_LOCK_FIXTURE"); path != "" {
		s := New(path)
		defer s.Close()
		if release, err := s.WorkerLease("same-session"); err == nil {
			release()
			t.Fatal("child acquired parent's worker lease")
		}
		return
	}
	s := testStore(t)
	release, err := s.WorkerLease("same-session")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	child := exec.Command(os.Args[0], "-test.run=^TestWorkerLeaseIndependentProcess$")
	child.Env = append(os.Environ(), "REASONIX_DRAFT_LOCK_FIXTURE="+s.path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
}

func TestRequestIdentitySurvivesConversionAndRejectsChangedSnapshot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d, _, err := s.Open(ctx, "workspace", "global", "", "draft", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := SnapshotDigest(d.ContentJSON, d.SettingsJSON)
	if err != nil {
		t.Fatal(err)
	}
	request := Operation{ID: "operation", RequestID: "request", SourceDigest: digest, DraftID: d.ID, DraftRevision: d.Revision, WorkspaceID: d.WorkspaceID, SessionID: "session", SubmissionID: "submission", Fingerprint: "original", RequestJSON: `{}`}
	op, created, err := s.BeginOperation(ctx, request)
	if err != nil || !created {
		t.Fatalf("begin: %+v %v %v", op, created, err)
	}
	if _, err = s.SetOperationPhase(ctx, op.ID, "dispatching", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptAndConvert(ctx, d.ID, op.ID); err != nil {
		t.Fatal(err)
	}
	request.ID = "must-not-create"
	again, created, err := s.BeginOperation(ctx, request)
	if err != nil || created || again.ID != op.ID {
		t.Fatalf("retry: %+v %v %v", again, created, err)
	}
	request.Fingerprint = "changed"
	if _, _, err = s.BeginOperation(ctx, request); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed request: %v", err)
	}
}

func TestSnapshotDigestTreatsInheritedModelAsLiveCompatibilityMirror(t *testing.T) {
	inheritedA, err := SnapshotDigest(`{}`, `{"model":"fixture/a","modelSource":"default"}`)
	if err != nil {
		t.Fatal(err)
	}
	inheritedB, err := SnapshotDigest(`{}`, `{"model":"fixture/b","modelSource":"default"}`)
	if err != nil {
		t.Fatal(err)
	}
	if inheritedA != inheritedB {
		t.Fatalf("inherited model mirror changed digest: %s != %s", inheritedA, inheritedB)
	}
	explicitA, err := SnapshotDigest(`{}`, `{"model":"fixture/a","modelSource":"explicit"}`)
	if err != nil {
		t.Fatal(err)
	}
	explicitB, err := SnapshotDigest(`{}`, `{"model":"fixture/b","modelSource":"explicit"}`)
	if err != nil {
		t.Fatal(err)
	}
	if explicitA == explicitB {
		t.Fatal("explicit model was omitted from the draft digest")
	}
}

func TestOperationResumeCASAndWorkerExclusion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d, _, err := s.Open(ctx, "workspace", "global", "", "draft", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.BeginOperation(ctx, Operation{ID: "op", DraftID: d.ID, DraftRevision: d.Revision, WorkspaceID: d.WorkspaceID, SessionID: "session", SubmissionID: "submission", Fingerprint: "f", RequestJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	op, err = s.SetOperationPhase(ctx, op.ID, "resume_required", "")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.ResumeOperation(ctx, op.ID, op.Revision)
	if err != nil || resumed.Revision <= op.Revision {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	if _, err = s.ResumeOperation(ctx, op.ID, op.Revision); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("stale resume: %v", err)
	}
	release, err := s.WorkerLease(op.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other := New(s.path)
	defer other.Close()
	if unlock, err := other.WorkerLease(op.SessionID); err == nil {
		unlock()
		t.Fatal("second worker acquired live execution ownership")
	}
}
