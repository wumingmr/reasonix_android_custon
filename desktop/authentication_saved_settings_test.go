package main

import (
	"context"
	"os"
	"reasonix/internal/config"
	"testing"
)

func TestSavedCredentialAllowsAdmissionThroughPendingMetadata(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(config.ReasonixHomeDir(), 0700); err != nil {
		t.Fatal(err)
	}
	body := "default_model='relay/chat'\n[[providers]]\nname='relay'\nkind='openai'\nbase_url='https://relay.invalid/v1'\nmodel='chat'\napi_key_env='MISSING_REVIEW_KEY'\n[desktop]\nprovider_access=['relay']\n"
	if err := os.WriteFile(config.UserConfigPath(), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	t.Cleanup(app.closeSessionServices)
	tab := modelSettingsBootTab(t, app, "review", t.TempDir(), "relay/chat")
	original := tab.Ctrl
	t.Cleanup(original.Close)
	app.activeTabID = tab.ID
	before := app.tabMeta(tab, true)
	if before.Authentication == nil || before.Authentication.Ready() {
		t.Fatal("expected missing-key fixture")
	}
	result, err := config.CommitConnectionCredential(config.ConnectionCredentialRequest{RequestID: "review-save", ConfigPath: config.UserConfigPath(), ProviderNames: []string{"relay"}, Key: "fixture-key"})
	if err != nil || !result.Persisted {
		t.Fatalf("save: %+v %v", result, err)
	}
	app.refreshTabMetaExtras(tab)
	application := app.GetModelSettingsApplication()
	meta := app.tabMeta(tab, true)
	if application.Application != "pending" || meta.Authentication == nil || meta.Authentication.Ready() {
		t.Fatalf("unexpected state: application=%s auth=%+v", application.Application, meta.Authentication)
	}
	if !meta.ModelSettingsPending {
		t.Fatal("saved settings did not expose pending admission")
	}
	if len(tab.Ctrl.History()) != len(original.History()) {
		t.Fatal("metadata refresh created a turn")
	}
	admission, _, err := app.beginTabTurn(tab.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	admission.abort()
	if fresh := app.tabMeta(tab, true); fresh.Authentication == nil || !fresh.Authentication.Ready() {
		t.Fatalf("backend admission did not recover: %+v", fresh.Authentication)
	}
	if fresh := app.tabMeta(tab, true); fresh.ModelSettingsPending {
		t.Fatal("replacement inherited stale pending metadata")
	}
}
