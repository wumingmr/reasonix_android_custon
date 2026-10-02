package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/fileutil"
)

const (
	copyReceiptName          = "copy-receipt.json"
	copyReceiptSchemaVersion = 1
)

// CopyRequest identifies one full-history copy. OperationID is durable
// idempotency authority: retrying it addresses the same child even after the
// source has accepted more events.
type CopyRequest struct {
	Source      SessionRef
	ChildID     string
	OperationID string
	CWD         string
}

type CopyResult struct {
	Child SessionRef
}

type copyReceipt struct {
	SchemaVersion   int       `json:"schemaVersion"`
	OperationID     string    `json:"operationId"`
	SourceSessionID string    `json:"sourceSessionId"`
	CreatedAt       time.Time `json:"createdAt"`
}

// CopySession publishes a complete immutable snapshot under a new identity.
// It does not attach a runtime and it never changes the source runtime.
func (s *Service) CopySession(ctx context.Context, request CopyRequest) (CopyResult, error) {
	if s == nil {
		return CopyResult{}, errors.New("session: nil service")
	}
	if err := request.Source.validate(s.hostID); err != nil {
		return CopyResult{}, err
	}
	operationID := strings.TrimSpace(request.OperationID)
	if operationID == "" {
		return CopyResult{}, errors.New("session: copy operation id is required")
	}
	childID := strings.TrimSpace(request.ChildID)
	if childID == "" {
		childID = deterministicID("copy\x00" + request.Source.SessionID + "\x00" + operationID)
	}
	if err := validateSessionID(childID); err != nil {
		return CopyResult{}, err
	}
	filesystem, ok := s.persistence.(*FilesystemPersistence)
	if !ok {
		return CopyResult{}, errors.New("session: persistence does not support filesystem copy")
	}
	childRef := SessionRef{HostID: s.hostID, SessionID: childID}
	childDir := filepath.Join(filesystem.Root, childID)
	if matched, err := copyChildMatches(childDir, request.Source.SessionID, operationID); err == nil {
		if matched {
			return CopyResult{Child: childRef}, nil
		}
		return CopyResult{}, fmt.Errorf("%w: %s", ErrSessionExists, childID)
	} else if !os.IsNotExist(err) {
		return CopyResult{}, err
	}

	container, err := os.MkdirTemp(filesystem.Root, ".copy-export-")
	if err != nil {
		return CopyResult{}, err
	}
	defer os.RemoveAll(container)
	bundle := filepath.Join(container, "bundle")
	if err := s.Export(ctx, request.Source, bundle); err != nil {
		return CopyResult{}, err
	}
	createdAt := time.Now().UTC()
	manifest, err := readManifest(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return CopyResult{}, err
	}
	manifest.CreatedAt = createdAt
	if err := writeManifestFile(filepath.Join(bundle, "manifest.json"), manifest); err != nil {
		return CopyResult{}, err
	}
	receipt := copyReceipt{
		SchemaVersion: copyReceiptSchemaVersion, OperationID: operationID,
		SourceSessionID: request.Source.SessionID, CreatedAt: createdAt,
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return CopyResult{}, err
	}
	if err := fileutil.AtomicWriteFileStrict(filepath.Join(bundle, copyReceiptName), append(body, '\n'), 0o600); err != nil {
		return CopyResult{}, err
	}
	_, err = s.ImportWithHeader(ctx, bundle, CreateOptions{
		SessionID: childID, CWD: request.CWD, ParentSessionID: request.Source.SessionID,
		Origin: SessionOriginCanonicalImport,
	})
	if err == nil {
		return CopyResult{Child: childRef}, nil
	}
	// Two retries can race through the preflight. The published receipt is the
	// durable proof that the winner belongs to this exact operation.
	if matched, readErr := copyChildMatches(childDir, request.Source.SessionID, operationID); readErr == nil && matched {
		return CopyResult{Child: childRef}, nil
	}
	return CopyResult{}, err
}

func copyChildMatches(childDir, sourceSessionID, operationID string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(childDir, copyReceiptName))
	if err != nil {
		return false, err
	}
	var receipt copyReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return false, err
	}
	return receipt.SchemaVersion == copyReceiptSchemaVersion &&
		receipt.SourceSessionID == strings.TrimSpace(sourceSessionID) &&
		receipt.OperationID == strings.TrimSpace(operationID), nil
}

// CopyOperationMatches proves that an already-published child belongs to the
// exact full-copy operation. Hosts use it to resume workspace attachment after
// a process stopped between storage publication and registry commit.
func (s *Service) CopyOperationMatches(ctx context.Context, child SessionRef, sourceSessionID, operationID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := child.validate(s.hostID); err != nil {
		return false, err
	}
	filesystem, ok := s.persistence.(*FilesystemPersistence)
	if !ok {
		return false, errors.New("session: persistence does not support filesystem copy")
	}
	return copyChildMatches(filepath.Join(filesystem.Root, child.SessionID), sourceSessionID, operationID)
}
