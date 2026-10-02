package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rotationFixture(t *testing.T, providers ...ProviderEntry) string {
	t.Helper()
	isolateUserConfigHome(t)
	unsetForTest(t, "TEAM_KEY")
	path := UserConfigPath()
	cfg := Default()
	cfg.Providers = append(cfg.Providers, providers...)
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if _, err := SetCredential("TEAM_KEY", "sk-old"); err != nil {
		t.Fatal(err)
	}
	return path
}

func teamProvider(name string) ProviderEntry {
	return ProviderEntry{Name: name, Kind: "openai", BaseURL: "https://" + name + ".invalid/v1", Model: "chat", APIKeyEnv: "TEAM_KEY"}
}

func withEditLocks(t *testing.T, fn func()) {
	t.Helper()
	unlockConfig := LockUserConfigEdits()
	defer unlockConfig()
	unlockCredentials, err := LockUserCredentialEdits()
	if err != nil {
		t.Fatal(err)
	}
	defer unlockCredentials()
	fn()
}

// rotateAndStop rotates providers' key and returns before publishing, as a
// crash between the credential write and the config commit would.
func rotateAndStop(t *testing.T, path string, providers []string, value string) (*Config, string) {
	t.Helper()
	var cfg *Config
	var slot string
	withEditLocks(t, func() {
		var err error
		if cfg, err = LoadForEditReadOnlyStrict(path); err != nil {
			t.Fatal(err)
		}
		if err := cfg.BeginModelCredentialCommitLocked(path, "cli-setup"); err != nil {
			t.Fatal(err)
		}
		if slot, err = cfg.RotateModelCredentialLocked(providers, value); err != nil {
			t.Fatal(err)
		}
	})
	return cfg, slot
}

func storedCredentialNames(t *testing.T) []string {
	t.Helper()
	file, ok := readDotEnvFile(UserCredentialsPath())
	if !ok {
		t.Fatal("credential store unreadable")
	}
	names := make([]string, 0, len(file.Values))
	for name := range file.Values {
		names = append(names, name)
	}
	return names
}

func TestRotationRewritesANameOnlyThisProviderReads(t *testing.T) {
	path := rotationFixture(t, teamProvider("p"))
	cfg, slot := rotateAndStop(t, path, []string{"p"}, "sk-new")
	withEditLocks(t, func() {
		if err := cfg.SaveModelSettingsTo(path, cfg.ModelSettingsBaseline()); err != nil {
			t.Fatal(err)
		}
		if err := cfg.MarkModelCredentialConfigCommittedLocked(path); err != nil {
			t.Fatal(err)
		}
		if err := cfg.CompleteModelCredentialCommitLocked(); err != nil {
			t.Fatal(err)
		}
	})
	if slot != "TEAM_KEY" {
		t.Fatalf("rotation used %q, want TEAM_KEY rewritten in place", slot)
	}
	if value, _ := envFileValue(UserCredentialsPath(), "TEAM_KEY"); value != "sk-new" || os.Getenv("TEAM_KEY") != "sk-new" {
		t.Fatalf("TEAM_KEY stored=%q env=%q, want sk-new", value, os.Getenv("TEAM_KEY"))
	}
	if names := storedCredentialNames(t); len(names) != 1 || journalCount(t) != 0 {
		t.Fatalf("store holds %v with %d journals; the previous value must be gone once published", names, journalCount(t))
	}
	if raw, _ := os.ReadFile(UserCredentialsPath()); strings.Contains(string(raw), "REASONIX_ROTATION_") {
		t.Fatalf("the dropped backup left a line behind:\n%s", raw)
	}
}

func TestRotationUsesAPrivateSlotFromAProjectFile(t *testing.T) {
	rotationFixture(t)
	project := filepath.Join(t.TempDir(), "reasonix.toml")
	cfg := Default()
	cfg.Providers = []ProviderEntry{teamProvider("p")}
	if err := cfg.SaveTo(project); err != nil {
		t.Fatal(err)
	}
	_, slot := rotateAndStop(t, project, []string{"p"}, "sk-new")
	if slot == "TEAM_KEY" {
		t.Fatal("a project file rotated a stored variable the user config may read")
	}
	if value, _ := envFileValue(UserCredentialsPath(), "TEAM_KEY"); value != "sk-old" {
		t.Fatalf("TEAM_KEY = %q, want it untouched", value)
	}
}

