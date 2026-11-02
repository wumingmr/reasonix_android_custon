package cli

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

type scrollFooterAPI struct{ control.SessionAPI }

func (scrollFooterAPI) LastUsage() *provider.Usage {
	return &provider.Usage{PromptTokens: 162500, TotalTokens: 166600, CacheHitTokens: 160000, CacheMissTokens: 2500, CompletionTokens: 4100}
}

func (scrollFooterAPI) SessionCache() (int, int) { return 160000, 2500 }

func (scrollFooterAPI) ContextSnapshot() (int, int) { return 166600, 1000000 }

func newTerminalScrollTUI(t *testing.T, width int) chatTUI {
	t.Helper()
	m := newComposerMouseTestTUI(t, width, 30)
	m.label = "deepseek-v4-flash"
	m.ctrl.SetAutoApproveTools(true)
	m.ctrl = scrollFooterAPI{m.ctrl}
	m.todos = []event.Todo{
		{Content: "盘点 reverse 各实体 sprites 目录与对应的渲染绘制函数", Status: "completed"},
		{Content: "阅读原作实体脚本，确认贴图尺寸 / 染色 / 绘制来源", Status: "completed"},
		{Content: "扩展 devtools/port_assets.py：场景精灵清单 + 引用式漏洞检查", Status: "completed"},
		{Content: "改 render 层：draw_cores / draw_build_points 改用贴图", Status: "completed"},
	}
	m = updateComposerMouseTestTUI(t, m, agentEventMsg(event.Event{Kind: event.Usage, Usage: m.ctrl.LastUsage()}))
	// Vary wrapping and blank rows deterministically, including clusters whose
	// terminal width can exceed the legacy wcwidth measurement at the margin.
	rng := rand.New(rand.NewPCG(1, 2))
	samples := []string{
		"阅读原作实体脚本 · render_layer ",
		"✅ 检查 👩🏽‍💻 测试 🇨🇳 路径",
		"⚠️ ✅ ↔️ 🏳️‍🌈 1️⃣ é 世",
		"some ASCII content mixed with 中文",
	}
	for i := range 80 {
		text := fmt.Sprintf("%03d %s\n%s", i, strings.Repeat(samples[i%len(samples)], rng.IntN(10)+1), strings.Repeat("\n", rng.IntN(4)))
		m = updateComposerMouseTestTUI(t, m, agentEventMsg(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: text}))
	}
	return m
}

// A correct View is insufficient: a width disagreement can wrap the physical
// terminal without moving the renderer's cursor model. Later relative moves
// then overwrite or duplicate the pinned footer. Replay the actual output into
// a terminal, preserving both screens between frames, to catch that drift.
func TestTerminalScrollPreservesFooter(t *testing.T) {
	defer restoreThemeForTest(activeColorProfile, activeCLITheme)
	defer i18n.DetectLanguage("en")
	activeColorProfile = colorprofile.TrueColor
	configureCLITheme("dark")
	i18n.DetectLanguage("zh")
	for _, width := range []int{80, 120} {
		for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
			t.Run(fmt.Sprintf("width=%d/method=%d", width, method), func(t *testing.T) {
				checkTerminalScrollFooter(t, width, method)
			})
		}
	}
}

func checkTerminalScrollFooter(t *testing.T, width int, method ansi.Method) {
	t.Helper()
	const height = 30
	m := newTerminalScrollTUI(t, width)
	var output bytes.Buffer
	renderer := uv.NewTerminalRenderer(&output, []string{"TERM=xterm-ghostty", "COLORTERM=truecolor"})
	renderer.SetColorProfile(colorprofile.TrueColor)
	renderer.SetFullscreen(true)
	renderer.SetScrollOptim(true)
	renderer.SetRelativeCursor(false)
	renderer.SetMapNewline(false) // Bubble Tea's raw PTY disables OPOST.
	renderer.SetBackspace(true)
	renderer.SetTabStops(width)
	renderer.Resize(width, height)
	renderer.Erase()
	screen := uv.NewScreenBuffer(width, height)
	// Match Bubble Tea's negotiated screen width method. Its renderer retains
	// its default width method, including after a mode 2027 response.
	screen.Method = method
	terminal := vt.NewEmulator(width, height)
	t.Cleanup(func() { _ = terminal.Close() })
	_, _ = terminal.WriteString(ansi.SetModeAltScreenSaveCursor)
	if method == ansi.GraphemeWidth {
		_, _ = terminal.WriteString(ansi.SetModeUnicodeCore)
	}
	for frame := range 161 {
		if frame > 0 {
			button := tea.MouseWheelUp
			if (frame-1)%80 >= 40 {
				button = tea.MouseWheelDown
			}
			m = updateComposerMouseTestTUI(t, m, tea.MouseWheelMsg{Button: button})
		}
		view := m.View()
		screen.Clear()
		uv.NewStyledString(view.Content).Draw(screen, screen.Bounds())
		renderer.Render(screen.RenderBuffer)
		if view.Cursor != nil {
			renderer.MoveTo(view.Cursor.X, view.Cursor.Y)
		}
		if err := renderer.Flush(); err != nil {
			t.Fatal(err)
		}
		if _, err := terminal.Write(output.Bytes()); err != nil {
			t.Fatal(err)
		}
		// Clusters clipped at the transcript's right margin may differ between
		// width modes, but must never move the todo, composer, or status rows.
		for y := m.transcriptHeight(); y < height; y++ {
			got := terminalRowText(width, y, terminal.CellAt)
			want := terminalRowText(width, y, screen.CellAt)
			if got != want {
				t.Fatalf("frame %d, pinned row %d:\ngot  %q\nwant %q", frame, y, got, want)
			}
		}
		output.Reset()
	}
}

func terminalRowText(width, y int, cellAt func(int, int) *uv.Cell) string {
	var text strings.Builder
	for x := range width {
		if cell := cellAt(x, y); cell != nil {
			text.WriteString(cell.Content)
		} else {
			text.WriteByte(' ')
		}
	}
	return strings.TrimRight(text.String(), " ")
}
