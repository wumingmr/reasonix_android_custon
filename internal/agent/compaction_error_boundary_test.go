package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"reasonix/internal/attachment"
	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/extension/protocol"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestCompactionFinishPreservesUnrelatedErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &Agent{}
		ctx, finish := a.beginCompactionRun(t.Context())
		unrelated := errors.New("request preparation failed")
		wrapped := fmt.Errorf("save failed: %w", &compactionPersistenceError{context.DeadlineExceeded})
		time.Sleep(compactionBudget)
		synctest.Wait()
		for _, original := range []error{nil, unrelated, wrapped, context.Canceled, fmt.Errorf("recovery guidance: %w", errSummaryBudget)} {
			if got := finish(original); got != original { //nolint:errorlint // Assert exact identity; even an additional wrapper violates this boundary.
				t.Errorf("finish(%v) = %v; original error identity lost", original, got)
			}
		}
		if got := compactionError(ctx, context.DeadlineExceeded); !errors.Is(got, errSummaryBudget) {
			t.Fatalf("work deadline = %v", got)
		}
		blocked := fmt.Errorf("%w: %w", ErrCompactionRequired, context.DeadlineExceeded)
		if got := compactionError(ctx, blocked); !errors.Is(got, ErrCompactionRequired) || !errors.Is(got, errSummaryBudget) {
			t.Fatalf("blocked deadline lost its causes: %v", got)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	original := errors.New("unrelated error after stop")
	if got := compactionError(ctx, original); got != original { //nolint:errorlint // Assert exact identity, not merely an equivalent wrapped cause.
		t.Fatalf("cancellation replaced unrelated error: %v", got)
	}
	if got := compactionError(t.Context(), context.DeadlineExceeded); got != context.DeadlineExceeded { //nolint:errorlint // A foreign deadline must be returned unchanged.
		t.Fatalf("foreign deadline reclassified: %v", got)
	}
}

func TestRequestAndCompressValidationAreNotSummaryFailures(t *testing.T) {
	p := &mockProvider{name: "fixture"}
	s := NewSession("system")
	s.Add(provider.Message{Role: provider.RoleUser, ImageInputs: []attachment.ImageInput{{Kind: attachment.KindURL, URL: "https://example.invalid/image.png"}}})
	a := New(p, nil, s, Options{}, event.Discard)
	_, err := a.prepareSamplingRequest(t.Context())
	if err == nil || err.Error() != "image request resolver is unavailable" {
		t.Fatalf("image error = %v", err)
	}
	_, err = a.CompressContext(t.Context(), tool.CompressRequest{Direction: "invalid"})
	if err == nil || err.Error() != "compress: direction must be before or after" {
		t.Fatalf("compress validation = %v", err)
	}
	if len(p.requests) != 0 {
		t.Fatal("validation invoked the provider")
	}
}

func TestSafeCompactionTimeoutPreservesLaterExtensionBlock(t *testing.T) {
	for _, point := range []extension.InterceptorPoint{extension.PointContextPrepare, extension.PointProviderRequest} {
		t.Run(string(point), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := &slowSummaryProvider{}
				client := &fakeDispatchClient{interceptFn: func(protocol.InterceptEvent, json.RawMessage) (protocol.InterceptResult, error) {
					return blockWith("policy refused"), nil
				}}
				a := New(p, nil, foldableSessionOverForce(6), Options{ContextWindow: 5000, CompactRatio: .5,
					Extensions: newExtDispatcher(client, false, nil, point)}, event.Discard)
				started := time.Now()
				_, err := a.prepareSamplingRequest(t.Context())
				var summary *SummaryError
				want := extensionBlockedError(point, "policy refused").Error()
				if err == nil || err.Error() != want || errors.As(err, &summary) || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("extension error after safe timeout = %v", err)
				}
				if p.calls != 1 || time.Since(started) != compactionBudget || a.currentProjectionVersion() != 0 {
					t.Fatalf("calls=%d elapsed=%s projection=%d", p.calls, time.Since(started), a.currentProjectionVersion())
				}
			})
		})
	}
}

func TestPendingContextPersistenceIsNotSummaryFailure(t *testing.T) {
	saveErr := errors.New("retry flush failure")
	recorder := &modelContextRecorderStub{err: saveErr}
	s := NewSession("system")
	a := New(&mockProvider{name: "fixture"}, nil, s, Options{SessionCheckpointer: recorder}, event.Discard)
	a.sess.pendingModelContextCommit = &SessionModelContextCommit{OperationID: "pending-commit"}
	_, err := a.prepareSamplingRequest(t.Context())
	var summary *SummaryError
	if !errors.Is(err, saveErr) || errors.As(err, &summary) {
		t.Fatalf("pending persistence error = %v", err)
	}
	if a.sess.pendingModelContextCommit == nil {
		t.Fatal("failed persistence discarded the pending commit")
	}
}

func TestSummaryRequestClassifiesItsOwnFailures(t *testing.T) {
	providerErr := errors.New("summary provider failed")
	for _, tc := range []struct {
		name  string
		prov  *fakeProvider
		code  string
		cause error
	}{
		{"provider", &fakeProvider{streamErr: providerErr}, "summary_provider_error", providerErr},
		{"empty", &fakeProvider{}, "summary_empty", errSummaryEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New(tc.prov, nil, NewSession("system"), Options{}, event.Discard)
			_, _, err := a.summarize(t.Context(), []provider.Message{{Role: provider.RoleUser, Content: "summarize this"}}, "")
			var summary *SummaryError
			if !errors.As(err, &summary) || summary.Code != tc.code || !errors.Is(err, tc.cause) {
				t.Fatalf("summary error = %v, want %s wrapping %v", err, tc.code, tc.cause)
			}
		})
	}
}

func TestCompactionTimeoutKeepsOutermostRecoveryGuidance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &slowSummaryProvider{}
		a := New(p, nil, foldableSessionOverForce(12), Options{ContextWindow: 5000, CompactRatio: .5}, event.Discard)
		_, err := a.prepareSamplingRequest(t.Context())
		if !errors.Is(err, ErrCompactionRequired) || !errors.Is(err, errSummaryBudget) || !strings.HasPrefix(err.Error(), i18n.M.ContextLimitRecovery+": ") {
			t.Fatalf("hard-limit timeout lost recovery guidance: %v", err)
		}
		if p.calls != 1 {
			t.Fatalf("summary calls=%d", p.calls)
		}
	})
}
