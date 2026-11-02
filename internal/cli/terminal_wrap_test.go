package cli

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestTerminalWrappingPreservesGraphemes(t *testing.T) {
	for _, width := range []int{10, 40, 80, 120} {
		for _, cluster := range []string{"1️⃣", "#️⃣", "*️⃣", "é", "👩🏽‍💻", "🇨🇳", "中文", "⚠️✅↔️🏳️‍🌈1️⃣é世"} {
			t.Run(fmt.Sprintf("%d/%s", width, cluster), func(t *testing.T) {
				plain := strings.Repeat(cluster+"X", 50)
				styled := ansi.SetHyperlink("https://example.com") + "\x1b[31m" + plain + "\x1b[m" + ansi.ResetHyperlink()
				for name, wrap := range map[string]func(string, int) string{
					"transcript": wrapTranscript, "scrollback": clampWidth, "status": wrapStatusLine,
				} {
					t.Run(name, func(t *testing.T) {
						wrapped := wrap(styled, width)
						var restored strings.Builder
						for row := range strings.SplitSeq(wrapped, "\n") {
							if got := visibleWidth(row); got > width {
								t.Fatalf("row occupies %d columns, limit %d: %q", got, width, row)
							}
							if ansi.Strip(row) != "" {
								restored.WriteString(strings.TrimRight(ansi.Strip(row), " "))
							}
							// A width assertion alone can miss a split grapheme. Compare
							// the actual decoded cells with every character in the row.
							screen := uv.NewScreenBuffer(width, 1)
							screen.Method = ansi.GraphemeWidth
							uv.NewStyledString(row).Draw(screen, screen.Bounds())
							if got, want := terminalRowText(width, 0, screen.CellAt), strings.TrimRight(ansi.Strip(row), " "); got != want {
								t.Fatalf("screen lost content: got %q, want %q", got, want)
							}
						}
						if restored.String() != plain {
							t.Fatalf("wrapped text lost or altered a cluster:\n%q\nwant %q", restored.String(), plain)
						}
					})
				}
			})
		}
	}
}

func TestWrappedTranscriptBalancesStyleAndLinkAcrossKeycaps(t *testing.T) {
	text := strings.Repeat("1️⃣X", 50)
	wrapped := wrapTranscript(ansi.SetHyperlink("https://example.com")+"\x1b[31m"+text+"\x1b[m"+ansi.ResetHyperlink(), 40)
	for row := range strings.SplitSeq(wrapped, "\n") {
		screen := uv.NewScreenBuffer(40, 1)
		screen.Method = ansi.GraphemeWidth
		uv.NewStyledString(row).Draw(screen, screen.Bounds())
		for x := range 40 {
			cell := screen.CellAt(x, 0)
			if cell == nil || cell.Content == "" || cell.Content == " " {
				continue
			}
			if cell.Link.URL != "https://example.com" || cell.Style.Fg == nil {
				t.Fatalf("wrapped glyph lost style or link: %+v", cell)
			}
		}
	}
}
