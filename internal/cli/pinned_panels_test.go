package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"reasonix/internal/event"
	"reasonix/internal/i18n"
)

func longPanelTestTUI(t *testing.T) chatTUI {
	t.Helper()
	m := newTerminalScrollTUI(t, 80)
	for i := range m.todos {
		m.todos[i].Content = fmt.Sprintf("TODO %d %s END-%d", i, strings.Repeat("排查长路径和详细的执行步骤 ", 8), i)
	}
	m.input.SetValue("draft")
	return updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
}

func longPanelChooser() *chooser {
	options := make([]event.AskOption, 5)
	for i := range options {
		options[i] = event.AskOption{Label: fmt.Sprintf("OPTION-%d", i), Description: strings.Repeat("详细说明和中文长路径 ", 20) + fmt.Sprintf(" DESC-END-%d", i)}
	}
	return newChooser(event.Ask{ID: "long", Questions: []event.AskQuestion{{ID: "one", Prompt: "选择后续步骤", Options: options}}})
}

func TestPinnedPanelsKeepFooterOnTerminal(t *testing.T) {
	defer restoreThemeForTest(activeColorProfile, activeCLITheme)
	defer i18n.DetectLanguage("en")
	activeColorProfile = colorprofile.TrueColor
	configureCLITheme("dark")
	i18n.DetectLanguage("zh")
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		t.Run(fmt.Sprintf("method=%d", method), func(t *testing.T) {
			m := longPanelTestTUI(t)
			replay := newPanelTerminalReplay(t, method)
			for _, size := range [][2]int{{80, 24}, {60, 18}, {120, 40}, {80, 24}} {
				m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				replay.check(t, m)
				m.chooser = longPanelChooser()
				m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				for range 5 {
					replay.check(t, m)
					m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
				}
				m.chooser = nil
				m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				replay.check(t, m)
			}
		})
	}
}

func TestPinnedPanelScrollPreservesTextAndInput(t *testing.T) {
	m := longPanelTestTUI(t)
	input, cursor, transcript := m.input.Value(), m.input.Cursor(), m.viewport.YOffset()
	var seen strings.Builder
	for range 30 {
		seen.WriteString(ansi.Strip(m.View().Content))
		seen.WriteByte('\n')
		m = updateComposerMouseTestTUI(t, m, tea.MouseWheelMsg{X: 2, Y: m.viewport.Height() + 1, Button: tea.MouseWheelDown})
	}
	for i := range 4 {
		if !strings.Contains(seen.String(), fmt.Sprintf("END-%d", i)) {
			t.Fatalf("todo %d could not be read by scrolling", i)
		}
	}
	if m.input.Value() != input || *m.input.Cursor() != *cursor || m.viewport.YOffset() != transcript {
		t.Fatal("panel scroll moved the draft, caret, or transcript")
	}
	if m.pinnedScroll.offset == 0 {
		t.Fatal("long panel never scrolled")
	}
	before := m.pinnedScroll.offset
	m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: tea.ModAlt})
	if m.pinnedScroll.offset >= before || m.input.Value() != input {
		t.Fatal("panel page-up did not scroll independently of the input")
	}
	before = m.pinnedScroll.offset
	m = updateComposerMouseTestTUI(t, m, tea.MouseWheelMsg{X: 2, Y: 0, Button: tea.MouseWheelUp})
	if m.pinnedScroll.offset != before || m.viewport.YOffset() >= transcript {
		t.Fatal("wheel over transcript did not stay in the transcript")
	}
}

