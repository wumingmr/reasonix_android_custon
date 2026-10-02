package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/turnevent"
)

func outlineProjection(t *testing.T, baseline []Message) *Projection {
	t.Helper()
	p, err := NewProjection(testIdentity, baseline, 0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func outline(t *testing.T, p *Projection, req OutlineRequest) OutlinePage {
	t.Helper()
	page, err := p.Outline(req)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func turn(recordID, prompt, answer string) []Message {
	rows := []Message{{RecordID: recordID, MessageID: recordID, Role: "user", Content: prompt}}
	if answer != "" {
		rows = append(rows, Message{RecordID: recordID + ":a", MessageID: recordID + ":a", Role: "assistant", Content: answer})
	}
	return rows
}

// The reported defect: a tool-heavy tail fills the first body page, so a rail
// derived from loaded records shows nothing even though the session has turns.
func TestOutlineCoversTurnsOutsideTheLoadedBodyPage(t *testing.T) {
	baseline := turn("m:1", "first question", "first answer")
	baseline = append(baseline, turn("m:2", "second question", "second answer")...)
	for i := range 200 {
		baseline = append(baseline, Message{RecordID: "tool:" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Role: "tool", ToolCallID: "call", Content: "output"})
	}
	p := outlineProjection(t, baseline)

	records := snapshot(t, p).Records
	for _, record := range records {
		if record.Message.Role == "user" {
			t.Fatalf("test premise broken: the default page already contains a user turn")
		}
	}

	page := outline(t, p, OutlineRequest{})
	if page.Stale || page.Total != 2 || len(page.Entries) != 2 {
		t.Fatalf("outline = %+v, want 2 complete turns", page)
	}
	if page.Entries[0].Prompt != "first question" || page.Entries[0].Answer != "first answer" {
		t.Fatalf("first entry = %+v", page.Entries[0])
	}
	if page.Entries[1].Prompt != "second question" {
		t.Fatalf("second entry = %+v", page.Entries[1])
	}
}

// A page's identity must survive the body cursor moving, and must match the
// record identity the body pages use.
func TestOutlineIdentityMatchesBodyRecords(t *testing.T) {
	baseline := turn("m:1", "one", "answer one")
	baseline = append(baseline, turn("m:2", "two", "answer two")...)
	baseline = append(baseline, turn("m:3", "three", "answer three")...)
	p := outlineProjection(t, baseline)

	full := snapshot(t, p)
	wantOrder := map[string]int{}
	for _, record := range full.Records {
		if record.Message.Role == "user" {
			wantOrder[record.ID] = record.Order
		}
	}
	if len(wantOrder) != 3 {
		t.Fatalf("body page holds %d user records, want 3", len(wantOrder))
	}

	first := outline(t, p, OutlineRequest{SnapshotID: full.SnapshotID, Entries: 2})
	if first.Total != 3 || len(first.Entries) != 2 || first.Done {
		t.Fatalf("first page = %+v", first)
	}
	if first.NextOffset != 2 || first.SnapshotID != full.SnapshotID {
		t.Fatalf("first page cursor = %+v", first)
	}
	second := outline(t, p, OutlineRequest{SnapshotID: full.SnapshotID, Offset: first.NextOffset})
	if len(second.Entries) != 1 || !second.Done || second.NextOffset != 3 {
		t.Fatalf("second page = %+v", second)
	}
	for _, entry := range append(append([]OutlineEntry{}, first.Entries...), second.Entries...) {
		order, ok := wantOrder[entry.ID]
		if !ok {
			t.Fatalf("outline entry %q has no body record", entry.ID)
		}
		if entry.Order != order {
			t.Fatalf("entry %q order = %d, want %d", entry.ID, entry.Order, order)
		}
	}
	if first.Entries[0].Turn != 1 || second.Entries[0].Turn != 3 {
		t.Fatalf("turn numbering is not absolute: %d..%d", first.Entries[0].Turn, second.Entries[0].Turn)
	}
}

// Loading an older body page must not renumber or shrink the rail.
func TestOutlineSurvivesOlderBodyPaging(t *testing.T) {
	baseline := turn("m:1", "one", "answer one")
	baseline = append(baseline, turn("m:2", "two", "answer two")...)
	baseline = append(baseline, turn("m:3", "three", "answer three")...)
	for i := range 200 {
		baseline = append(baseline, Message{RecordID: fmt.Sprintf("tool:%d", i), Role: "tool", ToolCallID: "call", Content: "output"})
	}
	p := outlineProjection(t, baseline)

	full := snapshot(t, p)
	if full.Before == 0 {
		t.Fatalf("test premise broken: the default page already covers every record")
	}
	before := outline(t, p, OutlineRequest{SnapshotID: full.SnapshotID})

	page, err := p.Snapshot(PageRequest{SnapshotID: full.SnapshotID, Before: full.Before, Records: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Before >= full.Before {
		t.Fatalf("older page did not advance the cursor: %+v", page)
	}
	after := outline(t, p, OutlineRequest{SnapshotID: full.SnapshotID})
	if after.Total != before.Total || len(after.Entries) != len(before.Entries) {
		t.Fatalf("older paging shrank the outline: %d -> %d", before.Total, after.Total)
	}
	for i := range after.Entries {
		if after.Entries[i] != before.Entries[i] {
			t.Fatalf("entry %d changed after paging: %+v -> %+v", i, before.Entries[i], after.Entries[i])
		}
	}
}

// An evicted cut must report staleness rather than answering positions against
// the newest revision.
func TestOutlineReportsStaleCutInsteadOfRepositioning(t *testing.T) {
	p := outlineProjection(t, turn("m:1", "one", "answer one"))
	full := snapshot(t, p)

	stale := outline(t, p, OutlineRequest{SnapshotID: "evicted-cut"})
	if !stale.Stale || len(stale.Entries) != 0 {
		t.Fatalf("stale outline = %+v", stale)
	}
	if stale.SnapshotID != full.SnapshotID || stale.ProtocolVersion != ProtocolVersion {
		t.Fatalf("stale outline must still carry the live boundary: %+v", stale.Boundary)
	}
}

func TestOutlinePreviewIsBoundedCollapsedAndDisplayOnly(t *testing.T) {
	longPrompt := "  first\n\nline\t" + strings.Repeat("宽", 80)
	longAnswer := "answer\n\nbody " + strings.Repeat("答", 200)
	baseline := []Message{
		{RecordID: "m:1", MessageID: "m:1", Role: "user", Content: longPrompt},
		{RecordID: "m:1:think", MessageID: "m:1:think", Role: "assistant", Reasoning: strings.Repeat("thinking ", 40), Content: "short answer"},
		{RecordID: "m:1:tool", Role: "tool", ToolCallID: "call", Content: strings.Repeat("tool output ", 40)},
		{RecordID: "m:1:a", MessageID: "m:1:a", Role: "assistant", Content: longAnswer},
	}
	p := outlineProjection(t, baseline)

	page := outline(t, p, OutlineRequest{})
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(page.Entries))
	}
	entry := page.Entries[0]
	if strings.ContainsAny(entry.Prompt, "\n\t") || strings.Contains(entry.Prompt, "  ") {
		t.Fatalf("prompt whitespace was not collapsed: %q", entry.Prompt)
	}
	if got := len([]rune(strings.TrimSuffix(entry.Prompt, "…"))); got > promptPreviewRunes {
		t.Fatalf("prompt preview = %d runes, want <= %d", got, promptPreviewRunes)
	}
	if got := len([]rune(strings.TrimSuffix(entry.Answer, "…"))); got > answerPreviewRunes {
		t.Fatalf("answer preview = %d runes, want <= %d", got, answerPreviewRunes)
	}
	// The last assistant body wins; reasoning and tool output never leak in.
	if !strings.HasPrefix(entry.Answer, "answer body 答") {
		t.Fatalf("answer preview = %q, want the final assistant body", entry.Answer)
	}
	if strings.Contains(entry.Answer, "thinking") || strings.Contains(entry.Answer, "tool output") {
		t.Fatalf("preview leaked reasoning or tool output: %q", entry.Answer)
	}
}

func TestOutlineKeepsEmptyPromptTurnIdentity(t *testing.T) {
	baseline := []Message{
		{RecordID: "m:1", MessageID: "m:1", Role: "user", Content: "   "},
		{RecordID: "m:1:a", MessageID: "m:1:a", Role: "assistant", Content: "answer"},
		{RecordID: "m:2", MessageID: "m:2", Role: "user", Content: "second"},
	}
	p := outlineProjection(t, baseline)

	page := outline(t, p, OutlineRequest{})
	if page.Total != 2 {
		t.Fatalf("empty prompt removed a navigation item: %+v", page)
	}
	if page.Entries[0].ID != "m:1" || page.Entries[0].Turn != 1 || page.Entries[1].Turn != 2 {
		t.Fatalf("turn identity or numbering = %+v", page.Entries)
	}
}

// A turn with no completed answer preface yet keeps its identity and simply
// carries no answer preview.
func TestOutlineRunningTurnHasIdentityWithoutAnswer(t *testing.T) {
	p := outlineProjection(t, turn("m:1", "one", "answer one"))
	projectEvent(t, p, 1, event.Event{Kind: event.UserMessage, MessageID: "2", Text: "running question"})

	page := outline(t, p, OutlineRequest{})
	if page.Total != 2 {
		t.Fatalf("running turn missing from outline: %+v", page)
	}
	if page.Entries[1].ID != "m:2" || page.Entries[1].Prompt != "running question" || page.Entries[1].Answer != "" {
		t.Fatalf("running turn = %+v", page.Entries[1])
	}
}

func TestOutlinePageRespectsByteBudgetAndAdvances(t *testing.T) {
	var baseline []Message
	for i := range 40 {
		baseline = append(baseline, turn(recordName(i), strings.Repeat("q", 40), strings.Repeat("a", 80))...)
	}
	p := outlineProjection(t, baseline)

	page := outline(t, p, OutlineRequest{Bytes: 1})
	if len(page.Entries) != 1 {
		t.Fatalf("a byte budget below one entry must still advance: %+v", page)
	}
	if page.Done || page.NextOffset != 1 {
		t.Fatalf("cursor did not advance: %+v", page)
	}
	if got := outline(t, p, OutlineRequest{Offset: page.NextOffset, Bytes: defaultOutlineBytes}); len(got.Entries) == 0 {
		t.Fatalf("second page is empty: %+v", got)
	}
	total := outline(t, p, OutlineRequest{})
	if total.Total != 40 {
		t.Fatalf("total = %d, want 40", total.Total)
	}
}

func TestOutlinePageStaysWithinResponseLimit(t *testing.T) {
	var baseline []Message
	for i := range 300 {
		baseline = append(baseline, turn(recordName(i), strings.Repeat("промпт", 20), strings.Repeat("ответ", 60))...)
	}
	p := outlineProjection(t, baseline)

	page := outline(t, p, OutlineRequest{Entries: 10_000, Bytes: 64 << 20})
	if len(page.Entries) > maxOutlineEntries {
		t.Fatalf("entry cap ignored: %d", len(page.Entries))
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded)+1 > MaxResponseBytes {
		t.Fatalf("outline page = %d bytes, over the response limit", len(encoded))
	}
}

// Offsets beyond the end clamp instead of reporting a negative or skipped page.
func TestOutlineOffsetPastEndIsDone(t *testing.T) {
	p := outlineProjection(t, turn("m:1", "one", "answer one"))
	page := outline(t, p, OutlineRequest{Offset: 99})
	if len(page.Entries) != 0 || !page.Done || page.NextOffset != 1 || page.Total != 1 {
		t.Fatalf("past-end page = %+v", page)
	}
}

func TestOutlineEmptySessionIsComplete(t *testing.T) {
	p := outlineProjection(t, nil)
	page := outline(t, p, OutlineRequest{})
	if page.Total != 0 || !page.Done || page.Stale || len(page.Entries) != 0 {
		t.Fatalf("empty outline = %+v", page)
	}
	// Entries must encode as an empty array, never null, so clients can iterate.
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"entries":[]`) {
		t.Fatalf("entries encoded as %s", encoded)
	}
}

func recordName(i int) string {
	return fmt.Sprintf("m:%d", i)
}

// Outline reads share the projection mutex with streaming commits, and an
// evicted cut must never be served from a newer revision.
func TestOutlineConcurrentWithStreamingCommits(t *testing.T) {
	baseline := turn("m:1", "one", "answer one")
	baseline = append(baseline, turn("m:2", "two", "answer two")...)
	p := outlineProjection(t, baseline)

	const workers, readers = 4, 8
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for worker := range workers {
		wg.Go(func() {
			sequence := uint64(0)
			for {
				select {
				case <-stop:
					return
				default:
				}
				sequence++
				_ = p.Apply(turnevent.Envelope{
					SessionID: "session", RuntimeEpoch: "runtime", TurnID: fmt.Sprintf("turn-%d", worker),
					Sequence: sequence, Kind: "message", Status: event.TurnInProgress,
					Event: eventwire.Event{Kind: "message", MessageID: fmt.Sprintf("w%d-%d", worker, sequence), Text: "streamed"},
				})
			}
		})
	}
	var reads atomic.Int64
	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				page, err := p.Outline(OutlineRequest{Entries: 4})
				if err != nil {
					t.Errorf("outline: %v", err)
					return
				}
				// Every page either describes a live cut or reports staleness.
				if !page.Stale && page.Total < 2 {
					t.Errorf("live outline lost turns: %+v", page)
					return
				}
				reads.Add(1)
			}
		})
	}
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
	if reads.Load() == 0 {
		t.Fatal("no outline reads overlapped the writers")
	}
}
