package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func enabledNames(entries []PluginEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	slices.Sort(names)
	return names
}

func writeProjectDeclaredServers(t *testing.T) (home, root string) {
	t.Helper()
	home, root = t.TempDir(), t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	mustWrite := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(home, "config.toml"), `
[[plugins]]
name = "user-stdio"
command = "user-server"
`)
	mustWrite(filepath.Join(root, "reasonix.toml"), `
[[plugins]]
name = "toml-stdio"
command = "project-server"

[[plugins]]
name = "toml-http"
type = "http"
url = "http://127.0.0.1:9/mcp"
auto_start = true
`)
	mustWrite(filepath.Join(root, ".mcp.json"), `{"mcpServers":{
  "json-stdio": {"command": "project-server"},
  "json-http": {"type": "http", "url": "http://127.0.0.1:9/mcp"}
}}`)
	return home, root
}

func TestProjectDeclaredServersStayOffUntilTheUserEnablesThem(t *testing.T) {
	home, root := writeProjectDeclaredServers(t)
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMCPActivationStore(home)
	if got := enabledNames(cfg.EnabledPlugins(root, store)); !slices.Equal(got, []string{"user-stdio"}) {
		t.Fatalf("enabled with no decision = %v, want only the user-level server", got)
	}

	for _, p := range cfg.Plugins {
		if p.Name == "toml-http" || p.Name == "json-stdio" {
			if err := store.SetServerEnabled(p, root, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := enabledNames(cfg.EnabledPlugins(root, store)); !slices.Equal(got, []string{"json-stdio", "toml-http", "user-stdio"}) {
		t.Fatalf("enabled after explicit approval = %v", got)
	}
	if got := enabledNames(cfg.EnabledPlugins(t.TempDir(), store)); !slices.Equal(got, []string{"user-stdio"}) {
		t.Fatalf("approval leaked into another workspace: %v", got)
	}
}

func TestUnreadableActivationStoreKeepsProjectServersOff(t *testing.T) {
	home, root := writeProjectDeclaredServers(t)
	if err := os.WriteFile(MCPActivationPath(home), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := enabledNames(cfg.EnabledPlugins(root, NewMCPActivationStore(home))); !slices.Equal(got, []string{"user-stdio"}) {
		t.Fatalf("enabled with an unreadable store = %v, want only the user-level server", got)
	}
}

func TestServerOfUnknownProvenanceCountsAsProjectDeclared(t *testing.T) {
	store := NewMCPActivationStore(t.TempDir())
	enabled, err := store.IsEnabled(PluginEntry{Name: "anon", Command: "x"}, t.TempDir())
	if err != nil || enabled {
		t.Fatalf("unknown-source server enabled=%v err=%v, want off", enabled, err)
	}
	if DeclaredDefaultOn(PluginEntry{Name: "anon"}) {
		t.Fatal("DeclaredDefaultOn(unknown source) = true, want false")
	}
	if !DeclaredDefaultOn(PluginEntry{Name: "u", Source: MCPSourceUserConfig}) {
		t.Fatal("DeclaredDefaultOn(user source) = false, want true")
	}
}

func loadProjectServer(t *testing.T, root, name string) PluginEntry {
	t.Helper()
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Plugins {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("server %q not loaded", name)
	return PluginEntry{}
}

func TestProjectDecisionHoldsOnlyForTheApprovedDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(root string) error
	}{
		{"command args", func(root string) error {
			return os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("[[plugins]]\nname = \"srv\"\ncommand = \"./server.sh\"\nargs = [\"--other\"]\n"), 0o600)
		}},
		{"workspace executable content", func(root string) error {
			return os.WriteFile(filepath.Join(root, "server.sh"), []byte("#!/bin/sh\necho changed\n"), 0o700)
		}},
		{"project .env value it expands", func(root string) error {
			return os.WriteFile(filepath.Join(root, ".env"), []byte("SRV_FLAG=--changed\n"), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Setenv("REASONIX_HOME", home)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("SRV_FLAG", "")
			os.Unsetenv("SRV_FLAG")
			for name, body := range map[string]string{
				"reasonix.toml": "[[plugins]]\nname = \"srv\"\ncommand = \"./server.sh\"\nargs = [\"${SRV_FLAG}\"]\n",
				"server.sh":     "#!/bin/sh\necho ok\n",
				".env":          "SRV_FLAG=--ok\n",
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			store := NewMCPActivationStore(home)
			if err := store.SetServerEnabled(loadProjectServer(t, root, "srv"), root, true); err != nil {
				t.Fatal(err)
			}
			if d, err := store.Decision(loadProjectServer(t, root, "srv"), root); err != nil || d != MCPDecisionOn {
				t.Fatalf("decision right after enabling = %v, %v", d.Code(), err)
			}
			if err := tc.change(root); err != nil {
				t.Fatal(err)
			}
			d, err := store.Decision(loadProjectServer(t, root, "srv"), root)
			if err != nil || d != MCPDecisionChanged {
				t.Fatalf("decision after the declaration changed = %s, %v; want %s", d.Code(), err, MCPDecisionChanged.Code())
			}
		})
	}
}

func TestProjectDotEnvDoesNotExpandUserLevelServers(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("USER_SRV_OPTS", "")
	os.Unsetenv("USER_SRV_OPTS")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[[plugins]]\nname = \"user-stdio\"\ncommand = \"node\"\nenv = { NODE_OPTIONS = \"${USER_SRV_OPTS}\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("USER_SRV_OPTS=--require ./x.js\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := loadProjectServer(t, root, "user-stdio")
	if got := p.ExpandedPlugin().Env["NODE_OPTIONS"]; got != "" {
		t.Fatalf("project .env expanded a user-level server: NODE_OPTIONS=%q", got)
	}
}

// Carrying a decision across a Reasonix edit covers only the declaration that
// edit wrote; a different one found afterwards stays unapproved.
func TestKeptDecisionDoesNotCoverAnotherWritersDeclaration(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	toml := func(arg string) error {
		return os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("[[plugins]]\nname = \"srv\"\ncommand = \"server\"\nargs = [\""+arg+"\"]\n"), 0o600)
	}
	if err := toml("--approved"); err != nil {
		t.Fatal(err)
	}
	if err := DefaultMCPActivationStore().SetServerEnabled(loadProjectServer(t, root, "srv"), root, true); err != nil {
		t.Fatal(err)
	}
	userEdit := loadProjectServer(t, root, "srv")
	userEdit.Args = []string{"--edited"}
	err := KeepMCPDecisionAcross(root, "srv", func() (PluginEntry, error) {
		return userEdit, toml("--someone-else")
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := MCPServerDecision(loadProjectServer(t, root, "srv"), root); d != MCPDecisionChanged {
		t.Fatalf("decision after a foreign rewrite = %s, want %s", d.Code(), MCPDecisionChanged.Code())
	}
	err = KeepMCPDecisionAcross(root, "srv", func() (PluginEntry, error) {
		return userEdit, toml("--edited")
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := MCPServerDecision(loadProjectServer(t, root, "srv"), root); d != MCPDecisionChanged {
		t.Fatalf("an unapproved server was re-approved by an edit: %s", d.Code())
	}
	if err := DefaultMCPActivationStore().SetServerEnabled(loadProjectServer(t, root, "srv"), root, true); err != nil {
		t.Fatal(err)
	}
	userEdit.Args = []string{"--mine"}
	if err := KeepMCPDecisionAcross(root, "srv", func() (PluginEntry, error) { return userEdit, toml("--mine") }); err != nil {
		t.Fatal(err)
	}
	if d := MCPServerDecision(loadProjectServer(t, root, "srv"), root); d != MCPDecisionOn {
		t.Fatalf("decision after the user's own edit = %s, want enabled", d.Code())
	}
}

// Inputs the declaration draws from the workspace that change while Reasonix
// writes an edit are not covered by the decision the edit carries.
func TestKeptDecisionRejectsInputsChangedDuringTheEdit(t *testing.T) {
	for _, tc := range []struct {
		name, toml, file, before, after string
	}{
		{"named file", "[[plugins]]\nname = \"srv\"\ncommand = \"node\"\nargs = [\"server.js\"]\n", "server.js", "good", "changed"},
		{"project .env", "[[plugins]]\nname = \"srv\"\ncommand = \"node\"\nargs = [\"${ENTRY}\"]\n", ".env", "ENTRY=a.js\n", "ENTRY=b.js\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Setenv("REASONIX_HOME", home)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("ENTRY", "")
			os.Unsetenv("ENTRY")
			for name, body := range map[string]string{"reasonix.toml": tc.toml, tc.file: tc.before} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := DefaultMCPActivationStore().SetServerEnabled(loadProjectServer(t, root, "srv"), root, true); err != nil {
				t.Fatal(err)
			}
			err := KeepMCPDecisionAcross(root, "srv", func() (PluginEntry, error) {
				written := loadProjectServer(t, root, "srv")
				return written, os.WriteFile(filepath.Join(root, tc.file), []byte(tc.after), 0o600)
			})
			if err != nil {
				t.Fatal(err)
			}
			if d := MCPServerDecision(loadProjectServer(t, root, "srv"), root); d == MCPDecisionOn {
				t.Fatalf("%s changed during the edit and the decision carried onto it", tc.file)
			}
		})
	}
}