func TestPinnedPanelFollowsChooserSelectionAndFreeText(t *testing.T) {
	m := longPanelTestTUI(t)
	m.chooser = longPanelChooser()
	m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 5 {
		if !strings.Contains(ansi.Strip(m.View().Content), fmt.Sprintf("❯ %d. OPTION-%d", i+1, i)) {
			t.Fatalf("selected option %d is off screen", i)
		}
		m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	// Enter on the free-text row shrinks the panel viewport to make room for
	// the live textarea. The same layout must position its real caret.
	m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.hideComposer() {
		t.Fatal("chooser free-text input remained hidden")
	}
	x, y, ok := m.composerOrigin()
	if !ok || x < 0 || y < 0 || y+m.input.Height() >= m.height {
		t.Fatalf("composer is outside the terminal: %d,%d", x, y)
	}
	m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.input.Value() != "x" {
		t.Fatal("free-text composer cannot receive input")
	}
}

func TestPinnedPanelNativeScrollbackIsBoundedAndKeyboardScrollable(t *testing.T) {
	m := longPanelTestTUI(t)
	m.nativeScrollback = true
	m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View()
	if view.AltScreen || strings.Count(view.Content, "\n")+1 > m.height {
		t.Fatal("native scrollback bottom frame outgrew the terminal")
	}
	before := m.pinnedScroll.offset
	m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: tea.ModAlt})
	if m.pinnedScroll.offset <= before {
		t.Fatal("native scrollback cannot scroll the long panel without a mouse")
	}
}

func TestPinnedPanelRevealsSelectionAfterHeightShrink(t *testing.T) {
	m := longPanelTestTUI(t)
	m.chooser = longPanelChooser()
	m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	for range 3 {
		m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 18})
	if !strings.Contains(ansi.Strip(m.View().Content), "❯ 4. OPTION-3") {
		t.Fatal("height-only shrink hid the selected option")
	}
	var seen strings.Builder
	for range 25 {
		seen.WriteString(ansi.Strip(m.View().Content))
		m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: tea.ModAlt})
	}
	if !strings.Contains(seen.String(), "DESC-END-4") {
		t.Fatal("long option description was discarded instead of scrollable")
	}
	m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if !strings.Contains(ansi.Strip(m.View().Content), "❯ 3. OPTION-2") {
		t.Fatal("keyboard navigation did not reveal selection after manual scrolling")
	}
}

func TestPinnedPanelFollowsElicitationFieldsAndSubmit(t *testing.T) {
	m := longPanelTestTUI(t)
	m.elicit = &elicitCard{message: strings.Repeat("Long question ", 30), fields: make([]elicitField, 8), values: make([]string, 8), bools: make([]bool, 8)}
	for i := range m.elicit.fields {
		m.elicit.fields[i] = elicitField{Label: fmt.Sprintf("FIELD-%d", i), Type: "boolean"}
	}
	m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 18})
	for i := range 8 {
		if !strings.Contains(ansi.Strip(m.View().Content), fmt.Sprintf("› FIELD-%d", i)) {
			t.Fatalf("elicitation field %d is off screen", i)
		}
		m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "  ["+i18n.M.ElicitSubmit+"]") {
		t.Fatal("elicitation Submit is off screen")
	}
}

func TestPinnedPanelIgnoresTodoSelectionPrefixes(t *testing.T) {
	for _, marker := range []string{"› ", "❯ ", "▶ "} {
		t.Run(marker, func(t *testing.T) {
			m := longPanelTestTUI(t)
			// A continuation line is displayed without the todo status icon.
			// Its content must never become the panel's selected row.
			m.todos[0].Content = marker + "ordinary todo\n" + marker + "not selected\n" + strings.Repeat("detail\n", 30)
			m.todos[3].Content = "ACTIVE-TASK"
			m.todos[3].Status = "in_progress"
			m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 18})
			p := m.pinnedPanelView()
			if !strings.Contains(ansi.Strip(p.lines[p.focus]), "ACTIVE-TASK") || !strings.Contains(ansi.Strip(m.View().Content), "ACTIVE-TASK") {
				t.Fatalf("todo text stole focus from the active task:\n%s", ansi.Strip(m.View().Content))
			}
			// Wording changes must not cancel a deliberate manual scroll.
			m.scrollPinnedPanel(-p.offset)
			m.todos[3].Content = "RENAMED-TASK"
			if m.pinnedPanelView().offset != 0 {
				t.Fatal("renaming the active task reset manual scrolling")
			}
		})
	}
}

