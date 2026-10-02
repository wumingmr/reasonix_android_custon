package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInventoryIsClassifiedAndCurrent(t *testing.T) {
	root := filepath.Join("..", "..")
	inv, err := build(root)
	if err != nil {
		t.Fatal(err)
	}
	if u := inv.unclassified(); len(u) > 0 {
		t.Fatalf("unclassified entries: %v", u)
	}
	counts := inv.counts()
	for _, kind := range kindOrder {
		// native-call and frontend-native tracked the retired shell's direct
		// bridge calls; both are legitimately empty under the Electron host.
		if len(counts[kind]) == 0 && kind != kindNativeCall && kind != kindFrontendNative {
			t.Errorf("no %s entries discovered", kind)
		}
	}
	md, js, err := render(inv)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"INVENTORY.md": md, "inventory.json": js} {
		have, err := os.ReadFile(filepath.Join(root, "docs", "desktop-migration", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !sameInventory(have, want) {
			t.Errorf("%s is stale; run: go run ./tools/desktopinventory", name)
		}
	}
}

func TestShellFileRules(t *testing.T) {
	cases := map[string]class{
		"webview2_recovery_windows.go": classDeleteShell,
		"tray_loop_windows.go":         classMigrateHost,
		"main.go":                      classKeepBusiness,
		"sessions.go":                  "",
	}
	for name, want := range cases {
		got, _, ok := shellFile(name)
		if (want == "") == ok || got != want {
			t.Errorf("%s: got %q ok=%v, want %q", name, got, ok, want)
		}
	}
}

func TestInventoryWithoutGit(t *testing.T) {
	// Source archives and shallow CI checkouts must render the same frozen
	// baseline as a developer checkout, without depending on available refs.
	root := filepath.Join("..", "..")
	withGit, err := build(root)
	if err != nil {
		t.Fatal(err)
	}
	_, want, err := render(withGit)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	inv, err := build(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := render(inv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("inventory depends on local Git availability")
	}
}

func TestInventoryComparisonIgnoresOnlySourceLineHints(t *testing.T) {
	const baseline = `{"name":"snapshot.sqlite","location":"desktop/session_source_compatibility.go:346","class":"keep-business","owner":"shared"}`
	for _, test := range []struct {
		name string
		old  string
		new  string
		same bool
	}{
		{"line", ":346", ":367", true},
		{"path", "session_source_compatibility.go", "other.go", false},
		{"entry", "snapshot.sqlite", "other.sqlite", false},
		{"class", "keep-business", "delete-shell", false},
		{"owner", "shared", "other", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := bytes.ReplaceAll([]byte(baseline), []byte(test.old), []byte(test.new))
			if got := sameInventory([]byte(baseline), changed); got != test.same {
				t.Fatalf("sameInventory = %v, want %v", got, test.same)
			}
		})
	}
	if sameInventory([]byte(baseline), nil) {
		t.Fatal("removed inventory accepted")
	}
	if sameInventory([]byte(`{"name":"release:346"}`), []byte(`{"name":"release:367"}`)) {
		t.Fatal("non-location number ignored")
	}
}
