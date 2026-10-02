package control

import (
	"context"
	"fmt"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestAuditTitleAuthenticationRejectionStopsFurtherRequests(t *testing.T) {
	for _, explicitModel := range []bool{false, true} {
		t.Run(fmt.Sprint(explicitModel), func(t *testing.T) {
			prov := &sessionTitleProviderStub{err: &provider.AuthError{Provider: "test", Status: 401, HasKey: true}}
			ctrl := sessionTitleTestController(t, prov, event.Discard)
			for range 2 {
				var err error
				if explicitModel {
					_, err = ctrl.GenerateSessionTitleForModel(context.Background(), "test/title-model", "test title")
				} else {
					_, err = ctrl.GenerateSessionTitle(context.Background(), "test title")
				}
				if err == nil {
					t.Fatal("expected provider rejection")
				}
			}
			if len(prov.requests) != 1 {
				t.Fatalf("authentication rejection allowed %d title requests without explicit retry", len(prov.requests))
			}
		})
	}
}

func TestAuxiliaryAuthenticationFailureIsScopedToConnectionAndModel(t *testing.T) {
	gate := newAuthenticationGate(AuthenticationState{Status: AuthenticationReady}, "main/chat")
	gate.recordFailure(&provider.AuthError{Provider: "other", Status: 401}, "other/title")
	if err := gate.admissionError(); err != nil {
		t.Fatalf("other connection blocked main chat: %v", err)
	}
	if err := gate.admissionErrorForModel("other/title"); err == nil {
		t.Fatal("failed title connection was not blocked")
	}
	gate.recordFailure(&provider.AuthError{Provider: "main", Status: 403}, "main/title")
	if err := gate.admissionError(); err != nil {
		t.Fatalf("title-only 403 blocked chat: %v", err)
	}
	if err := gate.admissionErrorForModel("main/title"); err == nil {
		t.Fatal("failed title model was not blocked")
	}
	gate.recordFailure(&provider.AuthError{Provider: "main", Status: 401}, "main/title")
	if err := gate.admissionError(); err == nil {
		t.Fatal("same-connection 401 did not block chat")
	}
}
