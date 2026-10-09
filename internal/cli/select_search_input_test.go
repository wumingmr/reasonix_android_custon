package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// The search menus read raw bytes from a raw-mode stdin. A single Chinese
// character is three bytes whose lead byte (0xE9-0xEF) fails every ASCII
// printable test, so the old `k[0] >= 32 && k[0] < 127` guard dropped the whole
// character into the ignored default branch.
func TestSearchTextAcceptsMultiByteRunes(t *testing.T) {
	items := []menuItem{
		{name: "获取一下 github KeyAttestation 检测原理", desc: "2026-10-05 21:51"},
		{name: "不用搜索帮我写一个鹈鹕骑自行车的动画", desc: "2026-10-05 19:48"},
		{name: "hello world test", desc: "2026-10-04 10:00"},
	}

	cases := []struct {
		name  string
		chunk string
		want  string
	}{
		{"单个汉字", "鹈", "鹈"},
		{"一次读入多个汉字", "鹈鹕骑", "鹈鹕骑"},
		{"汉字间含空格", "鹈鹕 骑", "鹈鹕 骑"},
		{"英文单词", "hello world", "hello world"},
		{"中英混合", "鹈 test", "鹈 test"},
	}
	for _, tc := range cases {
		got, rest := searchText("", nil, []byte(tc.chunk))
		if got != tc.want {
			t.Fatalf("%s: searchText(%q) = %q, want %q", tc.name, tc.chunk, got, tc.want)
		}
		if len(rest) != 0 {
			t.Fatalf("%s: left over bytes %v, want none", tc.name, rest)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("%s: result %q is not valid UTF-8", tc.name, got)
		}
	}

	// The whole point: a Chinese query must actually match a Chinese label.
	if n := len(filterMenuItems(items, "鹈鹕")); n != 1 {
		t.Fatalf("filterMenuItems(%q) matched %d items, want 1", "鹈鹕", n)
	}
	if n := len(filterMenuItems(items, "hello world")); n != 1 {
		t.Fatalf("filterMenuItems(%q) matched %d items, want 1", "hello world", n)
	}
}

func TestSearchTextDropsControlBytes(t *testing.T) {
	// A chunk can carry more than the character it represents; control bytes
	// must not end up inside the query text.
	got, _ := searchText("", nil, []byte{'a', 3, 'b'})
	if got != "ab" {
		t.Fatalf("searchText with Ctrl-C = %q, want %q", got, "ab")
	}
	if got, _ = searchText("", nil, []byte("a\x7fb")); got != "ab" {
		t.Fatalf("searchText with DEL = %q, want %q", got, "ab")
	}
}

func TestSearchTextCompletesCharacterSplitAcrossReads(t *testing.T) {
	// A read can stop mid-character. The fragment must be held for the next read
	// rather than committed as invalid UTF-8 or dropped outright.
	got, pending := searchText("", nil, []byte{0xe9, 0xb9})
	if got != "" {
		t.Fatalf("searchText(partial) = %q, want empty", got)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want the 2 held-back bytes", pending)
	}
	// The rest of the character arrives on the next read and completes it.
	got, pending = searchText(got, pending, []byte{0x88})
	if got != "鹈" {
		t.Fatalf("searchText(remainder) = %q, want %q", got, "鹈")
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want empty after completion", pending)
	}
}

func TestSearchInputBufHoldsABurst(t *testing.T) {
	// The old 8-byte buffer truncated a longer burst mid-character.
	burst := []byte("鹈鹕骑自行车的动画效果")
	if len(burst) > searchInputBuf {
		t.Fatalf("test burst (%d bytes) exceeds searchInputBuf (%d)", len(burst), searchInputBuf)
	}
	got, pending := searchText("", nil, burst)
	if pending != nil {
		t.Fatalf("pending = %v, want nil for a complete burst", pending)
	}
	if !utf8.ValidString(got) || got != string(burst) {
		t.Fatalf("searchText(burst) = %q, want %q", got, string(burst))
	}
}

func TestTrimSearchRuneRemovesOneCharacter(t *testing.T) {
	if got := trimSearchRune("鹈鹕"); got != "鹈" {
		t.Fatalf("trimSearchRune(%q) = %q, want %q", "鹈鹕", got, "鹈")
	}
	if got := trimSearchRune("鹈"); got != "" {
		t.Fatalf("trimSearchRune(%q) = %q, want empty", "鹈", got)
	}
	if got := trimSearchRune("hello world"); got != "hello worl" {
		t.Fatalf("trimSearchRune(%q) = %q, want %q", "hello world", got, "hello worl")
	}
	if got := trimSearchRune(""); got != "" {
		t.Fatalf("trimSearchRune(%q) = %q, want empty", "", got)
	}
	// Deleting a multi-byte character must never leave a broken rune behind.
	trimmed := trimSearchRune("鹈鹕骑")
	if !utf8.ValidString(trimmed) {
		t.Fatalf("trimSearchRune left invalid UTF-8: %q", trimmed)
	}
}

