package session

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestCatalogReducerMatchesCanonicalProjection(t *testing.T) {
	r := catalogReducer{}
	var commits []Commit
	add := func(kind string, body any) {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		sequence := uint64(len(commits) + 1)
		commit := Commit{TurnID: "turn", FirstSequence: sequence, EventCount: 1, Events: []Event{{Kind: kind, Sequence: sequence, Payload: payload}}}
		commits = append(commits, commit)
		full, err := Project(commits)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.apply(commit); err != nil {
			t.Fatal(err)
		}
		want, got := metadataFromProjection(Manifest{}, sequence, full), r.metadata(Manifest{})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after %s: got %+v want %+v", kind, got, want)
		}
		if len(r.state.Messages)+len(r.state.ModelMessages)+len(r.state.Turns)+len(r.state.ActiveTools)+len(r.state.Interactions) != 0 {
			t.Fatal("body or authority state survived metadata reduction")
		}
	}
	message := func(id, text string) provider.Message {
		return provider.Message{ID: id, Role: provider.RoleUser, RawContent: text, Content: text}
	}
	add("legacy/import", map[string]any{"messages": []provider.Message{message("old", "old request")}, "modelRef": "old-model"})
	add("session/title", map[string]any{"title": "custom title"})
	add("session/config", map[string]any{"modelRef": "new-model", "modelIdentity": "identity"})
	add("turn/start", map[string]any{})
	add("message/complete", map[string]any{"message": message("second", "second request")})
	add("message/upsert", map[string]any{"message": message("old", "")})
	add("model/context-replace", map[string]any{"messages": []provider.Message{message("model", "not a preview")}})
	add("compaction", map[string]any{"messages": []provider.Message{message("compact", "not a preview either")}})
	add("turn/end", map[string]any{"status": "completed"})
	add("turn/end", map[string]any{"status": "completed"})
	add("history/replace", map[string]any{"messages": []provider.Message{message("x", "first"), message("x", "duplicate allowed by replacement"), message("y", "last")}})
	add("message/upsert", map[string]any{"message": message("x", "")})
	add("message/upsert", map[string]any{"message": provider.Message{ID: "y", Role: provider.RoleUser, Origin: provider.MessageOriginHost, Content: "host"}})
	add("history/replace", map[string]any{"messages": []provider.Message{}})
	for i := range 100 {
		add("message/complete", map[string]any{"message": message(fmt.Sprint(i), strings.Repeat("body", 100))})
		add("message/upsert", map[string]any{"message": message(fmt.Sprint(i), "")})
	}
}

func TestCatalogReducerKeepsFirstCompleteAndRejectsMalformedPayload(t *testing.T) {
	for _, kind := range []string{"message/complete", "session/config", "turn/end", "tool/result"} {
		r := catalogReducer{}
		if err := r.apply(Commit{Events: []Event{{Kind: kind, Payload: json.RawMessage(`{}`)}}}); err == nil {
			t.Fatalf("accepted malformed %s", kind)
		}
	}
	r := catalogReducer{}
	first := Commit{Events: []Event{{Kind: "message/complete", Payload: json.RawMessage(`{"message":{"id":"same","role":"user","content":"hello"}}`)}}}
	repeat := Commit{Events: []Event{{Kind: "message/complete", Payload: json.RawMessage(`{"message":{"id":"same","role":"user","content":"repeat"}}`)}}}
	for _, commit := range []Commit{first, repeat} {
		if err := r.apply(commit); err != nil {
			t.Fatalf("duplicate complete across commits: %v", err)
		}
	}
	if got := r.metadata(Manifest{}).Preview; got != "hello" {
		t.Fatalf("preview = %q, want the first occurrence", got)
	}
}

