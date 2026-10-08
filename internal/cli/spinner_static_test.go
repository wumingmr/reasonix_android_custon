package cli

import (
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

// ui.spinner = "static" 必须真正停止动画重绘。spinner.Update 会返回下一次
// tick 的命令,不消费它等于把重绘循环原样留下 —— 而 Termux 上正是这个循环
// 让上滑查看历史无法停稳。
func TestSpinnerStaticStopsRedrawLoop(t *testing.T) {
	countChangedFrames := func(t *testing.T, static bool) int {
		t.Helper()
		ctrl := newOwnedTestController(t, control.Options{})
		m := newChatTUI(ctrl, "", make(chan event.Event, 64), 24)
		m.state = tuiRunning
		if static {
			cfg := &config.Config{}
			cfg.UI.Spinner = "static"
			m.cfg = cfg
			m.spinnerStatic = true
		}
		if got := m.spinnerIsStatic(); got != static {
			t.Fatalf("spinnerIsStatic() = %v, want %v", got, static)
		}
		for i := 0; i < 30; i++ {
			m.ingestEvent(event.Event{Kind: event.Text, Text: "内容行"})
		}
		m.transcriptDirty = true
		m0, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
		m = m0.(chatTUI)
		if static {
			m.cfg = &config.Config{}
			m.cfg.UI.Spinner = "static"
			m.spinnerStatic = true
		}

		prev := m.View().Content
		changed := 0
		for i := 0; i < 20; i++ {
			m0, _ = m.Update(spinner.TickMsg{})
			m = m0.(chatTUI)
			if static {
				m.cfg = &config.Config{}
				m.cfg.UI.Spinner = "static"
				m.spinnerStatic = true
			}
			cur := m.View().Content
			if cur != prev {
				changed++
			}
			prev = cur
		}
		return changed
	}

	animated := countChangedFrames(t, false)
	static := countChangedFrames(t, true)
	t.Logf("animated %d/20 帧变化;static %d/20 帧变化", animated, static)
	if animated == 0 {
		t.Fatal("对照失效:animated 模式本应因 spinner 变化而重绘")
	}
	if static != 0 {
		t.Fatalf("static spinner 仍在触发重绘(%d/20 帧变化)", static)
	}
}
