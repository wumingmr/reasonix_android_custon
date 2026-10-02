package config

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"reasonix/internal/fileutil"
)

const modelCredentialCommitSchema = 1

type modelCredentialCommitJournal struct {
	Schema         int      `json:"schema"`
	TransactionID  string   `json:"transactionId"`
	RequestID      string   `json:"requestId,omitempty"`
	RequestDigest  string   `json:"requestDigest,omitempty"`
	ConfigPath     string   `json:"configPath"`
	BeforeRevision string   `json:"beforeRevision"`
	AfterRevision  string   `json:"afterRevision,omitempty"`
	ResultRevision string   `json:"resultRevision,omitempty"`
	Slots          []string `json:"slots"`
	// SlotDigests: slot -> keyed digest of the staged value; removal needs the store to still hold it.
	SlotDigests map[string]string    `json:"slotDigests,omitempty"`
	Rotations   []credentialRotation `json:"rotations,omitempty"`
	Phase       string               `json:"phase"`
	UpdatedAt   string               `json:"updatedAt"`
	journalPath string
}

// ModelSettingsReceipt is durable evidence that a request crossed the config
// publication point. It intentionally contains no credential value or config
// snapshot.
type ModelSettingsReceipt struct {
	Schema         int    `json:"schema"`
	RequestID      string `json:"requestId"`
	RequestDigest  string `json:"requestDigest"`
	ConfigPath     string `json:"configPath"`
	BeforeRevision string `json:"beforeRevision"`
	AfterRevision  string `json:"afterRevision"`
	ResultRevision string `json:"resultRevision,omitempty"`
	CommittedAt    string `json:"committedAt"`
}

func modelCredentialTransactionDir() string {
	home := ReasonixHomeDir()
	if strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, "transactions", "model-credentials")
}

func modelSettingsReceiptDir() string {
	home := ReasonixHomeDir()
	if strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, "transactions", "model-settings-receipts")
}

func modelSettingsReceiptPath(requestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(requestID)))
	return filepath.Join(modelSettingsReceiptDir(), hex.EncodeToString(sum[:])+".json")
}

func fileContentRevision(path string) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "unreadable"
	}
	revision, err := modelConfigContentRevision(raw)
	if err != nil {
		return "unreadable"
	}
	return revision
}

func modelSettingsDigestKey() ([]byte, error) {
	dir := modelSettingsReceiptDir()
	if dir == "" {
		return nil, fmt.Errorf("receipt store unavailable")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "request-digest.key")
	key, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := fileutil.AtomicCreateFile(path, key, 0600); err != nil && !os.IsExist(err) {
			return nil, err
		}
		key, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid receipt digest key")
	}
	return key, nil
}

func modelConfigContentRevision(raw []byte) (string, error) {
	key, err := modelSettingsDigestKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)), nil
}

// ModelSettingsRequestDigest survives process restarts without exposing an
// unkeyed digest of user-entered secrets in durable receipts.
func ModelSettingsRequestDigest(raw []byte) (string, error) {
	key, err := modelSettingsDigestKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return "hmac-v1:" + hex.EncodeToString(mac.Sum(nil)), nil
}

// Persist the exact publication candidate before touching the config. Recovery
// must never derive a success revision from whatever an external editor left.
func (c *Config) publishModelConfigBytes(path string, raw []byte, perm os.FileMode) error {
	if j := c.modelCredentialCommit; j != nil {
		if fileContentRevision(j.ConfigPath) != j.BeforeRevision {
			return fmt.Errorf("model settings changed before publication")
		}
		revision, err := modelConfigContentRevision(raw)
		if err != nil {
			return err
		}
		j.AfterRevision = revision
		j.Phase = "config_prepared"
		if err := writeModelCredentialJournal(j); err != nil {
			return err
		}
	}
	return fileutil.AtomicWriteFileStrict(path, raw, perm)
}

func (c *Config) writeModelConfigResolved(path, body string, perm os.FileMode) error {
	if c.modelCredentialCommit == nil {
		return writeConfigFileResolved(path, body, perm)
	}
	if err := finalizeOpenCodeGoJournal(path); err != nil {
		return err
	}
	return c.publishModelConfigBytes(path, []byte(body), perm)
}

func newModelCredentialTransactionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func writeModelCredentialJournal(j *modelCredentialCommitJournal) error {
	if j == nil || strings.TrimSpace(j.journalPath) == "" {
		return fmt.Errorf("model credential transaction store unavailable")
	}
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	dir := filepath.Dir(j.journalPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(j.journalPath, append(raw, '\n'), 0o600)
}

// BeginModelCredentialCommitLocked starts a crash-recoverable connection edit.
// The caller must hold the config lock followed by the credential lock.
func (c *Config) BeginModelCredentialCommitLocked(configPath, requestID string, requestDigest ...string) error {
	if c == nil {
		return fmt.Errorf("begin model credential commit: nil config")
	}
	dir := modelCredentialTransactionDir()
	if dir == "" {
		return fmt.Errorf("model credential transaction store unavailable")
	}
	if err := RecoverModelCredentialCommitsLocked(configPath); err != nil {
		return err
	}
	id, err := newModelCredentialTransactionID()
	if err != nil {
		return err
	}
	digest := ""
	if len(requestDigest) > 0 {
		digest = strings.TrimSpace(requestDigest[0])
	}
	c.modelCredentialCommit = &modelCredentialCommitJournal{
		Schema: modelCredentialCommitSchema, TransactionID: id, RequestID: strings.TrimSpace(requestID),
		RequestDigest: digest,
		ConfigPath:    filepath.Clean(configPath), BeforeRevision: fileContentRevision(configPath), Phase: "prepared",
		journalPath: filepath.Join(dir, id+".json"),
	}
	return writeModelCredentialJournal(c.modelCredentialCommit)
}

// StageModelCredential creates a private reference before the config commit.
// Callers hold both config and credential edit locks and defer cleanup
// through validation and persistence. No existing credential is overwritten.
func (c *Config) StageModelCredentialLocked(value string) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return c.stageModelCredentialLocked(fmt.Sprintf("REASONIX_CONNECTION_%X_KEY", id), value)
}

func (c *Config) stageModelCredentialLocked(key, value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("credential value contains a newline")
	}
	if j := c.modelCredentialCommit; j != nil {
		digest, err := stagedCredentialDigest(value)
		if err != nil {
			return "", err
		}
		if j.SlotDigests == nil {
			j.SlotDigests = map[string]string{}
		}
		j.Slots = append(j.Slots, key)
		j.SlotDigests[key] = digest
		j.Phase = "prepared"
		if err := writeModelCredentialJournal(j); err != nil {
			j.Slots = j.Slots[:len(j.Slots)-1]
			delete(j.SlotDigests, key)
			return "", err
		}
	}
	if _, err := storeCredentialAssignmentsLocked(map[string]string{key: value}); err != nil {
		return "", err
	}
	c.stagedModelCredentials = append(c.stagedModelCredentials, key)
	if c.modelCredentialCommit != nil {
		c.modelCredentialCommit.Phase = "credential_written"
		if err := writeModelCredentialJournal(c.modelCredentialCommit); err != nil {
			return "", err
		}
	}
	return key, nil
}

// MarkModelCredentialConfigCommittedLocked records the config publication
// point. CompleteModelCredentialCommitLocked removes the recovery evidence only
// after the caller has reread and validated the saved connection.
func (c *Config) MarkModelCredentialConfigCommittedLocked(path string, resultRevision ...string) error {
	if c == nil || c.modelCredentialCommit == nil {
		return nil
	}
	actual := fileContentRevision(path)
	if c.modelCredentialCommit.AfterRevision == "" || actual != c.modelCredentialCommit.AfterRevision {
		return fmt.Errorf("config publication could not be confirmed")
	}
	if len(resultRevision) > 0 {
		c.modelCredentialCommit.ResultRevision = strings.TrimSpace(resultRevision[0])
	}
	c.modelCredentialCommit.Phase = "config_committed"
	return writeModelCredentialJournal(c.modelCredentialCommit)
}

