package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/fileutil"
	"reasonix/internal/provider"
)

// Rebuild authored previews, retracted input/turn metadata, title sequencing,
// and visible result sequencing.
const catalogMetadataVersion = 5

// metadataForDurable publishes catalog metadata only for a durable prefix.
func (s *Session) metadataForDurable(durable uint64) (catalogMetadata, bool) {
	if s == nil {
		return catalogMetadata{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if durable+1 != s.next {
		return catalogMetadata{}, false
	}
	return metadataFromProjection(s.manifest, durable, s.projection), true
}

const (
	MetadataReady   = "ready"
	MetadataPending = "pending"
	MetadataFailed  = "failed"
)

type catalogMetadata struct {
	Version        int    `json:"version"`
	Codec          string `json:"codec"`
	SessionID      string `json:"sessionId"`
	CreatedAt      string `json:"createdAt"`
	Sequence       uint64 `json:"sequence"`
	ResultSequence uint64 `json:"resultSequence,omitempty"`
	Title          string `json:"title,omitempty"`
	TitleSequence  uint64 `json:"titleSequence,omitempty"`
	ModelRef       string `json:"modelRef,omitempty"`
	ModelIdentity  string `json:"modelIdentity,omitempty"`
	Turns          int    `json:"turns"`
	Preview        string `json:"preview,omitempty"`
	// LogRevision pins the cache to the exact durable bytes it was built from.
	// Catalog listing validates this instead of replaying the log, so a page of
	// long sessions costs one stat per session rather than a full scan.
	LogSize      int64  `json:"logSize"`
	LogModTimeNS int64  `json:"logModTimeNs"`
	LogIdentity  string `json:"logIdentity,omitempty"`
}

func catalogMetadataPath(cacheDir string) string {
	return filepath.Join(cacheDir, "catalog-metadata.json")
}

func metadataFromProjection(manifest Manifest, sequence uint64, projection Projection) catalogMetadata {
	metadata := catalogMetadata{
		Version: catalogMetadataVersion, Codec: Codec, SessionID: manifest.SessionID,
		CreatedAt: manifest.CreatedAt.UTC().Format(time.RFC3339Nano), Sequence: sequence,
		Title: projection.Title, TitleSequence: projection.TitleSequence,
		ModelRef: projection.ModelRef, ModelIdentity: projection.ModelIdentity,
	}
	metadata.Turns = visibleBoundaryCount(projection, true)
	metadata.ResultSequence = latestVisibleResultSequence(projection)
	for _, input := range projection.TranscriptInputs {
		if input.Preview != "" {
			metadata.Preview = input.Preview
			return metadata
		}
	}
	if len(projection.TranscriptInputs) > 0 {
		return metadata
	}
	for _, message := range projection.Messages {
		if preview := catalogMessagePreview(message); preview != "" {
			metadata.Preview = preview
			break
		}
	}
	return metadata
}

func latestVisibleResultSequence(projection Projection) uint64 {
	var latest uint64
	for _, turn := range projection.Turns {
		if projection.HiddenTurns[turn.TurnID] || !turn.Status.Terminal() || turn.MessageID == "" {
			continue
		}
		latest = max(latest, turn.BoundarySequence)
	}
	return latest
}

// Catalog labels use authored display text, including literal markup in an
// explicit RawContent. Host messages and mid-turn steers are not session names.
func catalogMessagePreview(message provider.Message) string {
	if message.Role != provider.RoleUser || agent.IsHostGeneratedUserMessage(message) {
		return ""
	}
	if _, steer := agent.SteerText(message.Content); steer {
		return ""
	}
	return messagePreview(message)
}

// logRevision identifies the durable file revision a cache entry describes.
// Catalog listing deliberately uses metadata only; it never opens or samples
// event bodies. Session incarnation, size, and nanosecond mtime fence a stale
// cache, while durable writers refresh the cache after every drained prefix.
type logRevision struct {
	Size      int64
	ModTimeNS int64
	Identity  string
	Exists    bool
}

// revisionOfLog inspects the manifest-selected event log without reading its body.
func revisionOfLog(dir string) (logRevision, error) {
	manifest, err := readStoredManifest(filepath.Join(dir, "manifest.json"))
	if os.IsNotExist(err) {
		return logRevision{}, nil
	}
	if err != nil {
		return logRevision{}, err
	}
	info, err := os.Stat(logPathForManifest(dir, manifest))
	if os.IsNotExist(err) {
		return logRevision{}, nil
	}
	if err != nil {
		return logRevision{}, err
	}
	return logRevision{Size: info.Size(), ModTimeNS: info.ModTime().UnixNano(), Exists: true}, nil
}

func readCatalogMetadata(cacheDir string, manifest Manifest, revision logRevision) (catalogMetadata, error) {
	data, err := os.ReadFile(catalogMetadataPath(cacheDir))
	if err != nil {
		return catalogMetadata{}, err
	}
	return decodeCatalogMetadata(data, manifest, revision)
}

func decodeCatalogMetadata(data []byte, manifest Manifest, revision logRevision) (catalogMetadata, error) {
	var metadata catalogMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return catalogMetadata{}, err
	}
	if metadata.Version != catalogMetadataVersion || metadata.Codec != Codec || metadata.SessionID != manifest.SessionID ||
		metadata.CreatedAt != manifest.CreatedAt.UTC().Format(time.RFC3339Nano) ||
		metadata.LogSize != revision.Size || metadata.LogModTimeNS != revision.ModTimeNS || metadata.LogIdentity != revision.Identity {
		return catalogMetadata{}, errors.New("session: catalog metadata cache is stale")
	}
	return metadata, nil
}

func writeCatalogMetadata(cacheDir string, metadata catalogMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(catalogMetadataPath(cacheDir), append(data, '\n'), 0o600)
}

func rebuildCatalogMetadata(ctx context.Context, handle eventPageReader, cacheDir, sessionDir string, manifest Manifest) error {
	metadata, err := reduceCatalogMetadata(ctx, handle, manifest)
	if err != nil {
		return err
	}
	return writeCatalogMetadataForSession(cacheDir, sessionDir, metadata)
}

func reduceCatalogMetadata(ctx context.Context, handle eventPageReader, manifest Manifest) (catalogMetadata, error) {
	reducer := catalogReducer{}
	if stream, ok := handle.(interface {
		scanCatalog(context.Context, func(Commit) error) error
	}); ok {
		if err := stream.scanCatalog(ctx, reducer.apply); err != nil {
			return catalogMetadata{}, err
		}
		return reducer.metadata(manifest), nil
	}
	var cursor uint64
	for {
		page, err := handle.Read(ctx, cursor, 32)
		if err != nil {
			return catalogMetadata{}, err
		}
		for _, commit := range page.Commits {
			if err := reducer.apply(commit); err != nil {
				return catalogMetadata{}, err
			}
		}
		if !page.Truncated {
			break
		}
		if page.Next <= cursor {
			return catalogMetadata{}, ErrDamagedStore
		}
		cursor = page.Next
	}
	return reducer.metadata(manifest), nil
}

// writeCatalogMetadataForSession stamps the cache with the exact durable bytes
// it describes. Every writer of the cache goes through here so a reader can
// validate it using file metadata without reading event bodies.
func writeCatalogMetadataForSession(cacheDir, sessionDir string, metadata catalogMetadata) error {
	revision, err := revisionOfLog(sessionDir)
	if err != nil {
		return err
	}
	metadata.LogSize, metadata.LogModTimeNS, metadata.LogIdentity = revision.Size, revision.ModTimeNS, revision.Identity
	return writeCatalogMetadata(cacheDir, metadata)
}
