package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/fileutil"
)

func TestModelConfigRevisionUsesStableKeyedDigest(t *testing.T) {
	isolateUserConfigHome(t)
	first, err := modelConfigContentRevision([]byte("api_key = secret"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := modelConfigContentRevision([]byte("api_key = secret"))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := modelConfigContentRevision([]byte("api_key = different"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "hmac-sha256:") || first != second || first == changed {
		t.Fatalf("unexpected revisions: first=%q second=%q changed=%q", first, second, changed)
	}
}

func TestModelCredentialCommitRecoveryAtPersistenceBoundaries(t *testing.T) {
	for _, crash := range []struct {
		name         string
		journalWrite int
		pathSuffix   string
		wantCommit   bool
	}{
		{name: "initial journal", journalWrite: 1},
		{name: "credential publish", pathSuffix: string(filepath.Separator) + ".env"},
		{name: "config publish", pathSuffix: string(filepath.Separator) + "config.toml"},
		{name: "publication candidate", journalWrite: 4},
		{name: "committed journal", journalWrite: 5, wantCommit: true},
		{name: "receipt publish", pathSuffix: string(filepath.Separator) + "model-settings-receipts", wantCommit: true},
	} {
		t.Run(crash.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("REASONIX_HOME", home)
			path := UserConfigPath()
			cfg := Default()
			cfg.Providers = []ProviderEntry{{Name: "one", Kind: "openai", BaseURL: "https://one.invalid/v1", Model: "chat", APIKeyEnv: "OLD_KEY"}}
			if err := cfg.SaveTo(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(UserCredentialsPath(), []byte("OLD_KEY=old\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			journalWrites := 0
			crashed := false
			previous := fileutil.CrashPoint
			fileutil.CrashPoint = func(_ string, target string) {
				isJournal := strings.Contains(target, filepath.Join("transactions", "model-credentials"))
				if isJournal {
					journalWrites++
				}
				matches := crash.journalWrite > 0 && isJournal && journalWrites == crash.journalWrite
				if crash.pathSuffix != "" {
					matches = strings.HasSuffix(target, crash.pathSuffix) || strings.Contains(target, crash.pathSuffix+string(filepath.Separator))
				}
				if matches && !crashed {
					crashed = true
					panic("injected crash")
				}
			}
			func() {
				defer func() { _ = recover() }()
				unlockConfig := LockUserConfigEdits()
				defer unlockConfig()
				unlockCredentials, err := LockUserCredentialEdits()
				if err != nil {
					t.Fatal(err)
				}
				defer unlockCredentials()
				current, err := LoadForEditReadOnlyStrict(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := current.BeginModelCredentialCommitLocked(path, "crash-request", "request-digest"); err != nil {
					panic(err)
				}
				slot, err := current.StageModelCredentialLocked("new-secret")
				if err != nil {
					panic(err)
				}
				entry, _ := current.Provider("one")
				updated := *entry
				updated.APIKeyEnv = slot
				if err := current.UpsertProvider(updated); err != nil {
					panic(err)
				}
				if err := current.SaveTo(path); err != nil {
					panic(err)
				}
				if err := current.MarkModelCredentialConfigCommittedLocked(path, "result-revision"); err != nil {
					panic(err)
				}
				if err := current.CompleteModelCredentialCommitLocked(); err != nil {
					panic(err)
				}
			}()
			fileutil.CrashPoint = previous
			t.Cleanup(func() { fileutil.CrashPoint = previous })
			if !crashed {
				t.Fatal("injected crash point did not fire")
			}

			unlockConfig := LockUserConfigEdits()
			unlockCredentials, err := LockUserCredentialEdits()
			if err != nil {
				unlockConfig()
				t.Fatal(err)
			}
			err = RecoverModelCredentialCommitsLocked(path)
			unlockCredentials()
			unlockConfig()
			if err != nil {
				t.Fatal(err)
			}
			saved, err := LoadForEditReadOnlyStrict(path)
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := saved.Provider("one")
			if crash.wantCommit {
				if entry.APIKeyEnv == "OLD_KEY" || !CredentialStored(entry.APIKeyEnv) {
					t.Fatalf("committed connection was not preserved: %+v", entry)
				}
				if receipt, ok := LookupModelSettingsReceipt("crash-request"); !ok || receipt.RequestDigest != "request-digest" {
					t.Fatalf("commit receipt = %+v, %v", receipt, ok)
				}
			} else if entry.APIKeyEnv != "OLD_KEY" || !CredentialStored("OLD_KEY") {
				t.Fatalf("uncommitted edit changed old connection: %+v", entry)
			}
		})
	}
}

func TestModelSettingsReceiptContainsNoCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	j := &modelCredentialCommitJournal{
		Schema: modelCredentialCommitSchema, RequestID: "receipt", RequestDigest: "digest",
		ConfigPath: filepath.Join(home, "config.toml"), BeforeRevision: "before", AfterRevision: "after",
		ResultRevision: "result", Phase: "config_committed",
	}
	if err := persistModelSettingsReceipt(j); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(modelSettingsReceiptPath("receipt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "apiKey") {
		t.Fatalf("receipt contains credential material: %s", raw)
	}
	if receipt, ok := LookupModelSettingsReceipt("receipt"); !ok || receipt.ResultRevision != "result" {
		t.Fatalf("receipt lookup = %s, %+v, %v", string(raw), receipt, ok)
	}
}

func TestCleanupStagedModelCredentialWithoutJournal(t *testing.T) {
	isolateUserConfigHome(t)
	path := UserConfigPath()
	cfg := Default()
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	unlockConfig := LockUserConfigEdits()
	unlockCredentials, err := LockUserCredentialEdits()
	if err != nil {
		unlockConfig()
		t.Fatal(err)
	}
	current, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		unlockCredentials()
		unlockConfig()
		t.Fatal(err)
	}
	slot, err := current.StageModelCredentialLocked("temporary-secret")
	if err == nil {
		current.CleanupStagedModelCredentialsLocked(path)
	}
	unlockCredentials()
	unlockConfig()
	if err != nil {
		t.Fatal(err)
	}
	if CredentialStored(slot) {
		t.Fatalf("staged credential %q survived cleanup without a journal", slot)
	}
}

func TestRecoverModelCredentialCommitRetainsEvidenceAfterConcurrentConfigChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	path := UserConfigPath()
	cfg := Default()
	cfg.Providers = []ProviderEntry{{Name: "one", Kind: "openai", BaseURL: "https://one.invalid/v1", Model: "chat", APIKeyEnv: "OLD_KEY"}}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserCredentialsPath(), []byte("OLD_KEY=old\nNEW_SLOT=new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	updated := Default()
	updated.Providers = []ProviderEntry{{Name: "one", Kind: "openai", BaseURL: "https://one.invalid/v1", Model: "chat", APIKeyEnv: "NEW_SLOT"}}
	if err := updated.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	committedRevision := fileContentRevision(path)
	id := "concurrent-change"
	journal := &modelCredentialCommitJournal{
		Schema: modelCredentialCommitSchema, TransactionID: id, RequestID: id, RequestDigest: "digest",
		ConfigPath: path, BeforeRevision: "before", AfterRevision: committedRevision,
		Slots: []string{"NEW_SLOT"}, Phase: "config_committed",
		journalPath: filepath.Join(modelCredentialTransactionDir(), id+".json"),
	}
	if err := writeModelCredentialJournal(journal); err != nil {
		t.Fatal(err)
	}

	// An external writer wins after the connection commit but before recovery.
	external := Default()
	external.Language = "en"
	external.Providers = []ProviderEntry{{Name: "one", Kind: "openai", BaseURL: "https://one.invalid/v1", Model: "chat", APIKeyEnv: "OLD_KEY"}}
	if err := external.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	unlockConfig := LockUserConfigEdits()
	unlockCredentials, err := LockUserCredentialEdits()
	if err != nil {
		unlockConfig()
		t.Fatal(err)
	}
	err = RecoverModelCredentialCommitsLocked(path)
	unlockCredentials()
	unlockConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := LookupModelSettingsReceipt(id); ok {
		t.Fatal("concurrent config change must not be converted into a success receipt")
	}
	if _, err := os.Stat(journal.journalPath); err != nil {
		t.Fatalf("ambiguous transaction evidence was not retained: %v", err)
	}
	if !CredentialStored("NEW_SLOT") {
		t.Fatal("ambiguous transaction slot was removed")
	}
	saved, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := saved.Provider("one")
	if provider.APIKeyEnv != "OLD_KEY" || saved.Language != "en" {
		t.Fatalf("recovery overwrote the external config: %+v", saved)
	}
}

func TestCommittedModelCredentialSlotsAcceptExplicitEmptySlot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	path := UserConfigPath()
	cfg := Default()
	cfg.Providers = []ProviderEntry{{Name: "one", Kind: "openai", BaseURL: "https://one.invalid/v1", Model: "chat", APIKeyEnv: "EMPTY_SLOT"}}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserCredentialsPath(), []byte("EMPTY_SLOT=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	committed, err := committedModelCredentialSlots(path, []string{"EMPTY_SLOT"})
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("an explicit empty credential slot must remain a valid committed clear")
	}
}
