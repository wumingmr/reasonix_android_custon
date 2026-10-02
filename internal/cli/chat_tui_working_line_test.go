package cli

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

// TestWorkingLineRowsWrapAccounting pins the pure wrap-count helper shared by
// the composer cursor offset (rowsAboveBox) and the bottomRows height budget:
// the working (spinner) line above the input box occupies more than one
// terminal row on narrow terminals, and the helper must report the wrapped
// count, not 1.
func TestWorkingLineRowsWrapAccounting(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  int
	}{
		{name: "hidden line", text: "", width: 30, want: 0},
		{name: "no width", text: "Thinking…", width: 0, want: 0},
		{name: "fits one row", text: "  Thinking… 5s", width: 30, want: 1},
		{name: "exact fit stays one row", text: strings.Repeat("x", 30), width: 30, want: 1},
		{name: "wraps to two rows", text: strings.Repeat("x", 60), width: 30, want: 2},
		{name: "wraps to three rows", text: strings.Repeat("x", 61), width: 30, want: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workingLineRows(tc.text, tc.width); got != tc.want {
				t.Fatalf("workingLineRows(%q, %d) = %d, want %d", tc.text, tc.width, got, tc.want)
			}
		})
	}
}

// TestWorkingLineWrapKeepsComposerCursorInBox is the #7537 regression test: on
// a narrow terminal the working (spinner) line above the composer wraps to
// multiple terminal rows, and View() must still anchor the real terminal
// cursor inside the composer box instead of drifting above it.
func TestWorkingLineWrapKeepsComposerCursorInBox(t *testing.T) {
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 24)

	m0, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 12})
	m = m0.(chatTUI)
	m.state = tuiRunning
	m.elapsed = 1000000
	m.turnTokens = 9999999

	// Precondition: the working line really wraps at this width, otherwise this
	// test would not exercise the bug.
	working := m.runningWorkingLine(false, false)
	wrappedRows := workingLineRows(working, m.width)
	if wrappedRows <= 1 {
		t.Fatalf("test setup: working line %q at width %d must wrap, got %d row(s)", working, m.width, wrappedRows)
	}

	v := m.View()
	if v.Cursor == nil {
		t.Fatal("composer cursor should be set while the composer is visible")
	}
	// The composer box starts below the viewport and the wrapped working line;
	// its top border sits one row above the textarea rows and the bottom border
	// one row below them.
	boxTop := m.viewport.Height() + wrappedRows
	boxBottom := boxTop + m.input.Height() + 2
	if v.Cursor.Y < boxTop+1 || v.Cursor.Y >= boxBottom {
		t.Fatalf("composer cursor Y=%d outside composer box rows [%d,%d): viewport=%d wrappedWorkingRows=%d inputHeight=%d",
			v.Cursor.Y, boxTop+1, boxBottom, m.viewport.Height(), wrappedRows, m.input.Height())
	}
}
