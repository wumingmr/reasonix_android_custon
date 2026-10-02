package cli

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"reasonix/internal/config"
)

const opencodeBaseURL = "https://opencode.ai/zen/go/v1"

func newKeyEnvTestSession(t *testing.T, fixture func(*config.Config)) (*providerSetupSession, string) {
	t.Helper()
	isolateUserConfig(t)
	t.Setenv("REASONIX_HOME", t.TempDir())
	for _, key := range []string{"CUSTOM_OPENCODE_AI_API_KEY", "OPENCODE_KEY", "ORPHAN_KEY", "SHARED_RELAY_KEY"} {
		t.Setenv(key, "") // registers the restore; saving pins the key into the process
		_ = os.Unsetenv(key)
	}
	path := config.UserConfigPath()
	c := config.Default()
	if fixture != nil {
		fixture(c)
	}
	if err := c.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	return newProviderSetupSessionForPath(c, path), path
}

func addOpencodeThroughWizard(t *testing.T, s *providerSetupSession, input string, out *bytes.Buffer) bool {
	t.Helper()
	in := bufio.NewScanner(strings.NewReader(input))
	result, err := promptCustomProviderManualWith(in, opencodeBaseURL, "", false, "sk-opencode")
	if err != nil {
		t.Fatal(err)
	}
	return s.addPrompted(in, out, result)
}

func savedKeyEnv(t *testing.T, path, provider string) string {
	t.Helper()
	saved, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := saved.Provider(provider)
	if !ok {
		t.Fatalf("provider %q was not saved", provider)
	}
	return entry.APIKeyEnv
}

