package cli

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// hardwrapTerminalText measures whole graphemes, including clusters starting
// with ASCII (keycap digits, # and *). ansi.Hardwrap's ASCII fast path counts
// the base separately from its combining suffix and underestimates these.
// Escape sequences pass through without consuming columns or being split.
func hardwrapTerminalText(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	var out strings.Builder
	var state byte
	column := 0
	for len(s) > 0 {
		seq, cells, n, next := terminalSequence(s, state)
		if cells > 0 {
			if column > 0 && column+cells > width {
				out.WriteByte('\n')
				column = 0
			}
			column += cells
		} else if seq == "\n" || seq == "\r" {
			column = 0
		}
		out.WriteString(seq)
		s, state = s[n:], next
	}
	return out.String()
}

// wrapTerminalText uses the same complete clusters as the screen buffer, then
// balances SGR and hyperlinks at each break. Running the upstream word wrapper
// first is too late: it can already have split an ASCII base from its suffix.
func wrapTerminalText(s string, width int) string {
	if width <= 0 {
		return s
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    ")
	var balanced strings.Builder
	writer := lipgloss.NewWrapWriter(&balanced)
	_, _ = writer.Write([]byte(wordwrapTerminalText(s, width))) // strings.Builder cannot fail
	_ = writer.Close()
	rows := strings.Split(balanced.String(), "\n")
	for i := range rows {
		rows[i] = padRight(rows[i], width)
	}
	return strings.Join(rows, "\n")
}

func terminalSequence(s string, state byte) (seq string, cells, n int, next byte) {
	seq, cells, n, next = ansi.DecodeSequence(s, state, nil)
	if cells > 0 && n == 1 && len(s) > 1 && s[1] >= utf8.RuneSelf {
		seq, cells = ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		n = len(seq)
	}
	return
}

func wordwrapTerminalText(s string, width int) string {
	var out, word, spaces strings.Builder
	column, wordWidth, spaceWidth := 0, 0, 0
	var state byte
	flush := func() {
		out.WriteString(spaces.String())
		out.WriteString(word.String())
		column += spaceWidth + wordWidth
		spaces.Reset()
		word.Reset()
		spaceWidth, wordWidth = 0, 0
	}
	newline := func() {
		out.WriteByte('\n')
		column = 0
		spaces.Reset()
		spaceWidth = 0
	}
	for len(s) > 0 {
		seq, cells, n, next := terminalSequence(s, state)
		r, _ := utf8.DecodeRuneInString(seq)
		switch {
		case seq == "\n":
			flush()
			newline()
		case cells > 0 && unicode.IsSpace(r) && r != '\u00a0':
			if word.Len() > 0 {
				flush()
			}
			spaces.WriteString(seq)
			spaceWidth += cells
		case cells > 0:
			if wordWidth+cells > width {
				flush()
			}
			word.WriteString(seq)
			wordWidth += cells
			if column+spaceWidth+wordWidth > width {
				newline()
			}
			if seq == "-" || wordWidth == width {
				flush()
			}
		default:
			word.WriteString(seq)
		}
		s, state = s[n:], next
	}
	flush()
	// Indentation and trailing spaces are content too. Bound those without
	// discarding them or asking the terminal to perform an implicit wrap.
	return hardwrapTerminalText(out.String(), width)
}
