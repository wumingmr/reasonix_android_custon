package cli

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/skill"
)

func TestPanelFocusSurvivesWrappingAndPresentation(t *testing.T) {
	for _, width := range []int{20, 40, 80} {
		for _, style := range []lipgloss.Style{lipgloss.NewStyle(), choicePanelStyle.Width(40), managerContentPanelStyle(40)} {
			for _, marker := range []string{"", "› ", "❯ ", "任意符号 "} {
				t.Run(fmt.Sprintf("width=%d/border=%d/marker=%q", width, style.GetBorderBottomSize(), marker), func(t *testing.T) {
					var b panelBuilder
					b.WriteString("› misleading text\n" + strings.Repeat("中文 é 1️⃣ text ", 12) + "\n\n")
					b.MarkFocus(true, 7)
					b.WriteString(accent(marker+"TARGET") + "\n" + strings.Repeat("tail\n", 5))
					p := b.render(style, width)
					rows := strings.Split(wrapTerminalText(p.text, width), "\n")
					if p.focus < 0 || p.focus >= len(rows) || !strings.Contains(ansi.Strip(rows[p.focus]), "TARGET") {
						t.Fatalf("focus %d missed selected row:\n%s", p.focus, ansi.Strip(strings.Join(rows, "\n")))
					}
					if p.selection != 7 {
						t.Fatalf("lost logical selection: %d", p.selection)
					}
					if p.text != style.Render(b.String()) {
						t.Fatal("focus metadata changed the displayed content")
					}
				})
			}
		}
	}
}

func TestNativeManagerReportsSelectedRow(t *testing.T) {
	for _, width := range []int{20, 40, 80} {
		for _, kind := range []string{"mcp-list", "mcp-confirm", "skills"} {
			t.Run(fmt.Sprintf("%s/%d", kind, width), func(t *testing.T) {
				m := chatTUI{width: width}
				want := "TARGET"
				switch kind {
				case "mcp-list":
					m.mcp = &mcpManager{sel: 1, snapshot: mcpSnapshot{servers: []mcpServerView{{Name: "Other"}, {Name: want}}}}
				case "mcp-confirm":
					m.mcp = &mcpManager{stage: mcpStageConfirmRemove, confirm: 1, snapshot: mcpSnapshot{servers: []mcpServerView{{Name: strings.Repeat("Long name ", 10)}}}}
					want = "Cancel"
				case "skills":
					m.skillPick = &skillPicker{mode: pickerSkills, sel: 1, skills: []skill.Skill{{Name: "Other", Scope: skill.ScopeProject}, {Name: want, Scope: skill.ScopeGlobal}}}
				}
				p := m.renderMainManagerPanel()
				rows := strings.Split(wrapTerminalText(p.text, width), "\n")
				if p.focus < 0 || p.focus >= len(rows) || !strings.Contains(ansi.Strip(rows[p.focus]), want) {
					t.Fatalf("focus %d missed %q:\n%s", p.focus, want, ansi.Strip(strings.Join(rows, "\n")))
				}
			})
		}
	}
}