func TestRotationUsesAPrivateSlotWhenAnotherReaderShares(t *testing.T) {
	for name, share := range map[string]func(*Config){
		"provider": func(c *Config) { c.Providers = append(c.Providers, teamProvider("q")) },
		"setting":  func(c *Config) { c.Remote.Hosts = append(c.Remote.Hosts, RemoteHostEntry{PasswordEnv: "TEAM_KEY"}) },
	} {
		t.Run(name, func(t *testing.T) {
			path := rotationFixture(t, teamProvider("p"))
			cfg, err := LoadForEditReadOnlyStrict(path)
			if err != nil {
				t.Fatal(err)
			}
			share(cfg)
			if err := cfg.SaveTo(path); err != nil {
				t.Fatal(err)
			}
			_, slot := rotateAndStop(t, path, []string{"p"}, "sk-new")
			if slot == "TEAM_KEY" {
				t.Fatal("rotation rewrote a variable another reader resolves")
			}
			if value, _ := envFileValue(UserCredentialsPath(), "TEAM_KEY"); value != "sk-old" {
				t.Fatalf("TEAM_KEY = %q, want the shared value untouched", value)
			}
		})
	}
}

func TestRotationUsesAPrivateSlotForANameTheProviderOnlyNowReads(t *testing.T) {
	path := rotationFixture(t, ProviderEntry{Name: "p", Kind: "openai", BaseURL: "https://p.invalid/v1", Model: "chat", APIKeyEnv: "P_KEY"})
	var slot string
	withEditLocks(t, func() {
		cfg, err := LoadForEditReadOnlyStrict(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.BeginModelCredentialCommitLocked(path, "cli-setup"); err != nil {
			t.Fatal(err)
		}
		entry, _ := cfg.Provider("p")
		entry.APIKeyEnv = "TEAM_KEY"
		if slot, err = cfg.RotateModelCredentialLocked([]string{"p"}, "sk-new"); err != nil {
			t.Fatal(err)
		}
	})
	if slot == "TEAM_KEY" {
		t.Fatal("rotation overwrote a stored value the provider did not read before this edit")
	}
}

func TestInterruptedRotationRestoresThePreviousValue(t *testing.T) {
	path := rotationFixture(t, teamProvider("p"))
	rotateAndStop(t, path, []string{"p"}, "sk-new")
	recoverLocked(t, path)
	if value, _ := envFileValue(UserCredentialsPath(), "TEAM_KEY"); value != "sk-old" || os.Getenv("TEAM_KEY") != "sk-old" {
		t.Fatalf("TEAM_KEY stored=%q env=%q after recovery, want sk-old back", value, os.Getenv("TEAM_KEY"))
	}
	if names := storedCredentialNames(t); len(names) != 1 || journalCount(t) != 0 {
		t.Fatalf("store holds %v with %d journals after recovery", names, journalCount(t))
	}
}

func TestFailedRotationCleanupRestoresThePreviousValue(t *testing.T) {
	path := rotationFixture(t, teamProvider("p"))
	cfg, _ := rotateAndStop(t, path, []string{"p"}, "sk-new")
	withEditLocks(t, func() { cfg.CleanupStagedModelCredentialsLocked(path) })
	if value, _ := envFileValue(UserCredentialsPath(), "TEAM_KEY"); value != "sk-old" {
		t.Fatalf("TEAM_KEY = %q after a failed save, want sk-old back", value)
	}
	if names := storedCredentialNames(t); len(names) != 1 || journalCount(t) != 0 {
		t.Fatalf("store holds %v with %d journals after cleanup", names, journalCount(t))
	}
}

func TestRecoveryKeepsARotatedValueAnotherWriterReplaced(t *testing.T) {
	path := rotationFixture(t, teamProvider("p"))
	rotateAndStop(t, path, []string{"p"}, "sk-new")
	if _, err := SetCredential("TEAM_KEY", "sk-someone-else"); err != nil {
		t.Fatal(err)
	}
	recoverLocked(t, path)
	if value, _ := envFileValue(UserCredentialsPath(), "TEAM_KEY"); value != "sk-someone-else" {
		t.Fatalf("recovery replaced another writer's TEAM_KEY with %q", value)
	}
}

func TestRotationJournalHoldsNoCredentialValue(t *testing.T) {
	path := rotationFixture(t, teamProvider("p"))
	cfg, _ := rotateAndStop(t, path, []string{"p"}, "sk-new")
	raw, err := os.ReadFile(cfg.modelCredentialCommit.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-old") || strings.Contains(string(raw), "sk-new") {
		t.Fatalf("journal carries a credential value: %s", raw)
	}
}
