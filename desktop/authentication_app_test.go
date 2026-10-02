package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/control"
)

type desktopAuthenticationRunner struct{ calls atomic.Int32 }

func (r *desktopAuthenticationRunner) Run(context.Context, string) error {
	r.calls.Add(1)
	return nil
}

func newDesktopAuthenticationTab(t *testing.T, app *App, id string, state control.AuthenticationState) (*WorkspaceTab, *control.Controller, *desktopAuthenticationRunner) {
	t.Helper()
	runner := &desktopAuthenticationRunner{}
	ctrl := control.New(control.Options{Runner: runner, Authentication: state})
	t.Cleanup(ctrl.Close)
	tab := &WorkspaceTab{ID: id, Ctrl: ctrl, Ready: true, Scope: "global"}
	app.tabs[id] = tab
	return tab, ctrl, runner
}

func TestDesktopAuthenticationAdmissionDoesNotCreateTurn(t *testing.T) {
	app := NewApp()
	tab, ctrl, runner := newDesktopAuthenticationTab(t, app, "missing", control.AuthenticationState{
		Status: control.AuthenticationMissingCredential, ProviderName: "relay", ModelRef: "relay/chat",
	})
	app.activeTabID = tab.ID

	err := app.SubmitToTab(tab.ID, "must remain a draft")
	var authErr *control.AuthenticationError
	if !errors.As(err, &authErr) || authErr.State.Status != control.AuthenticationMissingCredential {
		t.Fatalf("SubmitToTab error = %v, want missing-credential AuthenticationError", err)
	}
	if runner.calls.Load() != 0 || ctrl.Turn() != 0 || len(ctrl.History()) != 0 {
		t.Fatalf("blocked submit changed runtime: calls=%d turn=%d history=%d", runner.calls.Load(), ctrl.Turn(), len(ctrl.History()))
	}
}

func TestRetryAuthenticationForTabIsSingleUseAndTabLocal(t *testing.T) {
	app := NewApp()
	first, firstCtrl, _ := newDesktopAuthenticationTab(t, app, "first", control.AuthenticationState{
		Status: control.AuthenticationRejected, ProviderName: "first-provider", HTTPStatus: 401,
	})
	second, secondCtrl, _ := newDesktopAuthenticationTab(t, app, "second", control.AuthenticationState{
		Status: control.AuthenticationRejected, ProviderName: "second-provider", HTTPStatus: 403,
	})
	app.activeTabID = second.ID

	state, err := app.RetryAuthenticationForTab(first.ID)
	if err != nil || !state.Ready() {
		t.Fatalf("first retry = %+v, %v", state, err)
	}
	if _, err := app.RetryAuthenticationForTab(first.ID); err == nil {
		t.Fatal("second retry was accepted without another rejection")
	}
	if got := firstCtrl.AuthenticationState(); !got.Ready() {
		t.Fatalf("first tab state = %+v, want ready for one attempt", got)
	}
	if got := secondCtrl.AuthenticationState(); got.Status != control.AuthenticationRejected || got.ProviderName != "second-provider" {
		t.Fatalf("retry leaked into second tab: %+v", got)
	}
}

func TestRetryAuthenticationRejectsMissingCredential(t *testing.T) {
	app := NewApp()
	tab, _, _ := newDesktopAuthenticationTab(t, app, "missing", control.AuthenticationState{Status: control.AuthenticationMissingCredential})
	if _, err := app.RetryAuthenticationForTab(tab.ID); err == nil {
		t.Fatal("missing credential incorrectly accepted authentication retry")
	}
}

func TestRetryAuthenticationPublishesAuthoritativeTabRefresh(t *testing.T) {
	app := NewApp()
	app.ctx = context.Background()
	tab, _, _ := newDesktopAuthenticationTab(t, app, "retry-refresh", control.AuthenticationState{Status: control.AuthenticationRejected, ProviderName: "relay", HTTPStatus: 401})
	app.activeTabID = tab.ID
	installNoopRuntimeEvents(app)
	refreshed := make(chan bool, 1)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...any) {
		if name != tabMetaRefreshEventChannel {
			return
		}
		for _, item := range app.ListTabs() {
			if item.ID == tab.ID && item.Authentication != nil && item.Authentication.Ready() {
				// 非阻塞发送：该回调跑在共享的 asyncRuntimeEmitter.run 上，
				// 阻塞发送会把后续所有 runtime 事件永久卡死。
				select {
				case refreshed <- true:
				default:
				}
				return
			}
		}
	}
	if _, err := app.RetryAuthenticationForTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not publish a refresh of authoritative authentication state")
	}

	// 回归：缓冲已满时再次触发同一事件，emitter 泵必须继续排空而不是停在
	// tabs.go 的 emit 调用点上（曾实证 chan send 卡死 7 分钟）。
	for range 3 {
		app.runtimeEvents.Emit(app.ctx, tabMetaRefreshEventChannel)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		app.runtimeEvents.mu.Lock()
		busy := app.runtimeEvents.running
		app.runtimeEvents.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("asyncRuntimeEmitter pump stayed blocked after repeated refresh events")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
