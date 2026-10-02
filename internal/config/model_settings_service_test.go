package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitConnectionCredentialIsolatesSelectedConnection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	path := UserConfigPath()
	cfg := Default()
	one := ProviderEntry{Name: "one", Kind: "openai", BaseURL: "https://one.invalid/v1", Models: []string{"chat"}, Default: "chat", APIKeyEnv: "SHARED_API_KEY"}
	two := ProviderEntry{Name: "two", Kind: "openai", BaseURL: "https://two.invalid/v1", Models: []string{"chat"}, Default: "chat", APIKeyEnv: "SHARED_API_KEY"}
	if err := cfg.UpsertProvider(one); err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpsertProvider(two); err != nil {
		t.Fatal(err)
	}
	unlock := LockUserConfigEdits()
	if err := cfg.SaveTo(path); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	if err := os.WriteFile(UserCredentialsPath(), []byte("SHARED_API_KEY=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := CommitConnectionCredential(ConnectionCredentialRequest{
		RequestID: "test-rotate-one", ConfigPath: path, ProviderNames: []string{"one"}, Key: "new-secret", ExpectedRevision: ConfigFileRevision(path),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Persisted || !strings.HasPrefix(result.Slot, "REASONIX_CONNECTION_") {
		t.Fatalf("result = %+v", result)
	}
	saved, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	savedOne, _ := saved.Provider("one")
	savedTwo, _ := saved.Provider("two")
	if savedOne.APIKeyEnv != result.Slot {
		t.Fatalf("selected provider slot = %q, want %q", savedOne.APIKeyEnv, result.Slot)
	}
	if savedTwo.APIKeyEnv != "SHARED_API_KEY" {
		t.Fatalf("unselected provider changed to %q", savedTwo.APIKeyEnv)
	}
	raw, err := os.ReadFile(UserCredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "SHARED_API_KEY=old") || !strings.Contains(text, result.Slot+"=new-secret") {
		t.Fatalf("credential store did not preserve old slot and add new slot:\n%s", text)
	}
	entries, err := os.ReadDir(filepath.Join(home, "transactions", "model-credentials"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("completed transaction left %d journal(s)", len(entries))
	}
}

func TestCommitConnectionCredentialDurableRequestReceipt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	cfg := Default()
	cfg.Providers = []ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "chat", APIKeyEnv: "OLD_KEY"}}
	path := UserConfigPath()
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	req := ConnectionCredentialRequest{
		RequestID: "shared-service-receipt", ConfigPath: path, ProviderNames: []string{"relay"},
		Key: "first-secret", ExpectedRevision: ConfigFileRevision(path),
	}
	first, err := CommitConnectionCredential(req)
	if err != nil || !first.Persisted {
		t.Fatalf("first commit = %+v, %v", first, err)
	}
	replayed, err := CommitConnectionCredential(req)
	if err != nil || !replayed.Persisted || replayed.Revision != first.Revision {
		t.Fatalf("replayed commit = %+v, %v", replayed, err)
	}
	req.Key = "different-secret"
	if _, err := CommitConnectionCredential(req); err == nil || !strings.Contains(err.Error(), "request_conflict") {
		t.Fatalf("conflicting request error = %v", err)
	}
}
