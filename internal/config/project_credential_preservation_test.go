package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectCredentialEditPreservesUnknownProviderFields(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	body := "[[providers]]\nname='relay'\nkind='openai'\nbase_url='https://relay.invalid/v1'\nmodel='chat'\napi_key_env='OLD_KEY'\nfuture_option='keep-me'\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := CommitConnectionCredential(ConnectionCredentialRequest{RequestID: "project-test", ConfigPath: path, ProviderNames: []string{"relay"}, Key: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "future_option") {
		t.Fatal("credential-only edit removed unknown provider field")
	}
}

func TestCredentialReceiptLegacyDigestCannotReplay(t *testing.T) {
	result, err := connectionCredentialReceiptResult(ModelSettingsReceipt{RequestDigest: "legacy-digest", AfterRevision: "saved"}, "hmac-v1:new")
	if err == nil || !strings.Contains(err.Error(), "unknown_result") || result.Persisted {
		t.Fatal("legacy receipt was treated as a verified replay")
	}
	result, err = connectionCredentialReceiptResult(ModelSettingsReceipt{RequestDigest: "hmac-v1:one", AfterRevision: "saved"}, "hmac-v1:two")
	if err == nil || !strings.Contains(err.Error(), "request_conflict") || result.Persisted {
		t.Fatal("changed request was accepted")
	}
}
