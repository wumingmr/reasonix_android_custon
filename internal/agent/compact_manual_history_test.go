package agent

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/sessioncontext"
)

func TestManualCompactFindsHistoryBehindFixedContext(t *testing.T) {
	for _, window := range []int{0, 128_000} {
		name := "configured-window"
		if window == 0 {
			name = "automatic-compaction-disabled"
		}
		t.Run(name, func(t *testing.T) {
			prov := &fakeProvider{reply: "- Completed the requested work."}
			sess := NewSession(strings.Repeat("stable system instructions ", 500))
			contextMessage := HostGeneratedUserMessage(sessioncontext.Build(sessioncontext.Sections{Workspace: strings.Repeat("workspace instructions ", 500)}).Content)
			sess.Add(contextMessage)
			sess.Add(provider.Message{Role: provider.RoleUser, Content: "Implement the requested change."})
			sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("Completed implementation and verification. ", 50)})
			a := New(prov, nil, sess, Options{ContextWindow: window}, event.Discard)
			canonical := append([]provider.Message(nil), sess.Messages...)
			if err := a.PrepareContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(prov.got) != 0 {
				t.Fatal("automatic maintenance compacted below its threshold")
			}
			if err := a.CompactNow(t.Context(), "retain decisions"); err != nil {
				t.Fatal(err)
			}
			if len(prov.got) == 0 || a.currentProjectionVersion() == 0 {
				t.Fatal("manual compaction reported no history for a completed conversation")
			}
			if !reflect.DeepEqual(canonical, sess.Messages) {
				t.Fatal("manual compaction changed the canonical transcript")
			}
			visible := a.modelVisibleMessages()
			if visible[0].Content != canonical[0].Content {
				t.Fatal("system prefix changed")
			}
			foundContext := false
			for _, msg := range visible {
				foundContext = foundContext || msg.Content == contextMessage.Content
			}
			if !foundContext {
				t.Fatal("workspace context was lost")
			}
		})
	}
}

func TestManualCompactWithoutWindowRecoversOverflow(t *testing.T) {
	for _, numbered := range []bool{false, true} {
		t.Run(fmt.Sprintf("numbered=%v", numbered), func(t *testing.T) {
			prov := &denseTokenizerProvider{window: 20_000, unnumberedReplay: !numbered}
			turns := 4 // Replay-only rejection; the transcript representation fits.
			if numbered {
				turns = 16 // Learn the physical window and split the oversized history.
			}
			a := New(prov, nil, longASCIISession(turns), Options{}, event.Discard)
			if err := a.CompactNow(t.Context(), "retain decisions"); err != nil {
				t.Fatal(err)
			}
			if prov.overflows == 0 || len(prov.requests) < 2 || a.currentProjectionVersion() == 0 || !hasCompactionSummary(a.modelVisibleMessages()) {
				t.Fatalf("overflow recovery did not install a summary: calls=%d overflows=%d", len(prov.requests), prov.overflows)
			}
			if numbered && a.ContextUsedTokens() >= a.hardInputCeiling() {
				t.Fatal("manual rescue left the context above the learned ceiling")
			}
			calls := len(prov.requests)
			for range 30 {
				a.sess.conversation.Add(provider.Message{Role: provider.RoleUser, Content: strings.Repeat("continue ", 1000)})
			}
			if err := a.PrepareContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if a.ContextWindow() != 0 || len(prov.requests) != calls {
				t.Fatal("manual recovery re-enabled automatic compaction")
			}
		})
	}
}

func TestManualCompactWithoutWindowReportsSummaryFailure(t *testing.T) {
	want := errors.New("summary service unavailable")
	prov := &fakeProvider{streamErr: want}
	a := New(prov, nil, longASCIISession(4), Options{}, event.Discard)
	if err := a.CompactNow(t.Context(), ""); !errors.Is(err, want) {
		t.Fatalf("error = %v, want original summary error", err)
	}
	if a.currentProjectionVersion() != 0 {
		t.Fatal("unknown ceiling caused lossy truncation after a summary failure")
	}
}

func TestManualCompactWithoutWindowKeepsEmptyAndActiveHistory(t *testing.T) {
	for _, active := range []bool{false, true} {
		prov := &fakeProvider{reply: "- Summary."}
		sess := NewSession("system")
		a := New(prov, nil, sess, Options{}, event.Discard)
		if active {
			sess.Add(provider.Message{Role: provider.RoleUser, Content: "Keep the active request.", CreatedAt: 42})
			sess.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("work in progress ", 100)})
			a.activeTurnCreatedAt.Store(42)
		}
		if err := a.CompactNow(t.Context(), ""); err != nil {
			t.Fatal(err)
		}
		if len(prov.got) != 0 || a.currentProjectionVersion() != 0 {
			t.Fatalf("active=%v: compacted empty or active history", active)
		}
	}
}
