package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"reasonix/internal/session"
)

func isDesktopStoredPreview(path string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return false, err
	}
	var manifest session.Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return false, err
	}
	return (manifest.SchemaVersion == 3 && (manifest.Codec == session.PrototypeCodec || manifest.Codec == session.LegacyLinearCodec || manifest.Codec == session.FinalV31Codec)) ||
		(manifest.SchemaVersion == session.SchemaVersion && manifest.Codec == session.Codec && manifest.StorageRevision == 0), nil
}

func stageDesktopStoredPreview(ctx context.Context, path string) (*session.Service, session.SessionRef, func(), error) {
	tmp, err := os.MkdirTemp("", "reasonix-preview-upgrade-")
	if err != nil {
		return nil, session.SessionRef{}, nil, err
	}
	root := filepath.Join(tmp, "sessions")
	result, err := session.ImportStoredPreview(ctx, path, root)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, session.SessionRef{}, nil, err
	}
	service, err := session.NewService("preview-stage", session.NewFilesystemPersistence(root))
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, session.SessionRef{}, nil, err
	}
	cleanup := func() { _ = service.Shutdown(context.Background()); _ = os.RemoveAll(tmp) }
	return service, session.SessionRef{HostID: "preview-stage", SessionID: result.TargetID}, cleanup, nil
}

func previewStoredRecovery(ctx context.Context, path string) (HistoryPage, error) {
	stage, ref, cleanup, err := stageDesktopStoredPreview(ctx, path)
	if err != nil {
		return HistoryPage{Messages: []HistoryMessage{}}, err
	}
	defer cleanup()
	messages, err := stage.Query().History(ctx, ref)
	if err != nil {
		return HistoryPage{Messages: []HistoryMessage{}}, err
	}
	return historyPageFromProviderMessages(messages, func(value string) string { return value }, nil, nil, 0, 32), nil
}
