package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"reasonix/internal/config"
	"reasonix/internal/control"
)

func TestAuditSetupEditsEffectiveProviderSource(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	root := t.TempDir()
	global := config.Default()
	global.Providers = []config.ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: "https://global.invalid/v1", Model: "chat", APIKeyEnv: "GLOBAL_KEY"}}
	if err := global.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "reasonix.toml")
	if err := os.WriteFile(project, []byte("[[providers]]\nname = \"relay\"\nkind = \"openai\"\nbase_url = \"https://project.invalid/v1\"\nmodel = \"chat\"\napi_key_env = \"PROJECT_KEY\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	effective, err := config.LoadForRootReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := effective.Provider("relay")
	if !ok || entry.BaseURL != "https://global.invalid/v1" {
		t.Fatal("fixture did not resolve global provider")
	}
	ctrl := control.New(control.Options{WorkspaceRoot: root})
	t.Cleanup(ctrl.Close)
	m := newTestChatTUI()
	m.ctrl = ctrl
	m.beginConnectionKeyEdit("relay")
	if m.setup.configPath != config.UserConfigPath() {
		t.Fatalf("setup edits shadowed project config %s instead of active global config", m.setup.configPath)
	}
}

func TestAuditSetupPasteDoesNotEnterChat(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			m := newTestChatTUI()
			m.setup = &connectionSetup{providerName: "relay"}
			m.input.SetValue("user draft")
			var msg tea.Msg = tea.PasteMsg{Content: "sk-audit-secret-value"}
			if native {
				msg = clipboardTextPasteMsg{text: "sk-audit-secret-value"}
			}
			updated, _ := m.Update(msg)
			m = updated.(chatTUI)
			if strings.Contains(m.input.Value(), "sk-audit-secret-value") {
				t.Fatal("pasting into the credential editor inserted the secret into the chat composer")
			}
			if m.setup.key != "sk-audit-secret-value" {
				t.Fatal("paste did not reach masked credential field")
			}
		})
	}
}

func TestAuditShellSetupKeyOnlyEditPersists(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	c := config.Default()
	c.Providers = []config.ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: "https://relay.invalid/v1", Model: "chat", APIKeyEnv: "OLD_KEY"}}
	path := config.UserConfigPath()
	if err := c.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newProviderSetupSessionForPath(c, path)
	if err := s.setCredentialForProviders([]string{"relay"}, "OLD_KEY", "updated-secret"); err != nil {
		t.Fatal(err)
	}
	written, err := commitProviderSetupSession(s, path)
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Fatal("updating only an existing key silently skipped the commit")
	}
	saved, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := saved.Provider("relay")
	if entry.APIKeyEnv == "OLD_KEY" || !config.CredentialStored(entry.APIKeyEnv) {
		t.Fatal("new credential was not published")
	}
}

func TestAuditShellSetupDistinctConnectionKeysStaySeparate(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	c := config.Default()
	c.Providers = []config.ProviderEntry{{Name: "a", Kind: "openai", BaseURL: "https://relay.invalid/v1", Model: "chat", APIKeyEnv: "SHARED_KEY"}, {Name: "b", Kind: "openai", BaseURL: "https://relay.invalid/v1", Model: "chat", APIKeyEnv: "SHARED_KEY"}}
	c.DefaultModel = "a/chat"
	path := config.UserConfigPath()
	if err := c.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newProviderSetupSessionForPath(c, path)
	// Make another legitimate edit so this exercises the commit rather than the
	// independent key-only no-op defect.
	if err := s.setDefaultModel("b/chat"); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredentialForProviders([]string{"a"}, "SHARED_KEY", "key-for-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredentialForProviders([]string{"b"}, "SHARED_KEY", "key-for-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	saved, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := saved.Provider("a")
	b, _ := saved.Provider("b")
	if a.APIKeyEnv == b.APIKeyEnv {
		t.Fatal("separate edits for A and B were coalesced into B's credential slot")
	}
}

func TestShellCredentialOnlyEditRejectsConcurrentRotation(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	path := config.UserConfigPath()
	c := config.Default()
	c.Providers = []config.ProviderEntry{{Name: "relay", Kind: "openai", Model: "chat", BaseURL: "https://relay.invalid/v1", APIKeyEnv: "OLD_KEY"}}
	if err := c.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newProviderSetupSessionForPath(c, path)
	if err := s.setCredentialForProviders([]string{"relay"}, "OLD_KEY", "stale-draft"); err != nil {
		t.Fatal(err)
	}
	result, err := config.CommitConnectionCredential(config.ConnectionCredentialRequest{RequestID: "other-writer", ConfigPath: path, ProviderNames: []string{"relay"}, Key: "concurrent-key"})
	if err != nil || !result.Persisted {
		t.Fatalf("concurrent save: %+v, %v", result, err)
	}
	if _, err := commitProviderSetupSession(s, path); err == nil {
		t.Fatal("stale credential draft overwrote concurrent rotation")
	}
	current, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := current.Provider("relay")
	if entry.APIKeyEnv != result.Slot {
		t.Fatal("concurrent slot reference changed")
	}
}
