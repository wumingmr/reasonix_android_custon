package cli

import (
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