// The wiring in selectOne/selectMany must store searchText's second result even
// when the query text itself did not change. A character cut in half by this
// read contributes nothing to the query, so the branch that only assigned
// inside `if next != searchQuery` dropped the held-back fragment and the
// character could never be completed — the exact failure searchPending exists
// to prevent.
//
// This drives searchState.applySearchChunk — the function both menus actually
// call — rather than a copy of its logic. A test that re-implements the wiring
// passes even when the wiring is wrong, which is exactly how the defect this
// guards against survived review once already.
func TestSearchPendingSurvivesChunkThatAddsNothing(t *testing.T) {
	// "鹈" is e9 b9 88, split by the terminal driver across two reads.
	var s searchState
	redraws := 0
	for _, k := range [][]byte{{0xe9, 0xb9}, {0x88}} {
		if s.applySearchChunk(k) {
			redraws++
		}
	}

	if s.query != "鹈" {
		t.Fatalf("query = %q after completing the character, want %q", s.query, "鹈")
	}
	if len(s.pending) != 0 {
		t.Fatalf("pending = %v after the character completed, want empty", s.pending)
	}
	if n := len(filterMenuItems([]menuItem{{name: "鹈鹕骑自行车"}}, s.query)); n != 1 {
		t.Fatalf("a character split across two reads matched %d items, want 1", n)
	}
	// The first chunk changes nothing visible, so it must not report a redraw;
	// the second completes the character and must.
	if redraws != 1 {
		t.Fatalf("redraws = %d, want 1 (only the chunk that completed the character)", redraws)
	}
}

// A read can catch a typed character together with an arrow key pressed right
// after it. Decoding the whole chunk would then append the sequence's body
// ("[A") to the query, which matches nothing and silently empties the result
// list. Only the chunk's first byte used to be consulted, so the pair
// contributed just the character.
func TestSearchTextSkipsEscapeSequenceInsideChunk(t *testing.T) {
	cases := []struct {
		name  string
		chunk []byte
		want  string
	}{
		{"字符后紧跟方向键上", []byte{'a', 0x1b, '[', 'A'}, "a"},
		{"字符后紧跟方向键下", []byte{'a', 0x1b, '[', 'B'}, "a"},
		{"字符后紧跟带参数序列", []byte{'a', 0x1b, '[', '3', '~'}, "a"},
		{"汉字后紧跟方向键", append([]byte("鹈"), 0x1b, '[', 'A'), "鹈"},
		{"裸 ESC 仍按控制字节丢弃", []byte{'a', 0x1b}, "a"},
		{"单独的 ESC 序列", []byte{0x1b, '[', 'A'}, ""},
	}
	for _, tc := range cases {
		got, rest := searchText("", nil, tc.chunk)
		if got != tc.want {
			t.Fatalf("%s: searchText(%v) = %q, want %q", tc.name, tc.chunk, got, tc.want)
		}
		if len(rest) != 0 {
			t.Fatalf("%s: left over bytes %v, want none", tc.name, rest)
		}
	}
}

