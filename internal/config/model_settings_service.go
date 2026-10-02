package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ConnectionCredentialRequest is the shared CLI/Desktop credential mutation.
// ExpectedRevision is optional for compatibility; interactive editors should
// supply the revision they loaded to avoid overwriting a concurrent edit.
type ConnectionCredentialRequest struct {
	RequestID        string
	ConfigPath       string
	ProviderNames    []string
	Key              string
	ExpectedRevision string
}

type ConnectionCredentialResult struct {
	Persisted bool
	Revision  string
	Slot      string
}

func connectionCredentialRequestDigest(req ConnectionCredentialRequest) (string, error) {
	raw, err := json.Marshal(struct {
		ConfigPath    string
		ProviderNames []string
		Key           string
		Revision      string
	}{filepath.Clean(req.ConfigPath), req.ProviderNames, req.Key, req.ExpectedRevision})
	if err != nil {
		return "", err
	}
	return ModelSettingsRequestDigest(raw)
}

func ConfigFileRevision(path string) string { return fileContentRevision(path) }

func connectionCredentialReceiptResult(receipt ModelSettingsReceipt, digest string) (ConnectionCredentialResult, error) {
	if !strings.HasPrefix(receipt.RequestDigest, "hmac-v1:") {
		return ConnectionCredentialResult{}, fmt.Errorf("unknown_result: legacy receipt content cannot be verified; reload current settings")
	}
	if receipt.RequestDigest != digest {
		return ConnectionCredentialResult{}, fmt.Errorf("request_conflict: request ID was already used for a different connection edit")
	}
	revision := receipt.ResultRevision
	if revision == "" {
		revision = receipt.AfterRevision
	}
	return ConnectionCredentialResult{Persisted: true, Revision: revision}, nil
}

// ProviderEditPath follows the source selected by the runtime merge, including
// project entries that replace built-in defaults but not user-owned entries.
func (c *Config) ProviderEditPath(root, name string) (string, error) {
	entry, ok := c.Provider(name)
	if !ok {
		return "", fmt.Errorf("unknown provider %q", name)
	}
	if c.providerSources[providerMergeKey(*entry)] == providerSourceProject {
		return filepath.Join(root, "reasonix.toml"), nil
	}
	return UserConfigPath(), nil
}

// CommitConnectionCredential writes the key before publishing the config
// reference: in place when its providers alone in the user config read their
// stored variable, otherwise to a fresh private slot.
func CommitConnectionCredential(req ConnectionCredentialRequest) (ConnectionCredentialResult, error) {
	var result ConnectionCredentialResult
	path := strings.TrimSpace(req.ConfigPath)
	if path == "" {
		return result, fmt.Errorf("config path is required")
	}
	if len(req.ProviderNames) == 0 {
		return result, fmt.Errorf("at least one provider is required")
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return result, fmt.Errorf("request ID is required")
	}
	if strings.ContainsAny(req.Key, "\r\n") {
		return result, fmt.Errorf("credential value contains a newline")
	}
	digest, err := connectionCredentialRequestDigest(req)
	if err != nil {
		return result, err
	}
	if receipt, ok := LookupModelSettingsReceipt(strings.TrimSpace(req.RequestID)); ok {
		return connectionCredentialReceiptResult(receipt, digest)
	}
	unlock, err := LockConfigFileEdits(path)
	if err != nil {
		return result, err
	}
	defer unlock()
	unlockCredentials, err := LockUserCredentialEdits()
	if err != nil {
		return result, err
	}
	defer unlockCredentials()
	if err := RecoverModelCredentialCommitsLocked(path); err != nil {
		return result, err
	}
	if receipt, ok := LookupModelSettingsReceipt(strings.TrimSpace(req.RequestID)); ok {
		return connectionCredentialReceiptResult(receipt, digest)
	}
	if req.ExpectedRevision != "" && fileContentRevision(path) != req.ExpectedRevision {
		return result, fmt.Errorf("model settings changed; reload before saving")
	}
	cfg, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		return result, err
	}
	if err := cfg.BeginModelCredentialCommitLocked(path, req.RequestID, digest); err != nil {
		return result, err
	}
	defer cfg.CleanupStagedModelCredentialsLocked(path)
	baseline := cfg.ModelSettingsBaseline()
	slot, err := cfg.RotateModelCredentialLocked(req.ProviderNames, req.Key)
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, rawName := range req.ProviderNames {
		name := strings.TrimSpace(rawName)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		entry, ok := cfg.Provider(name)
		if !ok {
			return result, fmt.Errorf("unknown provider %q", name)
		}
		updated := *entry
		updated.APIKeyEnv = slot
		if err := cfg.UpsertProvider(updated); err != nil {
			return result, err
		}
	}
	if len(seen) == 0 {
		return result, fmt.Errorf("at least one provider is required")
	}
	err = cfg.SaveModelSettingsTo(path, baseline)
	if err != nil {
		return result, err
	}
	result.Persisted = true
	result.Slot = slot
	result.Revision = fileContentRevision(path)
	if err := cfg.MarkModelCredentialConfigCommittedLocked(path, result.Revision); err != nil {
		return result, err
	}
	saved, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		return result, err
	}
	for name := range seen {
		entry, ok := saved.Provider(name)
		if !ok || entry.APIKeyEnv != slot {
			return result, fmt.Errorf("saved provider %q did not reference the new credential", name)
		}
	}
	if err := cfg.CompleteModelCredentialCommitLocked(); err != nil {
		return result, err
	}
	return result, nil
}
