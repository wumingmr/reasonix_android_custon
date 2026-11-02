package cli

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// panelContent reports a terminal row and a logical selection independently of
// the display text. The selection still changes when a windowed list moves but
// keeps its selected item on the same terminal row.
type panelContent struct {
	text             string
	focus, selection int
}

// panelBuilder records the start of the selected row while its owner writes
// the body. MarkFocus must be called at a row boundary, before writing the row.
type panelBuilder struct {
	strings.Builder
	focusOffset, selection int
	hasFocus               bool
}

func (b *panelBuilder) MarkFocus(selected bool, selection int) {
	if selected && !b.hasFocus {
		b.focusOffset, b.selection, b.hasFocus = b.Len(), selection, true
	}
}

type panelBody struct {
	text                   string
	focusOffset, selection int
	hasFocus               bool
}

func (b panelBody) trimTrailingNewlines() panelBody {
	b.text = strings.TrimRight(b.text, "\n")
	return b
}

func (b *panelBuilder) body() panelBody {
	return panelBody{b.String(), b.focusOffset, b.selection, b.hasFocus}
}

func (b *panelBuilder) render(style lipgloss.Style, width int) panelContent {
	return b.body().render(style, width)
}

func (b panelBody) render(style lipgloss.Style, width int) panelContent {
	p := panelContent{text: style.Render(b.text), selection: b.selection}
	if b.hasFocus {
		// Measure preceding rows with the same style and terminal wrapping as
		// the actual panel. A single cell preserves the selected row's empty
		// start; no marker is inserted into or searched for in the output.
		prefix := style.Render(b.text[:b.focusOffset] + "x")
		// Exclude the placeholder row and bottom frame before terminal
		// wrapping: a manager's minimum width can exceed the terminal width.
		rows := strings.Split(prefix, "\n")
		before := len(rows) - 1 - style.GetPaddingBottom() - style.GetBorderBottomSize() - style.GetMarginBottom()
		if before > 0 {
			p.focus = strings.Count(wrapTerminalText(strings.Join(rows[:before], "\n"), max(width, 10)), "\n") + 1
		}
	}
	return p
}