func TestCatalogResultSequenceAdvancesOnlyForVisibleAssistantResults(t *testing.T) {
	assistant, _ := json.Marshal(map[string]any{"message": provider.Message{ID: "answer", Role: provider.RoleAssistant, Content: "done"}})
	commits := []Commit{
		{TurnID: "turn-1", FirstSequence: 1, EventCount: 1, Events: []Event{{Kind: "turn/start", Sequence: 1, Payload: json.RawMessage(`{}`)}}},
		{TurnID: "turn-1", FirstSequence: 2, EventCount: 1, Events: []Event{{Kind: "message/complete", Sequence: 2, Payload: assistant}}},
		{TurnID: "turn-1", FirstSequence: 3, EventCount: 1, Events: []Event{{Kind: "turn/end", Sequence: 3, Payload: json.RawMessage(`{"status":"completed"}`)}}},
		{FirstSequence: 4, EventCount: 1, Events: []Event{{Kind: "plan/state", Sequence: 4, Payload: json.RawMessage(`{"enabled":false}`)}}},
		{FirstSequence: 5, EventCount: 1, Events: []Event{{Kind: "session/title", Sequence: 5, Payload: json.RawMessage(`{"title":"renamed"}`)}}},
	}
	r := catalogReducer{}
	for _, commit := range commits {
		if err := r.apply(commit); err != nil {
			t.Fatal(err)
		}
	}
	metadata := r.metadata(Manifest{})
	if metadata.ResultSequence != 3 {
		t.Fatalf("result sequence = %d, want completed answer boundary 3", metadata.ResultSequence)
	}
	if metadata.Sequence != 5 {
		t.Fatalf("event sequence = %d, want all events through 5", metadata.Sequence)
	}
}

type largeCatalogReader struct {
	count          int
	baseline, peak uint64
	allocatedPeak  uint64
	t              *testing.T
}

func (h *largeCatalogReader) Read(ctx context.Context, cursor uint64, limit int) (EventPage, error) {
	if err := ctx.Err(); err != nil {
		return EventPage{}, err
	}
	var current runtime.MemStats
	runtime.ReadMemStats(&current)
	if current.HeapAlloc > h.baseline {
		h.allocatedPeak = max(h.allocatedPeak, current.HeapAlloc-h.baseline)
	}
	if cursor%256 == 0 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		if stats.HeapAlloc > h.baseline {
			h.peak = max(h.peak, stats.HeapAlloc-h.baseline)
		}
		if h.peak > 32<<20 {
			h.t.Fatalf("retained heap grew with message bodies: %.1f MiB", float64(h.peak)/(1<<20))
		}
	}
	page := EventPage{}
	for i := int(cursor); i < min(int(cursor)+limit, h.count); i++ {
		role := provider.RoleAssistant
		if i == 0 {
			role = provider.RoleUser
		}
		payload, _ := json.Marshal(map[string]any{"message": provider.Message{ID: fmt.Sprint(i), Role: role, Content: strings.Repeat("a", 64<<10)}})
		seq := uint64(i + 1)
		page.Commits = append(page.Commits, Commit{FirstSequence: seq, EventCount: 1, Events: []Event{{Kind: "message/complete", Sequence: seq, Payload: payload}}})
		page.Next = seq
	}
	page.Truncated = int(page.Next) < h.count
	return page, nil
}

type streamingCatalogProbe struct {
	pagedCatalogHandle
	scans int
}

func (*streamingCatalogProbe) Read(context.Context, uint64, int) (EventPage, error) {
	panic("streaming catalog unexpectedly used sparse pages")
}
func (p *streamingCatalogProbe) scanCatalog(_ context.Context, apply func(Commit) error) error {
	p.scans++
	return apply(Commit{Events: []Event{{Kind: "session/title", Sequence: 1, Payload: json.RawMessage(`{"title":"streamed"}`)}}})
}

func TestCatalogUsesSinglePassReaderWhenAvailable(t *testing.T) {
	p := &streamingCatalogProbe{}
	m, err := reduceCatalogMetadata(t.Context(), p, Manifest{})
	if err != nil || m.Title != "streamed" || p.scans != 1 {
		t.Fatalf("metadata=%+v scans=%d err=%v", m, p.scans, err)
	}
}

func TestCatalogReducerLargeHistoryRetainedHeap(t *testing.T) {
	for _, count := range []int{512, 8192, 8192} {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		h := &largeCatalogReader{count: count, baseline: stats.HeapAlloc, t: t}
		m, err := reduceCatalogMetadata(t.Context(), h, Manifest{})
		if err != nil || m.Sequence != uint64(count) || m.Preview == "" {
			t.Fatalf("metadata=%+v err=%v", m, err)
		}
		t.Logf("%d messages / %d MiB text: sampled retained heap %.2f MiB, sampled heap peak %.2f MiB", count, count*64/1024, float64(h.peak)/(1<<20), float64(h.allocatedPeak)/(1<<20))
		runtime.GC()
		runtime.ReadMemStats(&stats)
		if stats.HeapAlloc > h.baseline+8<<20 {
			t.Fatalf("completed rebuild retained %.2f MiB", float64(stats.HeapAlloc-h.baseline)/(1<<20))
		}
		runtime.KeepAlive(m)
	}
}