func (c *Config) CompleteModelCredentialCommitLocked() error {
	if c == nil || c.modelCredentialCommit == nil {
		return nil
	}
	j := c.modelCredentialCommit
	path := j.journalPath
	if j.Phase == "config_committed" && j.RequestID != "" && j.RequestDigest != "" {
		if err := persistModelSettingsReceipt(j); err != nil {
			return err
		}
	}
	if len(j.Rotations) > 0 {
		if err := dropRotationBackups(j); err != nil {
			return err
		}
	}
	c.modelCredentialCommit = nil
	c.stagedModelCredentials = nil
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CleanupStagedModelCredentials only inspects references minted by this edit.
// Both edit locks still belong to the caller. An uncertain read or failed
// cleanup conservatively leaves an orphan; it never damages the old connection.
func (c *Config) CleanupStagedModelCredentialsLocked(path string) {
	if c == nil {
		return
	}
	if j := c.modelCredentialCommit; j != nil && (j.Phase == "config_committed" || fileContentRevision(path) != j.BeforeRevision) {
		return // Publication or external edit: neither slots nor evidence are ours to remove.
	}
	if j := c.modelCredentialCommit; j != nil && len(j.Rotations) > 0 {
		if restoreRotations(j) != nil || writeModelCredentialJournal(j) != nil {
			return // Keep the journal so recovery can finish the restore.
		}
	}
	if len(c.stagedModelCredentials) == 0 {
		if c.modelCredentialCommit != nil && len(c.modelCredentialCommit.Slots) == 0 {
			_ = os.Remove(c.modelCredentialCommit.journalPath)
			c.modelCredentialCommit = nil
		}
		return
	}
	referenced := false
	for _, key := range c.stagedModelCredentials {
		digest := ""
		if c.modelCredentialCommit != nil {
			digest = c.modelCredentialCommit.SlotDigests[key]
		}
		if configReferencesCredential(path, key) {
			referenced = true
		} else if !stagedValueUnchanged(key, digest) {
			continue // Another writer owns the slot now.
		} else {
			if err := removeCredentialFromFile(UserCredentialsPath(), key); err == nil {
				_ = os.Unsetenv(key)
			} else {
				return // Keep the journal and staged list so cleanup can be retried.
			}
		}
	}
	c.stagedModelCredentials = nil
	if c.modelCredentialCommit != nil && !referenced {
		_ = os.Remove(c.modelCredentialCommit.journalPath)
		c.modelCredentialCommit = nil
	}
}

func persistModelSettingsReceipt(j *modelCredentialCommitJournal) error {
	if j == nil || j.RequestID == "" || j.RequestDigest == "" || j.AfterRevision == "" {
		return nil
	}
	dir := modelSettingsReceiptDir()
	if dir == "" {
		return fmt.Errorf("model settings receipt store unavailable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	receipt := ModelSettingsReceipt{
		Schema: modelCredentialCommitSchema, RequestID: j.RequestID, RequestDigest: j.RequestDigest,
		ConfigPath: j.ConfigPath, BeforeRevision: j.BeforeRevision, AfterRevision: j.AfterRevision,
		ResultRevision: j.ResultRevision,
		CommittedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(modelSettingsReceiptPath(j.RequestID), append(raw, '\n'), 0o600)
}

// LookupModelSettingsReceipt returns durable commit evidence for requestID.
// A malformed or mismatched file is treated as unavailable evidence.
func LookupModelSettingsReceipt(requestID string) (ModelSettingsReceipt, bool) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ModelSettingsReceipt{}, false
	}
	raw, err := os.ReadFile(modelSettingsReceiptPath(requestID))
	if err != nil {
		return ModelSettingsReceipt{}, false
	}
	var receipt ModelSettingsReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Schema != modelCredentialCommitSchema || receipt.RequestID != requestID || receipt.RequestDigest == "" || receipt.AfterRevision == "" {
		return ModelSettingsReceipt{}, false
	}
	return receipt, true
}

// RecoverModelSettingsReceipt is the unlocked host query entry point. Receipt
// queries after a restart must finish provable publications before returning
// unknown_result; callers already holding edit locks use Lookup directly.
func RecoverModelSettingsReceipt(requestID string) (ModelSettingsReceipt, bool) {
	if receipt, ok := LookupModelSettingsReceipt(requestID); ok {
		return receipt, true
	}
	entries, err := os.ReadDir(modelCredentialTransactionDir())
	if err != nil {
		return ModelSettingsReceipt{}, false
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(modelCredentialTransactionDir(), entry.Name()))
		if err != nil {
			continue
		}
		var j modelCredentialCommitJournal
		if json.Unmarshal(raw, &j) != nil || j.Schema != modelCredentialCommitSchema || j.RequestID != strings.TrimSpace(requestID) || j.ConfigPath == "" {
			continue
		}
		func() {
			unlock, err := LockConfigFileEdits(j.ConfigPath)
			if err != nil {
				return
			}
			defer unlock()
			unlockCredentials, err := LockUserCredentialEdits()
			if err != nil {
				return
			}
			defer unlockCredentials()
			_ = RecoverModelCredentialCommitsLocked(j.ConfigPath)
		}()
	}
	return LookupModelSettingsReceipt(requestID)
}

