package main

import (
	"reasonix/internal/control"
	"testing"
)

func TestRemoteModelConfirmationExpiresOnReconnectAndNewRuntime(t *testing.T) {
	a := NewApp()
	tab := &remoteTab{gen: 1, capabilities: map[string]bool{"model-application-v1": true}}
	tab.routing.currentPath = "session"
	a.remoteTabs = map[string]*remoteTab{"tab": tab}
	d := &ModelApplicationDetails{RuntimeIdentity: "runtime", AppliedRevision: "a", DesiredRevision: "b", ConfirmationToken: "untrusted-source-token"}
	bound := bindRemoteModelConfirmation(tab, 1, "session", d)
	if bound.ConfirmationToken == d.ConfirmationToken {
		t.Fatal("source supplied the host confirmation")
	}
	choice := control.ModelApplicationChoice{Mode: "applied_once", ExpectedRuntimeIdentity: "runtime", ExpectedAppliedRevision: "a", ExpectedDesiredRevision: "b", ConfirmationToken: bound.ConfirmationToken}
	if err := a.validateRemoteModelConfirmation("tab", choice); err != nil {
		t.Fatal(err)
	}
	tab.gen = 2
	if err := a.validateRemoteModelConfirmation("tab", choice); err == nil {
		t.Fatal("reconnect accepted old confirmation")
	}
	bindRemoteModelConfirmation(tab, 2, "session", d)
	if err := a.validateRemoteModelConfirmation("tab", choice); err == nil {
		t.Fatal("fresh status revived old confirmation")
	}
	choice.ConfirmationToken = tab.settings.details.ConfirmationToken
	if err := a.validateRemoteModelConfirmation("tab", choice); err != nil {
		t.Fatal(err)
	}
	d.RuntimeIdentity = "new-runtime"
	bindRemoteModelConfirmation(tab, 2, "session", d)
	if err := a.validateRemoteModelConfirmation("tab", choice); err == nil {
		t.Fatal("replacement accepted old confirmation")
	}
}
