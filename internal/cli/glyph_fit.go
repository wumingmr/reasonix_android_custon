package cli

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/width"
)

// glyphFit swaps each rune the console draws wider than the layout counted for
// a stand-in both agree on, so a row laid out to the terminal width stays that
// wide on screen. The console's own measurement is the only judge of "wider".
type glyphFit struct {
	cells   func(rune) int  // columns the console advances for r; 0 when unmeasured
	bestFit func(rune) rune // single-byte best-fit stand-in for r, 0 when none
	chosen  map[rune]rune   // per-rune verdict; r maps to itself when it already fits
}

func newGlyphFit(cells func(rune) int, bestFit func(rune) rune) *glyphFit {
	return &glyphFit{cells: cells, bestFit: bestFit, chosen: map[rune]rune{}}
}

// apply rewrites the printable text of a styled frame; escape sequences,
// including OSC payloads such as hyperlink targets, pass through untouched.
func (g *glyphFit) apply(s string) string {
	if g == nil || !g.needed(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	var state byte
	for len(s) > 0 {
		seq, w, n, next := ansi.DecodeSequence(s, state, nil)
		state = next
		if w == 0 {
			b.WriteString(seq)
		} else {
			for _, r := range seq {
				b.WriteRune(g.fit(r))
			}
		}
		s = s[n:]
	}
	return b.String()
}

func (g *glyphFit) needed(s string) bool {
	for _, r := range s {
		if r >= utf8.RuneSelf && g.fit(r) != r {
			return true
		}
	}
	return false
}

func (g *glyphFit) fit(r rune) rune {
	if r < utf8.RuneSelf {
		return r
	}
	if c, ok := g.chosen[r]; ok {
		return c
	}
	c := g.choose(r)
	g.chosen[r] = c
	return c
}

// choose prefers Unicode's own halfwidth variant, then the OS best-fit mapping;
// a candidate counts only when the console measures it at the counted width.
func (g *glyphFit) choose(r rune) rune {
	counted := ansi.StringWidth(string(r))
	if counted != 1 || g.cells(r) <= counted {
		return r
	}
	for _, c := range []rune{width.LookupRune(r).Narrow(), g.bestFit(r)} {
		if c > 0 && c != r && ansi.StringWidth(string(c)) == counted && g.cells(c) == counted {
			return c
		}
	}
	return r
}
