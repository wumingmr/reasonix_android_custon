package cli

import (
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// Text is populated only for printable keys, so a key press that arrives with
// an empty Text is not user input: Termux IMEs emit those while composing.
// Treating String() as a fallback inserted composition leftovers and dropped
// the composed text's trailing space, because a space key reports "space".
func TestQuickPickerIgnoresKeyPressWithoutText(t *testing.T) {
	p := &quickPicker{}
	p.handleKey(tea.KeyPressMsg{Code: 'x', Text: "鹈"})
	p.handleKey(tea.KeyPressMsg{Code: 'x', Text: ""})

	if p.query != "鹈" {
		t.Fatalf("query = %q, want %q: an IME composition key must not become text", p.query, "鹈")
	}
}

func TestQuickPickerAcceptsSpaceInQuery(t *testing.T) {
	p := &quickPicker{
		items: []quickPickerItem{
			{ID: "one", Label: "hello world"},
			{ID: "two", Label: "hello there"},
			{ID: "three", Label: "goodbye"},
		},
	}
	for _, r := range "hello world" {
		p.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	if p.query != "hello world" {
		t.Fatalf("query = %q, want %q: every typed character must be kept", p.query, "hello world")
	}
	items := p.filteredItems()
	if len(items) != 1 || items[0].ID != "one" {
		t.Fatalf("filtered items = %+v, want the multi-word match only", items)
	}
}

func TestQuickPickerBackspaceRemovesOneRune(t *testing.T) {
	p := &quickPicker{}
	for _, r := range "鹈鹕" {
		p.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	p.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})

	if p.query != "鹈" {
		t.Fatalf("query = %q, want %q: backspace must drop one rune", p.query, "鹈")
	}
	if !utf8.ValidString(p.query) {
		t.Fatalf("query is not valid UTF-8: %q", p.query)
	}
}

func TestQuickPickerComposedTextKeepsTrailingSpace(t *testing.T) {
	p := &quickPicker{
		items: []quickPickerItem{{ID: "one", Label: "鹈鹕 骑车"}},
	}
	p.handleKey(tea.KeyPressMsg{Code: 'x', Text: "鹈鹕 "})

	if p.query != "鹈鹕 " {
		t.Fatalf("query = %q, want %q: a composed string arrives whole", p.query, "鹈鹕 ")
	}
	if items := p.filteredItems(); len(items) != 1 {
		t.Fatalf("filtered items = %+v, want one match", items)
	}
}
