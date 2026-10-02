package config

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
)

// credentialRotation is a key replaced in place. The previous value is kept in
// the credential store under Backup, never in the journal, so it has the same
// protection as every other stored key.
type credentialRotation struct {
	Slot   string `json:"slot"`
	Backup string `json:"backup"`
	Digest string `json:"digest"` // keyed digest of the value this edit wrote
}

// RotateModelCredentialLocked stores a new key for providers. When they are
// the only readers of the variable they share and the store holds its value,
// the variable is rewritten in place; otherwise the key gets a private slot as
// StageModelCredentialLocked does. Callers hold both edit locks.
func (c *Config) RotateModelCredentialLocked(providers []string, value string) (string, error) {
	key, ok := c.rotatableCredentialKey(providers)
	if !ok {
		return c.StageModelCredentialLocked(value)
	}
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("credential value contains a newline")
	}
	previous, _ := envFileValue(UserCredentialsPath(), key)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	digest, err := stagedCredentialDigest(value)
	if err != nil {
		return "", err
	}
	j := c.modelCredentialCommit
	rotation := credentialRotation{Slot: key, Backup: fmt.Sprintf("REASONIX_ROTATION_%X_KEY", id), Digest: digest}
	j.Rotations = append(j.Rotations, rotation)
	j.Phase = "prepared"
	if err := writeModelCredentialJournal(j); err != nil {
		j.Rotations = j.Rotations[:len(j.Rotations)-1]
		return "", err
	}
	// One file write: a crash leaves either both values or neither.
	if err := storeCredentialsInFile(UserCredentialsPath(), map[string]string{rotation.Backup: previous, key: value}); err != nil {
		return "", err
	}
	pinCredentialAssignments(map[string]string{key: value})
	j.Phase = "credential_written"
	if err := writeModelCredentialJournal(j); err != nil {
		return "", err
	}
	return key, nil
}

// rotatableCredentialKey names the variable providers share when rewriting it
// changes what no other reader in the user config resolves, both in this edit
// and on disk. A project file cannot see the user config, so it never rotates.
func (c *Config) rotatableCredentialKey(providers []string) (string, bool) {
	if c == nil || c.modelCredentialCommit == nil || len(providers) == 0 || !IsUserConfigPath(c.modelCredentialCommit.ConfigPath) {
		return "", false
	}
	key, ok := sharedCredentialKey(c, providers)
	if !ok || !isCredentialKey(key) || !onlyCredentialReaders(c, key, providers) || rotatedInThisEdit(c.modelCredentialCommit, key) {
		return "", false
	}
	before, err := LoadForEditWithoutCredentialsReadOnlyStrict(c.modelCredentialCommit.ConfigPath)
	if err != nil {
		return "", false
	}
	if held, ok := sharedCredentialKey(before, providers); !ok || held != key || !onlyCredentialReaders(before, key, providers) {
		return "", false
	}
	// The store overrides the shell at load, so a stored value is what every
	// in-process reader resolves.
	file, ok := readDotEnvFile(UserCredentialsPath())
	if _, stored := file.Values[key]; !ok || !stored {
		return "", false
	}
	for name := range file.Values {
		if name != key && sameCredentialName(name, key) {
			return "", false // Windows would load either spelling into one variable.
		}
	}
	return key, true
}

func sharedCredentialKey(c *Config, providers []string) (string, bool) {
	key := ""
	for _, name := range providers {
		entry, ok := c.Provider(strings.TrimSpace(name))
		if !ok {
			return "", false
		}
		env := strings.TrimSpace(entry.APIKeyEnv)
		if env == "" || (key != "" && env != key) {
			return "", false
		}
		key = env
	}
	return key, key != ""
}

func onlyCredentialReaders(c *Config, key string, providers []string) bool {
	for _, p := range c.Providers {
		if sameCredentialName(strings.TrimSpace(p.APIKeyEnv), key) && !containsTrimmed(providers, p.Name) {
			return false
		}
	}
	others := *c
	others.Providers = nil
	for _, name := range credentialEnvNamesFromConfig(&others) {
		if sameCredentialName(name, key) {
			return false
		}
	}
	return true
}

// rotatedInThisEdit keeps one backup per variable: a second would hold this
// edit's own value, and restoring it would lose the original.
func rotatedInThisEdit(j *modelCredentialCommitJournal, key string) bool {
	for _, r := range j.Rotations {
		if sameCredentialName(r.Slot, key) {
			return true
		}
	}
	return false
}

func containsTrimmed(names []string, want string) bool {
	for _, name := range names {
		if strings.TrimSpace(name) == want {
			return true
		}
	}
	return false
}

// restoreRotations puts the previous value back only while the variable still
// holds what this edit wrote; a value another writer stored stays.
func restoreRotations(j *modelCredentialCommitJournal) error {
	path := UserCredentialsPath()
	for _, r := range j.Rotations {
		previous, hasBackup := envFileValue(path, r.Backup)
		if hasBackup && stagedValueUnchanged(r.Slot, r.Digest) {
			if err := storeCredentialsInFile(path, map[string]string{r.Slot: previous}); err != nil {
				return err
			}
			pinCredentialAssignments(map[string]string{r.Slot: previous})
		}
		if err := dropRotationBackup(path, r.Backup, hasBackup); err != nil {
			return err
		}
	}
	j.Rotations = nil
	return nil
}

// dropRotationBackups settles a published rotation: the new value stays.
func dropRotationBackups(j *modelCredentialCommitJournal) error {
	path := UserCredentialsPath()
	for _, r := range j.Rotations {
		_, hasBackup := envFileValue(path, r.Backup)
		if err := dropRotationBackup(path, r.Backup, hasBackup); err != nil {
			return err
		}
	}
	j.Rotations = nil
	return nil
}

// dropRotationBackup leaves no cleared marker: nothing else writes a random
// backup name, so a marker would only grow the file on every rotation.
func dropRotationBackup(path, backup string, stored bool) error {
	if stored {
		lines, err := readCredentialFileLinesForWrite(path)
		if err != nil {
			return err
		}
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			if k, ok := credentialLineKey(line); !ok || k != backup {
				kept = append(kept, line)
			}
		}
		if err := writeCredentialFileLines(path, kept); err != nil {
			return err
		}
	}
	_ = os.Unsetenv(backup)
	return nil
}