func TestPinnedPanelFollowsWindowedSelectionAtSameRow(t *testing.T) {
	m := longPanelTestTUI(t)
	m.todos = nil
	m.quickPick = &quickPicker{title: "Models", selected: 8}
	for i := range 24 {
		m.quickPick.items = append(m.quickPick.items, quickPickerItem{ID: fmt.Sprint(i), Label: "Same label"})
	}
	m = updateComposerMouseTestTUI(t, m, tea.WindowSizeMsg{Width: 80, Height: 13})
	before := m.pinnedPanelContent().focus
	m.scrollPinnedPanel(-1000)
	m = updateComposerMouseTestTUI(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	p := m.pinnedPanelView()
	if p.focus != before {
		t.Fatal("fixture did not keep the selected item on the same row")
	}
	if !strings.Contains(ansi.Strip(p.render(m.width)), "❯ 10. Same label") {
		t.Fatal("selection change at the same row did not restore focus after manual scrolling")
	}
}

type panelTerminalReplay struct {
	output   bytes.Buffer
	renderer *uv.TerminalRenderer
	screen   uv.ScreenBuffer
	terminal *vt.Emulator
}

func newPanelTerminalReplay(t *testing.T, method ansi.Method) *panelTerminalReplay {
	t.Helper()
	r := &panelTerminalReplay{screen: uv.NewScreenBuffer(80, 24), terminal: vt.NewEmulator(80, 24)}
	t.Cleanup(func() { _ = r.terminal.Close() })
	r.screen.Method = method
	r.renderer = uv.NewTerminalRenderer(&r.output, []string{"TERM=xterm-ghostty", "COLORTERM=truecolor"})
	r.renderer.SetFullscreen(true)
	r.renderer.SetColorProfile(colorprofile.TrueColor)
	r.renderer.SetScrollOptim(true)
	r.renderer.SetRelativeCursor(false)
	r.renderer.SetMapNewline(false)
	r.renderer.Resize(80, 24)
	r.renderer.Erase()
	_, _ = r.terminal.WriteString(ansi.SetModeAltScreenSaveCursor)
	if method == ansi.GraphemeWidth {
		_, _ = r.terminal.WriteString(ansi.SetModeUnicodeCore)
	}
	return r
}

func (r *panelTerminalReplay) check(t *testing.T, m chatTUI) {
	t.Helper()
	view := m.View()
	if rows := strings.Count(view.Content, "\n") + 1; rows != m.height {
		t.Fatalf("%dx%d terminal has %d frame rows", m.width, m.height, rows)
	}
	r.terminal.Resize(m.width, m.height)
	r.renderer.Resize(m.width, m.height)
	r.screen.Resize(m.width, m.height)
	r.screen.Clear()
	uv.NewStyledString(view.Content).Draw(r.screen, r.screen.Bounds())
	r.renderer.Render(r.screen.RenderBuffer)
	if view.Cursor != nil {
		r.renderer.MoveTo(view.Cursor.X, view.Cursor.Y)
	}
	if err := r.renderer.Flush(); err != nil {
		t.Fatal(err)
	}
	_, _ = r.terminal.Write(r.output.Bytes())
	r.output.Reset()
	for y := m.transcriptHeight(); y < m.height; y++ {
		got, want := terminalRowText(m.width, y, r.terminal.CellAt), terminalRowText(m.width, y, r.screen.CellAt)
		if got != want {
			t.Fatalf("pinned row %d drifted:\ngot %q\nwant %q", y, got, want)
		}
	}
	if !strings.Contains(terminalRowText(m.width, m.height-1, r.terminal.CellAt), "166.6K") {
		t.Fatal("context status was displaced from the terminal")
	}
}
