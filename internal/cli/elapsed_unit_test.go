package cli

import (
	"strings"
	"testing"
)

// 冻结 spinner 后,阶段行的秒数是唯一还在每秒变化的东西;在 Termux 上每次
// 变化都要重绘,并与读者的上滑争抢。static 必须隐含"按分钟显示"。
func TestElapsedUnitFollowsFrozenSpinner(t *testing.T) {
	m := newTestChatTUI()
	m.elapsed = 125 // 2 分 5 秒

	if got := m.elapsedUnitLabel(); got != "125s" {
		t.Fatalf("默认应为秒: got %q", got)
	}

	m.spinnerStatic = true
	if got := m.elapsedUnitLabel(); got != "2m" {
		t.Fatalf("static 下应为分钟: got %q, want 2m", got)
	}
	if m.elapsed != 125 {
		t.Fatalf("内部 elapsed 必须保持秒(供 watchdog 用), got %d", m.elapsed)
	}
}

func TestElapsedUnitExplicitCoarse(t *testing.T) {
	m := newTestChatTUI()
	m.coarseElapsed = true
	m.elapsed = 59
	if got := m.elapsedUnitLabel(); got != "0m" {
		t.Fatalf("59 秒按分钟应为 0m, got %q", got)
	}
	m.elapsed = 60
	if got := m.elapsedUnitLabel(); got != "1m" {
		t.Fatalf("60 秒应为 1m, got %q", got)
	}
}

// 关键回归:static + 1Hz 心跳下,状态行必须长时间保持不变 —— 这正是
// Termux 滚动能停稳的前提。
func TestFrozenSpinnerLineHoldsStill(t *testing.T) {
	build := func(static bool) chatTUI {
		m := newTestChatTUI()
		m.width = 40
		m.state = tuiRunning
		m.spinnerStatic = static
		m.elapsed = 5
		return m
	}

	animatedLine := func(m chatTUI) string { return m.runningWorkingLine(false, false) }
	staticLine := func(m chatTUI) string { return m.runningWorkingLine(false, false) }

	// 真实场景走 phaseLabel 分支(那一行是 "⣟ 检查中 · 5s · ↓44"),
	// 而不是无 phaseLabel 时的 thinking 模板。
	a := build(false)
	a.readStatusLabel = "检查中"
	sa := animatedLine(a)
	s := build(true)
	s.readStatusLabel = "检查中"
	ss := staticLine(s)

	// 模拟 12 秒过去(12 次 elapsedTick)
	aChanged, sChanged := 0, 0
	for sec := 6; sec <= 18; sec++ {
		a.elapsed = sec
		s.elapsed = sec
		if animatedLine(a) != sa {
			aChanged++
		}
		if staticLine(s) != ss {
			sChanged++
		}
	}
	t.Logf("13 秒内: animated 状态行变化 %d 次;static 变化 %d 次", aChanged, sChanged)

	if aChanged == 0 {
		t.Fatal("对照失效:animated 行本应随秒数变化")
	}
	if sChanged > 1 {
		t.Fatalf("static 状态下状态行仍变化 %d 次(跨分钟时最多 1 次)", sChanged)
	}
	if strings.Contains(ss, "5s") {
		t.Fatalf("static 行不应再按秒显示: %q", ss)
	}
}