func committedModelCredentialSlots(configPath string, slots []string) (bool, error) {
	if len(slots) == 0 {
		return true, nil
	}
	cfg, err := LoadForEditReadOnlyStrict(configPath)
	if err != nil {
		return false, err
	}
	referenced := make(map[string]bool, len(slots))
	for _, provider := range cfg.Providers {
		referenced[strings.TrimSpace(provider.APIKeyEnv)] = true
	}
	credentialPath := UserCredentialsPath()
	for _, slot := range slots {
		slot = strings.TrimSpace(slot)
		if slot == "" || !referenced[slot] {
			return false, nil
		}
		if _, exists := envFileValue(credentialPath, slot); !exists && !envFileHasClearedKey(credentialPath, slot) {
			return false, nil
		}
	}
	return true, nil
}

// RecoverModelCredentialCommitsLocked resolves interrupted edits for one
// config target without replaying writes or overwriting newer config content.
func RecoverModelCredentialCommitsLocked(configPath string) error {
	dir := modelCredentialTransactionDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	configPath = filepath.Clean(configPath)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		journalPath := filepath.Join(dir, entry.Name())
		raw, readErr := os.ReadFile(journalPath)
		if readErr != nil {
			return readErr
		}
		var j modelCredentialCommitJournal
		if json.Unmarshal(raw, &j) != nil || j.Schema != modelCredentialCommitSchema || filepath.Clean(j.ConfigPath) != configPath {
			continue
		}
		if _, statErr := os.Stat(configPath); statErr != nil && !os.IsNotExist(statErr) {
			continue
		}
		anyReferenced := slices.ContainsFunc(j.Slots, func(slot string) bool { return configReferencesCredential(configPath, slot) })
		committed := false
		if anyReferenced || j.AfterRevision != "" {
			var committedErr error
			committed, committedErr = committedModelCredentialSlots(configPath, j.Slots)
			if committedErr != nil {
				continue
			}
		}
		if j.AfterRevision != "" {
			if fileContentRevision(configPath) != j.AfterRevision || !committed {
				if fileContentRevision(configPath) != j.BeforeRevision {
					continue
				}
			} else {
				if err := persistModelSettingsReceipt(&j); err != nil {
					return err
				}
				if err := dropRotationBackups(&j); err != nil {
					return err
				}
				if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
					return err
				}
				continue
			}
		}
		if anyReferenced {
			// A partial or unparseable reference is ambiguous. Preserve both the
			// journal and slots for explicit diagnosis instead of deleting data.
			continue
		}
		if fileContentRevision(configPath) != j.BeforeRevision {
			continue
		}
		if err := removeUnpublishedSlots(&j); err != nil {
			return err
		}
		if err := restoreRotations(&j); err != nil {
			return err
		}
		if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// removeUnpublishedSlots skips a slot whose value is no longer the staged one:
// another writer owns it now.
func removeUnpublishedSlots(j *modelCredentialCommitJournal) error {
	for _, slot := range j.Slots {
		if !stagedValueUnchanged(slot, j.SlotDigests[slot]) {
			continue
		}
		if err := removeCredentialFromFile(UserCredentialsPath(), slot); err != nil {
			return err
		}
		_ = os.Unsetenv(slot)
	}
	return nil
}
