package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func unsetForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers the restore; staging pins the key into the process
	_ = os.Unsetenv(key)
}

func stageNamedAndStop(t *testing.T, path, key, value string) *Config {
	t.Helper()
	unsetForTest(t, key)
	unlockConfig := LockUserConfigEdits()
	defer unlockConfig()
	unlockCredentials, err := LockUserCredentialEdits()
	if err != nil {
		t.Fatal(err)
	}
	defer unlockCredentials()
	cfg, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.BeginModelCredentialCommitLocked(path, "cli-setup"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.StageNamedModelCredentialLocked(key, "p", value); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func recoverLocked(t *testing.T, path string) {
	t.Helper()
	unlockConfig := LockUserConfigEdits()
	defer unlockConfig()
	unlockCredentials, err := LockUserCredentialEdits()
	if err != nil {
		t.Fatal(err)
	}
	defer unlockCredentials()
	if err := RecoverModelCredentialCommitsLocked(path); err != nil {
		t.Fatal(err)
	}
}

func journalCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(modelCredentialTransactionDir())
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n
}

func TestRecoveryKeepsANamedSlotAnotherWriterTookOver(t *testing.T) {
	isolateUserConfigHome(t)
	path := UserConfigPath()
	if err := Default().SaveTo(path); err != nil {
		t.Fatal(err)
	}
	stageNamedAndStop(t, path, "TEAM_KEY", "sk-mine")
	if _, err := SetCredential("TEAM_KEY", "sk-team-owned-elsewhere"); err != nil {
		t.Fatal(err)
	}
	recoverLocked(t, path)
	if value, _ := envFileValue(UserCredentialsPath(), "TEAM_KEY"); value != "sk-team-owned-elsewhere" {
		t.Fatalf("recovery removed a value it did not stage: TEAM_KEY=%q", value)
	}
}

func TestRecoveryStillRemovesAnUnpublishedNamedSlot(t *testing.T) {
	isolateUserConfigHome(t)
	path := UserConfigPath()
	if err := Default().SaveTo(path); err != nil {
		t.Fatal(err)
	}
	stageNamedAndStop(t, path, "TEAM_KEY", "sk-mine")
	recoverLocked(t, path)
	if CredentialStored("TEAM_KEY") || journalCount(t) != 0 {
		t.Fatalf("unpublished slot stored=%v journals=%d, want both gone", CredentialStored("TEAM_KEY"), journalCount(t))
	}
}

func TestCleanupReadsReferencesStructurallyNotBySubstring(t *testing.T) {
	isolateUserConfigHome(t)
	path := UserConfigPath()
	cfg := Default()
	cfg.Providers = []ProviderEntry{{Name: "deep", Kind: "openai", BaseURL: "https://deep.invalid/v1", Model: "chat", APIKeyEnv: "DEEPSEEK_API_KEY"}}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	staged := stageNamedAndStop(t, path, "API_KEY", "sk-short")
	func() {
		unlockConfig := LockUserConfigEdits()
		defer unlockConfig()
		unlockCredentials, err := LockUserCredentialEdits()
		if err != nil {
			t.Fatal(err)
		}
		defer unlockCredentials()
		staged.CleanupStagedModelCredentialsLocked(path)
	}()
	if CredentialStored("API_KEY") || journalCount(t) != 0 {
		t.Fatalf("API_KEY stored=%v journals=%d: a substring of DEEPSEEK_API_KEY kept the orphan", CredentialStored("API_KEY"), journalCount(t))
	}
}

func TestClaimableRefusesNamesOtherReadersHold(t *testing.T) {
	isolateUserConfigHome(t)
	t.Setenv("OPENAI_API_KEY", "sk-shell")
	cfg := Default()
	cfg.Remote.Hosts = append(cfg.Remote.Hosts, RemoteHostEntry{PasswordEnv: "BOX_PASSWORD"})
	cases := map[string]CredentialKeyHolder{
		cfg.Bot.QQ.AppSecretEnv: CredentialKeyHeldBySetting,
		"BOX_PASSWORD":          CredentialKeyHeldBySetting,
		"PATH":                  CredentialKeyHeldByEnvironment,
		"OPENAI_API_KEY":        CredentialKeyHeldByEnvironment,
	}
	for key, want := range cases {
		err := cfg.CredentialKeyClaimable(key, "p")
		var inUse *CredentialKeyInUseError
		if !errors.As(err, &inUse) || inUse.Holder != want {
			t.Errorf("CredentialKeyClaimable(%q) = %v, want holder %d", key, err, want)
		}
	}
}
