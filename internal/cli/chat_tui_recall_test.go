package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSubmittedInputRecallKeepsDraftAcrossCursorKeys(t *testing.T) {
	m := newTestChatTUI()
	m.rememberSubmittedInput("old")
	m.input.SetValue("my draft")

	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyUp},
		{Code: tea.KeyRight},
		{Code: tea.KeyHome},
	} {
		model, _ := m.Update(key)
		m = model.(chatTUI)
	}
	if got := m.input.Value(); got != "old" {
		t.Fatalf("up should recall %q, got %q", "old", got)
	}

	model, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = model.(chatTUI)
	if got := m.input.Value(); got != "my draft" {
		t.Fatalf("down after cursor keys should restore the draft, got %q", got)
	}
}

func TestSubmittedInputRecallEditedEntryBecomesDraft(t *testing.T) {
	m := newTestChatTUI()
	m.rememberSubmittedInput("old")
	m.input.SetValue("my draft")

	model, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = model.(chatTUI)
	model, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = model.(chatTUI)
	edited := m.input.Value()
	if edited == "old" {
		t.Fatalf("typing should edit the recalled entry")
	}

	model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = model.(chatTUI)
	if got := m.input.Value(); got != edited {
		t.Fatalf("down on an edited entry should leave it alone, got %q want %q", got, edited)
	}
}

func TestSubmittedInputRecallMovesWithinMultiLineEntry(t *testing.T) {
	m := newTestChatTUI()
	m.rememberSubmittedInput("line1\nline2\nline3")
	m.input.SetValue("draft")

	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyUp},
		{Code: tea.KeyLeft},
		{Code: tea.KeyUp},
		{Code: tea.KeyDown},
	} {
		model, _ := m.Update(key)
		m = model.(chatTUI)
	}
	if got := m.input.Value(); got != "line1\nline2\nline3" {
		t.Fatalf("up/down inside a recalled entry should move the cursor, got %q", got)
	}
	if got := m.input.Line(); got != 2 {
		t.Fatalf("down from line 2 should reach the last line, got line %d", got)
	}

	model, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = model.(chatTUI)
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("down on the last line should restore the draft, got %q", got)
	}
}
