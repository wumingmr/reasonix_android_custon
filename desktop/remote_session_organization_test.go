package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/config"
)

func TestRemoteSessionOrganizationReadDoesNotStartServeOrRewriteConfig(t *testing.T) {
	app, kernel := remoteOrganizationFixture(t)
	before, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"host-a", "host-b"} {
		got, err := app.GetSessionOrganization(SessionOrganizationWorkspace{HostID: host, Scope: "project", WorkspaceRoot: "/same/workspace"})
		if err != nil {
			t.Fatal(err)
		}
		want := "ref\x00" + host + "\x00same-session"
		if len(got.Groups) != 1 || !reflect.DeepEqual(got.Groups[0].SessionKeys, []string{want}) {
			t.Fatalf("%s organization=%#v", host, got)
		}
	}
	after, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only remote organization rewrote configuration")
	}
	if kernel.ensureCalls != 0 {
		t.Fatalf("reading remote organization started Serve %d times", kernel.ensureCalls)
	}
}

func TestRemoteSessionOrganizationCASIsolatesHostsWithSameWorkspaceAndSessionID(t *testing.T) {
	app, kernel := remoteOrganizationFixture(t)
	a := SessionOrganizationWorkspace{HostID: "host-a", Scope: "project", WorkspaceRoot: "/same/workspace"}
	b := SessionOrganizationWorkspace{HostID: "host-b", Scope: "project", WorkspaceRoot: "/same/workspace"}
	beforeA, err := app.GetSessionOrganization(a)
	if err != nil {
		t.Fatal(err)
	}
	beforeB, err := app.GetSessionOrganization(b)
	if err != nil {
		t.Fatal(err)
	}
	winner, err := app.UpdateSessionOrganization(a, beforeA.Revision, SessionOrganizationMutation{Kind: "rename-group", GroupID: "work", Title: "Only host A"})
	if err != nil || !winner.Applied {
		t.Fatalf("rename=%#v err=%v", winner, err)
	}
	otherWindow := appWithFakeKernel(&fakeRemoteKernel{})
	stale, err := otherWindow.UpdateSessionOrganization(a, beforeA.Revision, SessionOrganizationMutation{Kind: "rename-group", GroupID: "work", Title: "Stale"})
	if err != nil || stale.Applied || stale.Revision != winner.Revision || stale.Groups[0].Title != "Only host A" {
		t.Fatalf("stale CAS=%#v err=%v", stale, err)
	}
	afterB, err := otherWindow.GetSessionOrganization(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterB, beforeB) {
		t.Fatalf("host A mutation changed host B: before=%#v after=%#v", beforeB, afterB)
	}
	if kernel.ensureCalls != 0 {
		t.Fatal("local remote-organization metadata mutation started Serve")
	}
}

func TestRemoteSessionOrganizationPreservesUnknownFieldsOnConfigRoundTrip(t *testing.T) {
	app, _ := remoteOrganizationFixture(t)
	path := config.UserConfigPath()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("\n[future_organization_settings]\nkeep = \"retained\"\n")...)
	body = bytes.ReplaceAll(body, []byte("[[remote.projects]]"), []byte("[[remote.projects]]\nfuture_project = \"retained\""))
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	w := SessionOrganizationWorkspace{HostID: "host-a", Scope: "project", WorkspaceRoot: "/same/workspace"}
	before, err := app.GetSessionOrganization(w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.UpdateSessionOrganization(w, before.Revision, SessionOrganizationMutation{Kind: "rename-group", GroupID: "work", Title: "Changed"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Remote.Projects {
		var organization map[string]any
		if err := json.Unmarshal([]byte(p.SessionOrganization), &organization); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(organization["futureOrganization"], map[string]any{"enabled": true}) {
			t.Fatalf("unknown organization field lost for %s", p.HostID)
		}
		group := organization["groups"].([]any)[0].(map[string]any)
		if group["futureGroup"] != "retained" {
			t.Fatalf("unknown group field lost for %s", p.HostID)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "[future_organization_settings]") || !strings.Contains(string(after), `keep = "retained"`) {
		t.Fatal("remote organization save removed unknown TOML settings")
	}
	if strings.Count(string(after), `future_project = 'retained'`)+strings.Count(string(after), `future_project = "retained"`) != 2 {
		t.Fatal("remote organization save removed unknown per-project TOML settings")
	}
}

func remoteOrganizationFixture(t *testing.T) (*App, *fakeRemoteKernel) {
	t.Helper()
	isolateDesktopUserDirs(t)
	if err := editUserConfig(func(c *config.Config) error {
		for _, host := range []string{"host-a", "host-b"} {
			if err := c.UpsertRemoteHost(config.RemoteHostEntry{Name: host, Host: "127.0.0.1", Port: 22, User: "test"}); err != nil {
				return err
			}
			key := "ref\x00" + host + "\x00same-session"
			organization, err := json.Marshal(map[string]any{
				"revision": 7, "manualOrderEnabled": true, "order": []string{key}, "migrationVersion": 1, "imported": map[string]bool{key: true},
				"groups":             []map[string]any{{"id": "work", "title": "Work", "members": []string{key}, "futureGroup": "retained"}},
				"futureOrganization": map[string]bool{"enabled": true},
			})
			if err != nil {
				return err
			}
			c.Remote.Projects = append(c.Remote.Projects, config.RemoteProjectEntry{HostID: host, Workspace: "/same/workspace", Title: host, SessionOrganization: string(organization)})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	kernel := &fakeRemoteKernel{}
	return appWithFakeKernel(kernel), kernel
}
