package session

import (
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
	sessionHeaderName          = "header.json"
	SessionHeaderSchemaVersion = 1
)

type SessionOrigin string

const (
	SessionOriginNew             SessionOrigin = "new"
	SessionOriginFork            SessionOrigin = "fork"
	SessionOriginCanonicalImport SessionOrigin = "canonical-v4-import"
	SessionOriginLegacyImport    SessionOrigin = "legacy-import"
)

// SessionHeader is immutable Desktop ownership metadata. It deliberately
// excludes display and model projections so Session remains their sole source.
type SessionHeader struct {
	SchemaVersion   int           `json:"schemaVersion"`
	SessionID       string        `json:"sessionId"`
	CreatedAt       time.Time     `json:"createdAt"`
	CWD             string        `json:"cwd"`
	ParentSessionID string        `json:"parentSessionId,omitempty"`
	Origin          SessionOrigin `json:"origin"`
}

func headerForCreate(options CreateOptions) (*SessionHeader, error) {
	cwd := strings.TrimSpace(options.CWD)
	parentID := strings.TrimSpace(options.ParentSessionID)
	origin := options.Origin
	if cwd == "" && parentID == "" && origin == "" {
		return nil, nil
	}
	if cwd != "" {
		cwd = filepath.Clean(cwd)
	}
	if parentID != "" {
		if err := validateSessionID(parentID); err != nil {
			return nil, fmt.Errorf("session: invalid parent identity: %w", err)
		}
	}
	if origin == "" {
		origin = SessionOriginNew
	}
	if !validSessionOrigin(origin) {
		return nil, fmt.Errorf("session: unsupported session origin %q", origin)
	}
	return &SessionHeader{
		SchemaVersion:   SessionHeaderSchemaVersion,
		SessionID:       strings.TrimSpace(options.SessionID),
		CWD:             cwd,
		ParentSessionID: parentID,
		Origin:          origin,
	}, nil
}

func validSessionOrigin(origin SessionOrigin) bool {
	switch origin {
	case SessionOriginNew, SessionOriginFork, SessionOriginCanonicalImport, SessionOriginLegacyImport:
		return true
	default:
		return false
	}
}

func writeSessionHeader(dir string, header SessionHeader) error {
	if header.SchemaVersion != SessionHeaderSchemaVersion || header.SessionID == "" || header.CreatedAt.IsZero() || !validSessionOrigin(header.Origin) {
		return fmt.Errorf("session: invalid session header")
	}
	body, err := json.Marshal(header)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(filepath.Join(dir, sessionHeaderName), append(body, '\n'), 0o600)
}

func writeSessionHeaderForCreate(dir, sessionID string, createdAt time.Time, options CreateOptions) error {
	options.SessionID = sessionID
	header, err := headerForCreate(options)
	if err != nil || header == nil {
		return err
	}
	header.CreatedAt = createdAt
	return writeSessionHeader(dir, *header)
}

func validateSessionHeaderForCreate(dir, sessionID string, options CreateOptions) error {
	expected, err := headerForCreate(options)
	if err != nil || expected == nil {
		return err
	}
	header, found, err := readSessionHeader(dir, sessionID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: session header is missing", ErrDamagedStore)
	}
	if header.CWD != expected.CWD {
		return fmt.Errorf("%w: session header workspace does not match requested ownership", ErrDamagedStore)
	}
	if header.ParentSessionID != expected.ParentSessionID {
		return fmt.Errorf("%w: session header lineage does not match requested ownership", ErrDamagedStore)
	}
	if header.Origin != expected.Origin {
		return fmt.Errorf("%w: session header origin does not match requested ownership", ErrDamagedStore)
	}
	return nil
}

func readSessionHeader(dir, sessionID string) (SessionHeader, bool, error) {
	body, err := os.ReadFile(filepath.Join(dir, sessionHeaderName))
	if errors.Is(err, os.ErrNotExist) {
		return SessionHeader{}, false, nil
	}
	if err != nil {
		return SessionHeader{}, false, err
	}
	return decodeSessionHeader(body, sessionID)
}

func decodeSessionHeader(body []byte, sessionID string) (SessionHeader, bool, error) {
	var header SessionHeader
	if err := json.Unmarshal(body, &header); err != nil {
		return SessionHeader{}, true, fmt.Errorf("%w: decode session header: %w", ErrDamagedStore, err)
	}
	if header.SchemaVersion != SessionHeaderSchemaVersion {
		return SessionHeader{}, true, fmt.Errorf("%w: session header version %d", ErrUnsupportedVersion, header.SchemaVersion)
	}
	if header.SessionID != sessionID || header.CreatedAt.IsZero() || !validSessionOrigin(header.Origin) {
		return SessionHeader{}, true, fmt.Errorf("%w: invalid session header", ErrDamagedStore)
	}
	return header, true, nil
}
