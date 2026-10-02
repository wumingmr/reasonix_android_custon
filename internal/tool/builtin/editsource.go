package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"reasonix/internal/fileops"
	fileenc "reasonix/internal/fileutil/encoding"
	"reasonix/internal/tool"
)

// editSource is the file state a read-modify-write tool works against, plus the
// route its write must take back. Read and write must stay paired: content read
// from the host's unsaved buffer has to return there, and content read from disk
// has to return to disk. Mixing the two silently drops one side's changes.
type editSource struct {
	content string
	enc     fileenc.Kind
	overlay bool
	id      fileIdentity
}

func overlayObservationTarget(overlay FileOverlay, path string) fileops.Target {
	if identified, ok := overlay.(FileOverlayIdentity); ok {
		return fileops.OverlayTargetWithIdentity(path, identified.FileOverlayIdentity())
	}
	return fileops.OverlayTarget(path)
}

// readEditSource resolves path the way Execute and Preview must both see it:
// the host's unsaved editor buffer when the overlay can serve it, otherwise the
// decoded disk content. A non-UTF-8 file always stays on the disk route — the
// overlay contract is text-only, so routing GBK or UTF-16 through it would
// rewrite the file as UTF-8.
func readEditSource(ctx context.Context, overlay FileOverlay, path string) (source editSource, readErr error) {
	data, id, err := readDiskIdentity(path)
	if err != nil {
		return editSource{}, err
	}
	if !id.existed {
		if overlay != nil && filepath.IsAbs(path) {
			if buffered, ok := overlay.ReadTextFile(ctx, path); ok {
				return editSource{content: buffered, enc: fileenc.UTF8, overlay: true, id: overlayIdentity(buffered)}, nil
			}
		}
		return editSource{enc: fileenc.UTF8, id: id}, &os.PathError{Op: "read", Path: path, Err: os.ErrNotExist}
	}
	enc, text := fileenc.DetectAndDecode(data)
	content := string(text)
	if overlay != nil && enc == fileenc.UTF8 && filepath.IsAbs(path) {
		if buffered, ok := overlay.ReadTextFile(ctx, path); ok {
			return editSource{content: buffered, enc: enc, overlay: true, id: overlayIdentity(buffered)}, nil
		}
	}
	return editSource{content: content, enc: enc, id: id}, nil
}

// lockMutationPath serializes all structured mutations of one real target in
// this process. Existing hard-link aliases share the native file identity.
func lockMutationPath(path string) func() {
	info, _ := os.Stat(path)
	target := fileops.DiskTarget(path, info)
	target.Route = "mutation"
	return fileops.Lock(target)
}

func (s editSource) observation(overlay FileOverlay, path string) (fileops.Target, fileops.Version, error) {
	if s.overlay {
		return overlayObservationTarget(overlay, path), fileops.OverlayVersion(s.content), nil
	}
	return s.id.target, s.id.version, nil
}

func (s editSource) requireObserved(ctx context.Context, overlay FileOverlay, path string) error {
	store := fileops.FromContext(ctx)
	if store == nil { // Direct package-level tool calls remain usable in tests and embeddings.
		return nil
	}
	target, version, err := s.observation(overlay, path)
	if err != nil {
		return &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSNotFound, Path: path, Recovery: "read the file, then retry the edit"}, Cause: err}
	}
	observed := store.Get(target)
	if observed.Kind != fileops.Present {
		return &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSNotObserved, Path: path, Recovery: "read any current window of this file, then retry"}, Cause: fmt.Errorf("file has not been observed in this agent session")}
	}
	if observed.Version != version {
		return &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSStaleVersion, Path: path, ExpectedSnapshot: string(observed.Version), ActualSnapshot: string(version), Recovery: "the file changed after it was read; read it again, then retry"}, Cause: ErrFileChanged}
	}
	return nil
}

func (s editSource) commitObservation(ctx context.Context, overlay FileOverlay, path, content string) {
	store := fileops.FromContext(ctx)
	if store == nil {
		return
	}
	if s.overlay {
		store.ObservePresent(overlayObservationTarget(overlay, path), fileops.OverlayVersion(content))
		return
	}
	if info, err := os.Stat(path); err == nil {
		target, version := fileops.DiskSnapshot(path, info)
		store.ObservePresent(target, version)
	} else {
		store.Forget(fileops.DiskTarget(path, nil))
	}
}

func (s editSource) readSnapshot(path string) string {
	kind, prefix := tool.ReadSourceDisk, "raw-sha256:"
	if s.overlay {
		kind, prefix = tool.ReadSourceOverlay, "overlay:"
	}
	return tool.SourceSnapshot(kind, path, fmt.Sprintf("%s%x", prefix, s.id.sum))
}

// write persists content on the same route the source was read from. An overlay
// that declines a managed write leaves its outcome unknown. It never falls
// back to disk because that would update a different source than the one read.
func (s editSource) write(ctx context.Context, overlay FileOverlay, path, content string) error {
	if err := s.assertUnchanged(ctx, overlay, path); err != nil {
		return err
	}
	if s.overlay && overlay != nil {
		if err := s.recordWrite(ctx, path, content, "overlay", overlay); err != nil {
			return err
		}
		if err := s.assertUnchanged(ctx, overlay, path); err != nil {
			return err
		}
		if ok, err := overlay.WriteTextFile(ctx, path, content); ok {
			if err != nil {
				fileops.FromContext(ctx).Forget(overlayObservationTarget(overlay, path))
				return fmt.Errorf("write outcome unknown: %w", err)
			}
			if err == nil {
				s.commitObservation(ctx, overlay, path, content)
			}
			return err
		}
		fileops.FromContext(ctx).Forget(overlayObservationTarget(overlay, path))
		return fmt.Errorf("write outcome unknown: original overlay did not confirm the write")
	}
	if err := s.recordWrite(ctx, path, content, "disk", overlay); err != nil {
		return err
	}
	if err := s.assertUnchanged(ctx, overlay, path); err != nil {
		return err
	}
	if err := writeFileEncoded(path, content, s.enc); err != nil {
		return err
	}
	s.commitObservation(ctx, overlay, path, content)
	return nil
}
