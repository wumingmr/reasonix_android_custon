package cli

import (
	"testing"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

// Termux 用 nativeScrollback（不进 alt-screen）换取点击输入框唤起软键盘。
// 代价是工作 spinner 的默认 10 FPS 会在手机屏幕上把钉住的底部块每秒重写
// 10 次，与仍在追加的流式输出竞态，导致上滑查看历史时视图被反复拽回。
// Termux 下必须降到 3 FPS;其他终端保持默认速率。
func TestTermuxSpinnerRateIsReduced(t *testing.T) {
	orig := detectTermuxTerminal
	t.Cleanup(func() { detectTermuxTerminal = orig })

	ctrl := newOwnedTestController(t, control.Options{})

	detectTermuxTerminal = func() bool { return true }
	termux := newChatTUI(ctrl, "", make(chan event.Event, 1), 24)
	if !termux.nativeScrollback {
		t.Fatal("precondition: Termux 应启用 nativeScrollback")
	}
	if got := termux.spinner.Spinner.FPS; got != termuxSpinnerFPS {
		t.Fatalf("Termux spinner FPS = %v, want %v", got, termuxSpinnerFPS)
	}
	if termuxSpinnerFPS <= spinnerDefaultFPS {
		t.Fatalf("termuxSpinnerFPS=%v 未低于默认 %v(间隔需更大才算降频)", termuxSpinnerFPS, spinnerDefaultFPS)
	}

	detectTermuxTerminal = func() bool { return false }
	other := newChatTUI(ctrl, "", make(chan event.Event, 1), 24)
	if other.nativeScrollback {
		t.Fatal("precondition: 非 Termux 不应启用 nativeScrollback")
	}
	if got := other.spinner.Spinner.FPS; got == termuxSpinnerFPS {
		t.Fatal("非 Termux 终端不应被降频(alt-screen 无此竞态)")
	}
	if got := other.spinner.Spinner.FPS; got != spinnerDefaultFPS {
		t.Fatalf("非 Termux spinner FPS = %v, want 默认 %v", got, spinnerDefaultFPS)
	}
}