func TestSetupHonoursATypedKeyEnvName(t *testing.T) {
	s, path := newKeyEnvTestSession(t, nil)
	var out bytes.Buffer
	if !addOpencodeThroughWizard(t, s, "chat\nCUSTOM_OPENCODE_AI_API_KEY\n\n", &out) {
		t.Fatalf("wizard refused the provider: %s", out.String())
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	if got := savedKeyEnv(t, path, "custom-opencode-ai"); got != "CUSTOM_OPENCODE_AI_API_KEY" {
		t.Fatalf("api_key_env = %q, want the typed CUSTOM_OPENCODE_AI_API_KEY", got)
	}
	if res := config.ResolveCredentialForRootGlobalFirst(".", "CUSTOM_OPENCODE_AI_API_KEY"); res.Value != "sk-opencode" {
		t.Fatalf("typed variable holds %q, want the entered key", res.Value)
	}
}

func TestSetupEnterForDefaultKeepsAPrivateSlot(t *testing.T) {
	s, path := newKeyEnvTestSession(t, nil)
	var out bytes.Buffer
	if !addOpencodeThroughWizard(t, s, "chat\n\n\n", &out) {
		t.Fatalf("wizard refused the provider: %s", out.String())
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	if got := savedKeyEnv(t, path, "custom-opencode-ai"); !strings.HasPrefix(got, "REASONIX_CONNECTION_") {
		t.Fatalf("api_key_env = %q, want a private REASONIX_CONNECTION_ slot", got)
	}
}

func TestSetupRefusesATypedNameAnotherProviderHolds(t *testing.T) {
	other := config.ProviderEntry{Name: "relay", Kind: "openai", BaseURL: "https://relay.invalid/v1", Model: "chat", APIKeyEnv: "SHARED_RELAY_KEY"}
	s, path := newKeyEnvTestSession(t, func(c *config.Config) { c.Providers = append(c.Providers, other) })
	var out bytes.Buffer
	if !addOpencodeThroughWizard(t, s, "chat\nSHARED_RELAY_KEY\n\n\n", &out) {
		t.Fatalf("wizard refused the provider: %s", out.String())
	}
	if !strings.Contains(out.String(), "SHARED_RELAY_KEY is already used by provider relay") {
		t.Fatalf("wizard did not say why the typed name was refused: %q", out.String())
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	if got := savedKeyEnv(t, path, "custom-opencode-ai"); !strings.HasPrefix(got, "REASONIX_CONNECTION_") {
		t.Fatalf("api_key_env = %q, want a private slot after Enter", got)
	}
	if config.CredentialStored("SHARED_RELAY_KEY") {
		t.Fatal("the new provider's key was written into relay's variable")
	}
}

func TestSetupRefusesATypedNameThatAlreadyHoldsAKey(t *testing.T) {
	s, path := newKeyEnvTestSession(t, nil)
	if _, err := config.SetCredential("ORPHAN_KEY", "sk-someone-else"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if !addOpencodeThroughWizard(t, s, "chat\nORPHAN_KEY\n\nOPENCODE_KEY\n", &out) {
		t.Fatalf("wizard refused the provider: %s", out.String())
	}
	if !strings.Contains(out.String(), "ORPHAN_KEY already holds a saved credential") {
		t.Fatalf("wizard did not say why the typed name was refused: %q", out.String())
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	if got := savedKeyEnv(t, path, "custom-opencode-ai"); got != "OPENCODE_KEY" {
		t.Fatalf("api_key_env = %q, want the second typed name", got)
	}
	if res := config.ResolveCredentialForRootGlobalFirst(".", "ORPHAN_KEY"); res.Value != "sk-someone-else" {
		t.Fatalf("existing credential became %q", res.Value)
	}
}

func TestSetupSaveRefusesATypedNameClaimedAfterTheWizard(t *testing.T) {
	// A declared access list keeps the concurrent write from also changing which providers count as configured.
	s, path := newKeyEnvTestSession(t, func(c *config.Config) { c.Desktop.ProviderAccess = []string{"deepseek"} })
	var out bytes.Buffer
	if !addOpencodeThroughWizard(t, s, "chat\nCUSTOM_OPENCODE_AI_API_KEY\n\n", &out) {
		t.Fatalf("wizard refused the provider: %s", out.String())
	}
	if _, err := config.SetCredential("CUSTOM_OPENCODE_AI_API_KEY", "sk-written-meanwhile"); err != nil {
		t.Fatal(err)
	}
	_, err := commitProviderSetupSession(s, path)
	var inUse *config.CredentialKeyInUseError
	if !errors.As(err, &inUse) || inUse.Holder != config.CredentialKeyHeldByStore {
		t.Fatalf("commit error = %v, want CredentialKeyInUseError held by the store", err)
	}
	if res := config.ResolveCredentialForRootGlobalFirst(".", "CUSTOM_OPENCODE_AI_API_KEY"); res.Value != "sk-written-meanwhile" {
		t.Fatalf("commit overwrote the concurrent credential with %q", res.Value)
	}
}

func TestSetupRefusesATypedNameABotSettingReads(t *testing.T) {
	s, path := newKeyEnvTestSession(t, nil)
	secret := s.cfg.Bot.QQ.AppSecretEnv
	var out bytes.Buffer
	if !addOpencodeThroughWizard(t, s, "chat\n"+secret+"\n\n\n", &out) {
		t.Fatalf("wizard refused the provider: %s", out.String())
	}
	if !strings.Contains(out.String(), secret+" is read by a bot or remote-host setting") {
		t.Fatalf("wizard did not say why %s was refused: %q", secret, out.String())
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	if config.CredentialStored(secret) {
		t.Fatalf("the provider key was written into the bot secret %s", secret)
	}
}

func TestSetupRefusesATypedNameTheEnvironmentSets(t *testing.T) {
	for _, key := range []string{"PATH", "OPENAI_API_KEY"} {
		t.Run(key, func(t *testing.T) {
			s, path := newKeyEnvTestSession(t, nil)
			before := "shell-value"
			if key == "PATH" {
				before = os.Getenv("PATH")
			}
			t.Setenv(key, before)
			var out bytes.Buffer
			if !addOpencodeThroughWizard(t, s, "chat\n"+key+"\n\n\n", &out) {
				t.Fatalf("wizard refused the provider: %s", out.String())
			}
			if !strings.Contains(out.String(), key+" is already set in the environment") {
				t.Fatalf("wizard did not say why %s was refused: %q", key, out.String())
			}
			if _, err := commitProviderSetupSession(s, path); err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv(key); got != before {
				t.Fatalf("%s changed to %q", key, got)
			}
			if config.CredentialStored(key) {
				t.Fatalf("%s was written to the credential store", key)
			}
		})
	}
}

func TestSetupRotatesAKeyUnderATypedNameInPlace(t *testing.T) {
	typed := config.ProviderEntry{Name: "opencode", Kind: "openai", BaseURL: opencodeBaseURL, Model: "chat", APIKeyEnv: "CUSTOM_OPENCODE_AI_API_KEY"}
	s, path := newKeyEnvTestSession(t, func(c *config.Config) { c.Providers = append(c.Providers, typed) })
	if _, err := config.SetCredential("CUSTOM_OPENCODE_AI_API_KEY", "sk-old"); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredentialForProviders([]string{"opencode"}, "CUSTOM_OPENCODE_AI_API_KEY", "sk-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	if got := savedKeyEnv(t, path, "opencode"); got != "CUSTOM_OPENCODE_AI_API_KEY" {
		t.Fatalf("api_key_env = %q, want the typed name kept", got)
	}
	if res := config.ResolveCredentialForRootGlobalFirst(".", "CUSTOM_OPENCODE_AI_API_KEY"); res.Value != "sk-new" {
		t.Fatalf("typed variable holds %q after rotation, want the new key", res.Value)
	}
}
