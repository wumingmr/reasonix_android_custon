package cli

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"reasonix/internal/event"
)

// TestRenderTodoPanelIsFlat proves retired hierarchy fields do not affect the
// pinned task panel.
func TestRenderTodoPanelIsFlat(t *testing.T) {
	m := newTestChatTUI()
	m.width = 60
	m.todos = []event.Todo{
		{Content: "Phase A", Status: "in_progress"},
		{Content: "sub one", Status: "pending"},
	}

	out := ansi.Strip(m.renderTodoPanel())
	if !strings.Contains(out, "Phase A") {
		t.Fatalf("panel missing phase:\n%s", out)
	}
	if !strings.Contains(out, "  ○ sub one") {
		t.Fatalf("todo should render at the same flat depth:\n%s", out)
	}
}

func TestRenderTodoPanelScrollsToInProgressTodo(t *testing.T) {
	m := newTestChatTUI()
	m.width = 72
	m.todos = []event.Todo{
		{Content: "Item 01", Status: "completed"}, {Content: "Item 02", Status: "completed"},
		{Content: "Item 03", Status: "completed"}, {Content: "Item 04", Status: "completed"},
		{Content: "Item 05", Status: "completed"}, {Content: "Item 06", Status: "completed"},
		{Content: "Item 07", Status: "completed"}, {Content: "Item 08", Status: "completed"},
		{Content: "Item 09", Status: "in_progress"}, {Content: "Item 10", Status: "pending"},
	}

	out := ansi.Strip(m.renderTodoPanel())
	if !strings.Contains(out, "Item 09") {
		t.Fatalf("panel should keep the in-progress todo visible:\n%s", out)
	}
	if strings.Contains(out, "Item 01") {
		t.Fatalf("panel should window around the active todo instead of pinning the first rows:\n%s", out)
	}
}

func TestRenderTodoPanelKeepsCompletedListVisible(t *testing.T) {
	m := newTestChatTUI()
	m.width = 60
	m.todos = []event.Todo{{Content: "Verified", Status: "completed"}}
	if out := ansi.Strip(m.renderTodoPanel()); !strings.Contains(out, "1/1") || !strings.Contains(out, "Verified") {
		t.Fatalf("completed current-turn list must remain inspectable:\n%s", out)
	}
}
