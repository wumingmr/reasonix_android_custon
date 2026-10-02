package draftstate

import (
	"errors"
	"testing"
)

func TestDiscardAndSubmissionOwnershipOrder(t *testing.T) {
	for _, submitFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "discard-first", true: "submit-first"}[submitFirst], func(t *testing.T) {
			s := testStore(t)
			ctx := t.Context()
			draft, _, err := s.Open(ctx, "workspace", "global", "", "draft", `{}`)
			if err != nil {
				t.Fatal(err)
			}
			submit := func() error {
				_, _, err := s.BeginOperation(ctx, Operation{ID: "submit", DraftID: draft.ID, WorkspaceID: draft.WorkspaceID, DraftRevision: draft.Revision, SessionID: "session", SubmissionID: "message", Fingerprint: "input", RequestJSON: `{}`})
				return err
			}
			discard := func() error { return s.Discard(ctx, draft.ID, draft.Revision) }
			first, second := discard, submit
			if submitFirst {
				first, second = submit, discard
			}
			// A barrier fixes transaction ownership before the competing request
			// proceeds; scheduling or repeated test runs cannot choose the winner.
			claimed := make(chan error, 1)
			go func() { claimed <- first() }()
			if err := <-claimed; err != nil {
				t.Fatal(err)
			}
			if err := second(); err == nil {
				t.Fatal("both discard and first submission acquired ownership")
			}
			if submitFirst {
				for _, phase := range []string{"dispatching", "dispatch_unknown", "cancel_requested"} {
					if _, err := s.SetOperationPhase(ctx, "submit", phase, ""); err != nil {
						t.Fatal(err)
					}
					if err := discard(); !errors.Is(err, ErrOperationConflict) {
						t.Fatalf("%s must retain ownership: %v", phase, err)
					}
				}
			}
		})
	}
}