func TestEscSeqLen(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want int
	}{
		{"CSI 上", []byte{0x1b, '[', 'A'}, 3},
		{"CSI 带参数", []byte{0x1b, '[', '1', '5', '~'}, 5},
		{"SS3", []byte{0x1b, 'O', 'A'}, 3},
		{"不完整 CSI", []byte{0x1b, '['}, 0},
		{"不完整 SS3", []byte{0x1b, 'O'}, 0},
		{"裸 ESC", []byte{0x1b}, 0},
		{"非 ESC 开头", []byte("a"), 0},
	}
	for _, tc := range cases {
		if got := escSeqLen(tc.in); got != tc.want {
			t.Fatalf("%s: escSeqLen(%v) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
}

// An arrow key's escape sequence starts with ESC (0x1b), the same byte as the
// cancel key. When the menus decided "is this Esc?" with `k[0] == 27` alone,
// every arrow press cleared the query and left search mode, so a filtered list
// could be searched but never navigated — pressing up or down looked like the
// search had been cancelled. escArrow must claim the arrows before that branch.
func TestEscArrowClaimsArrowsBeforeBareEsc(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want int
	}{
		{"CSI 上", []byte{0x1b, '[', 'A'}, -1},
		{"CSI 下", []byte{0x1b, '[', 'B'}, 1},
		{"带参数的下 1;2B", []byte{0x1b, '[', '1', ';', '2', 'B'}, 1},
		{"SS3 上", []byte{0x1b, 'O', 'A'}, -1},
		{"SS3 下", []byte{0x1b, 'O', 'B'}, 1},
		{"裸 Esc 仍是取消键", []byte{0x1b}, 0},
		{"不完整序列 ESC[", []byte{0x1b, '['}, 0},
		{"Delete 不是方向键", []byte{0x1b, '[', '3', '~'}, 0},
		{"Home 不是方向键", []byte{0x1b, '[', 'H'}, 0},
		{"普通字符", []byte{'a'}, 0},
	}
	for _, tc := range cases {
		if got := escArrow(tc.in); got != tc.want {
			t.Fatalf("%s: escArrow(%v) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
}

// The regression that matters is the *order* of the cases in each menu's search
// branch: the arrow cases must precede the bare-Esc case, because an arrow's
// lead byte is ESC too. Asserting that on the source keeps the test honest —
// unlike a copy of the dispatch logic, which keeps passing after the production
// order is broken.
func TestSearchBranchChecksArrowsBeforeBareEsc(t *testing.T) {
	src, err := os.ReadFile("select.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"// up — move the selection", "// down — move the selection", "// up — move the cursor", "// down — move the cursor"} {
		if !bytes.Contains(src, []byte(marker)) {
			t.Fatalf("select.go lost the search-branch case %q — the arrows would fall into the Esc branch again", marker)
		}
	}

	// In both menus the Esc case must be written as `k[0] == 27` and sit after
	// every arrow case inside the same search branch.
	body := string(src)
	for _, sel := range []string{"sel", "cur"} {
		arrow := strings.Index(body, "// up — move the "+map[string]string{"sel": "selection", "cur": "cursor"}[sel])
		if arrow < 0 {
			t.Fatalf("no arrow case for %q", sel)
		}
		esc := strings.Index(body[arrow:], "case k[0] == 27:")
		if esc < 0 {
			t.Fatalf("the Esc case for %q is missing after the arrow cases", sel)
		}
		// Nothing but the down case may sit between the up case and the Esc case.
		between := body[arrow : arrow+esc]
		if strings.Contains(between, "searching = false") {
			t.Fatalf("the Esc branch (searching = false) sits between the arrow cases for %q", sel)
		}
	}

	// selectOne's search branch has no trailing redraw, so each arrow case must
	// redraw itself. Without it the selection index moves while the screen keeps
	// showing the old row, which reads as "the row cannot be selected" — the same
	// symptom as the bug this branch was added to fix. selectMany is exempt: its
	// search branch ends in an unconditional redraw().
	one := body[strings.Index(body, "func selectOne"):strings.Index(body, "func selectMany")]
	search := one[strings.Index(one, "if searching {"):]
	search = search[:strings.Index(search, "\n\t\tswitch {")]
	for _, dir := range []string{"up — move the selection", "down — move the selection"} {
		i := strings.Index(search, dir)
		if i < 0 {
			t.Fatalf("selectOne lost the %q case", dir)
		}
		caseBody := search[i : strings.Index(search[i:], "\n\t\t\tcase ")+i]
		if !strings.Contains(caseBody, "redraw()") {
			t.Fatalf("selectOne's %q case does not redraw — the selection would move without the screen following", dir)
		}
	}
}

// A bare Esc must still cancel the search, and an arrow must not. escArrow is the
// predicate the menus branch on, so its verdict is what decides.
func TestBareEscCancelsButArrowsDoNot(t *testing.T) {
	items := []menuItem{{name: "鹈鹕骑自行车"}, {name: "鹈鹕降落"}, {name: "hello world"}}
	searching := true
	searchQuery := "鹈鹕"
	sel := 0
	filtered := filterMenuItems(items, searchQuery)

	// The menu's search branch, reading escArrow exactly as select.go does.
	apply := func(k []byte) {
		switch {
		case escArrow(k) < 0:
			if sel > 0 {
				sel--
			}
		case escArrow(k) > 0:
			if sel < len(filtered)-1 {
				sel++
			}
		case k[0] == 27:
			searching = false
			searchQuery = ""
			filtered = items
			sel = 0
		}
	}

	apply([]byte{0x1b, '[', 'B'})
	if !searching || searchQuery != "鹈鹕" || sel != 1 {
		t.Fatalf("down arrow: searching=%v query=%q sel=%d, want true/鹈鹕/1", searching, searchQuery, sel)
	}
	apply([]byte{0x1b, '[', 'A'})
	if !searching || searchQuery != "鹈鹕" || sel != 0 {
		t.Fatalf("up arrow: searching=%v query=%q sel=%d, want true/鹈鹕/0", searching, searchQuery, sel)
	}
	apply([]byte{0x1b})
	if searching || searchQuery != "" || sel != 0 || len(filtered) != len(items) {
		t.Fatalf("bare Esc did not cancel: searching=%v query=%q sel=%d rows=%d", searching, searchQuery, sel, len(filtered))
	}
}
