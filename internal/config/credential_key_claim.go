package config

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// ErrCredentialKeyInUse marks a named credential variable that a connection
// may not claim; CredentialKeyInUseError says who holds it.
var ErrCredentialKeyInUse = errors.New("credential variable already in use")

// CredentialKeyHolder names why a variable is unavailable.
type CredentialKeyHolder uint8

const (
	// CredentialKeyHeldByProvider: another provider entry references the variable.
	CredentialKeyHeldByProvider CredentialKeyHolder = iota + 1
	// CredentialKeyHeldBySetting: a bot or remote-host setting reads the variable.
	CredentialKeyHeldBySetting
	// CredentialKeyHeldByStore: the credential store already has a value or tombstone for it.
	CredentialKeyHeldByStore
	// CredentialKeyHeldByEnvironment: the process environment sets it from outside the store.
	CredentialKeyHeldByEnvironment
)

type CredentialKeyInUseError struct {
	Key      string
	Holder   CredentialKeyHolder
	Provider string // set when Holder is CredentialKeyHeldByProvider
}

func (e *CredentialKeyInUseError) Error() string {
	switch e.Holder {
	case CredentialKeyHeldByProvider:
		return fmt.Sprintf("%s is referenced by provider %q", e.Key, e.Provider)
	case CredentialKeyHeldBySetting:
		return fmt.Sprintf("%s is read by a bot or remote-host setting", e.Key)
	case CredentialKeyHeldByEnvironment:
		return fmt.Sprintf("%s is already set in the environment", e.Key)
	default:
		return fmt.Sprintf("%s already has a stored credential", e.Key)
	}
}

func (e *CredentialKeyInUseError) Unwrap() error { return ErrCredentialKeyInUse }

// sameCredentialName compares variable names the way the OS environment does.
func sameCredentialName(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func environmentHasName(key string) bool {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name != "" && sameCredentialName(name, key) {
			return true
		}
	}
	return false
}

// CredentialKeyClaimable reports whether provider may write its key under key
// without changing what any other reader of that variable resolves. A stored
// value or tombstone may belong to a config this one cannot see.
func (c *Config) CredentialKeyClaimable(key, provider string) error {
	key = strings.TrimSpace(key)
	if !isCredentialKey(key) {
		return fmt.Errorf("invalid credential key %q", key)
	}
	for _, p := range c.Providers {
		if p.Name != provider && sameCredentialName(strings.TrimSpace(p.APIKeyEnv), key) {
			return &CredentialKeyInUseError{Key: key, Holder: CredentialKeyHeldByProvider, Provider: p.Name}
		}
	}
	others := *c
	others.Providers = nil
	for _, name := range credentialEnvNamesFromConfig(&others) {
		if sameCredentialName(name, key) {
			return &CredentialKeyInUseError{Key: key, Holder: CredentialKeyHeldBySetting}
		}
	}
	if credentialCurrentStoreHasKey(key) || credentialCurrentStoreClearedKey(key) {
		return &CredentialKeyInUseError{Key: key, Holder: CredentialKeyHeldByStore}
	}
	if environmentHasName(key) {
		return &CredentialKeyInUseError{Key: key, Holder: CredentialKeyHeldByEnvironment}
	}
	return nil
}

// StageNamedModelCredentialLocked is StageModelCredentialLocked for a name the
// user chose. It refuses a name CredentialKeyClaimable rejects, so the no-
// overwrite and rollback guarantees of a fresh slot still hold.
func (c *Config) StageNamedModelCredentialLocked(key, provider, value string) (string, error) {
	if err := c.CredentialKeyClaimable(key, provider); err != nil {
		return "", err
	}
	return c.stageModelCredentialLocked(strings.TrimSpace(key), value)
}

// configReferencesCredential reports whether the config at path reads key.
// A config that cannot be parsed counts as referencing it: removal needs proof.
func configReferencesCredential(path, key string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	}
	cfg, err := LoadForEditWithoutCredentialsReadOnlyStrict(path)
	if err != nil {
		return true
	}
	for _, name := range credentialEnvNamesFromConfig(cfg) {
		if sameCredentialName(name, key) {
			return true
		}
	}
	return false
}

func stagedCredentialDigest(value string) (string, error) {
	return ModelSettingsRequestDigest([]byte(strings.TrimSpace(value)))
}

// stagedValueUnchanged reports whether slot still holds what this edit staged.
// No recorded digest means a journal from before digests existed.
func stagedValueUnchanged(slot, digest string) bool {
	if digest == "" {
		return true
	}
	value, ok := envFileValue(UserCredentialsPath(), slot)
	if !ok {
		return !envFileHasClearedKey(UserCredentialsPath(), slot)
	}
	got, err := stagedCredentialDigest(value)
	return err == nil && got == digest
}
