//go:build !windows

package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lockedCredentialStore writes a global store the process cannot read. The
// Windows branch of the reader is selected explicitly; a mode-0 file is the
// portable stand-in for a deny ACE.
func lockedCredentialStore(t *testing.T) string {
	t.Helper()
	setRuntimeGOOS(t, "windows")
	t.Setenv("REASONIX_HOME", t.TempDir())
	t.Setenv("RECOVERED_KEY", "")
	path := UserCredentialsPath()
	if err := os.WriteFile(path, []byte("EXISTING_KEY=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this user can read a mode-0 file; cannot simulate a locked store")
	}
	return path
}

func stubCredentialRecovery(t *testing.T, reset func(string) error, quarantine func(string) (string, error)) {
	t.Helper()
	prevReset, prevQuarantine := credentialStoreReset, credentialStoreQuarantine
	credentialStoreReset, credentialStoreQuarantine = reset, quarantine
	t.Cleanup(func() { credentialStoreReset, credentialStoreQuarantine = prevReset, prevQuarantine })
}

func TestCredentialReadKeepsPermissionErrorAndNeverResets(t *testing.T) {
	path := lockedCredentialStore(t)
	stubCredentialRecovery(t,
		func(string) error { t.Fatal("plain read must not reset the ACL"); return nil },
		func(string) (string, error) { t.Fatal("plain read must not quarantine the store"); return "", nil })
	if _, err := readCredentialFile(path); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("read error = %v, want the original permission error", err)
	}
	if revision := CredentialStoreRevision(); revision != "unreadable" {
		t.Fatalf("revision = %q, want unreadable", revision)
	}
}

func TestCredentialSaveResetsLockedStoreACL(t *testing.T) {
	path := lockedCredentialStore(t)
	stubCredentialRecovery(t,
		func(p string) error { return os.Chmod(p, 0o600) },
		func(string) (string, error) {
			t.Fatal("quarantine must not run when the reset succeeds")
			return "", nil
		})
	if _, err := SetCredential("RECOVERED_KEY", "new"); err != nil {
		t.Fatalf("SetCredential after ACL reset: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "EXISTING_KEY=old") || !strings.Contains(got, "RECOVERED_KEY=new") {
		t.Fatalf("store after reset = %q, want existing and new keys", got)
	}
}

func TestCredentialSaveQuarantinesUnrecoverableStore(t *testing.T) {
	path := lockedCredentialStore(t)
	stubCredentialRecovery(t, func(string) error { return errors.New("WRITE_DAC denied") }, quarantineCredentialStore)
	if _, err := SetCredential("RECOVERED_KEY", "new"); err != nil {
		t.Fatalf("SetCredential after quarantine: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); strings.Contains(got, "EXISTING_KEY") || !strings.Contains(got, "RECOVERED_KEY=new") {
		t.Fatalf("fresh store = %q, want only the new key", got)
	}
	quarantined, _ := filepath.Glob(path + ".locked-*")
	if len(quarantined) != 1 {
		t.Fatalf("quarantined copies = %v, want exactly one", quarantined)
	}
	t.Cleanup(func() { _ = os.Chmod(quarantined[0], 0o600) })
}

func TestCredentialSaveReportsBothFailuresWhenNothingRecovers(t *testing.T) {
	lockedCredentialStore(t)
	stubCredentialRecovery(t,
		func(string) error { return errors.New("WRITE_DAC denied") },
		func(string) (string, error) { return "", errors.New("rename denied") })
	_, err := SetCredential("RECOVERED_KEY", "new")
	if err == nil || !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "rename denied") {
		t.Fatalf("SetCredential error = %v, want the permission error with the quarantine failure", err)
	}
}
