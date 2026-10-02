package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/fileops"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(moveFile{}) }

var renameFile = renameNoReplace

// moveFile moves or renames one file. roots, when non-empty, confine both the
// source and destination to the workspace; guard rejects Reasonix session-data
// endpoints on either side (a move out of the store mutates it too); workDir
// resolves relative paths.
type moveFile struct {
	roots   []string
	rootSet *sandbox.WritableRootSet
	guard   SessionDataGuard
	managed ManagedConfigPaths
	workDir string
}

func (moveFile) Name() string { return "move_file" }

func (moveFile) Description() string {
	return "Move or rename a file from source_path to destination_path. Creates the destination parent directory as needed. Use instead of shell mv, Move-Item, or ren for file moves so workspace confinement and file-edit permissions apply."
}

func (moveFile) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"source_path":{"type":"string","description":"Existing file path to move"},"destination_path":{"type":"string","description":"Destination file path; must not already exist"}},"required":["source_path","destination_path"]}`)
}

func (moveFile) ReadOnly() bool { return false }

func (m moveFile) DeclareWriteAccess(args json.RawMessage) (tool.WriteAccessDeclaration, error) {
	var p struct {
		SourcePath      string `json:"source_path"`
		DestinationPath string `json:"destination_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.WriteAccessDeclaration{}, fmt.Errorf("invalid args: %w", err)
	}
	return declareParentWriteDirs(m.workDir, p.SourcePath, p.DestinationPath)
}

func (m moveFile) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		SourcePath      string `json:"source_path"`
		DestinationPath string `json:"destination_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.SourcePath == "" {
		return "", fmt.Errorf("source_path is required")
	}
	if p.DestinationPath == "" {
		return "", fmt.Errorf("destination_path is required")
	}
	src := resolveIn(m.workDir, p.SourcePath)
	dst := resolveIn(m.workDir, p.DestinationPath)
	roots := effectiveWriteRoots(ctx, m.rootSet, m.roots)
	if err := confineWrite(ctx, roots, m.guard, m.managed, src); err != nil {
		return "", err
	}
	if err := confineWrite(ctx, roots, m.guard, m.managed, dst); err != nil {
		return "", err
	}
	initialInfo, _ := os.Stat(src)
	sourceLock := fileops.DiskTarget(src, initialInfo)
	destinationLock := fileops.DiskTarget(dst, nil)
	sourceLock.Route, destinationLock.Route = "mutation", "mutation"
	unlock := fileops.LockMany(sourceLock, destinationLock)
	defer unlock()
	info, err := os.Stat(src)
	if err != nil {
		return "", &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSNotFound, Path: src, Recovery: "inspect the current source path before retrying the move"}, Cause: err}
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; move_file only moves files", src)
	}
	if filepath.Clean(src) == filepath.Clean(dst) {
		return fmt.Sprintf("%s is already at %s; no changes made", src, dst), nil
	}
	sameFileDestination := false
	if dstInfo, err := os.Stat(dst); err == nil {
		if !os.SameFile(info, dstInfo) {
			return "", &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSAlreadyExists, Path: dst, Recovery: "choose a different destination or inspect the existing target"}, Cause: os.ErrExist}
		}
		sameFileDestination = true
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", dst, err)
	}
	if dir := filepath.Dir(dst); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	commit := func() {
		store := fileops.FromContext(ctx)
		store.ObserveAbsent(fileops.DiskTarget(src, nil))
		if moved, statErr := os.Stat(dst); statErr == nil {
			target, version := fileops.DiskSnapshot(dst, moved)
			store.ObservePresent(target, version)
		}
	}
	if err := renameFile(src, dst); err != nil {
		if sameFileDestination {
			if rerr := renameSameFileDestination(src, dst); rerr != nil {
				return "", fmt.Errorf("move %s to %s: %w", src, dst, rerr)
			}
			commit()
			return fmt.Sprintf("moved %s to %s", src, dst), nil
		}
		if isCrossDeviceMove(err) {
			if cerr := copyRegularFileAndRemoveSource(src, dst, info); cerr != nil {
				return "", fmt.Errorf("move %s to %s: %w", src, dst, cerr)
			}
			commit()
			return fmt.Sprintf("moved %s to %s", src, dst), nil
		}
		if os.IsExist(err) {
			return "", &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSAlreadyExists, Path: dst, Recovery: "the destination appeared concurrently; inspect it and choose another path"}, Cause: err}
		}
		return "", fmt.Errorf("move %s to %s: %w", src, dst, err)
	}
	commit()
	return fmt.Sprintf("moved %s to %s", src, dst), nil
}

func renameSameFileDestination(src, dst string) error {
	tmp, err := os.CreateTemp(filepath.Dir(src), ".reasonix-move-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Remove(tmpName); err != nil {
		return err
	}

	if err := renameFile(src, tmpName); err != nil {
		return err
	}
	// A distinct hard-link alias already names the moved file. Never delete
	// the destination: it may have been replaced since the initial stat.
	if tempInfo, err := os.Stat(tmpName); err == nil {
		if dstInfo, err := os.Stat(dst); err == nil && os.SameFile(tempInfo, dstInfo) {
			return os.Remove(tmpName)
		}
	}
	if err := renameFile(tmpName, dst); err != nil {
		if restoreErr := renameFile(tmpName, src); restoreErr != nil {
			return fmt.Errorf("%w; restore %s: %w", err, src, restoreErr)
		}
		return err
	}
	return nil
}

func isCrossDeviceMove(err error) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		return false
	}
	msg := strings.ToLower(linkErr.Err.Error())
	return strings.Contains(msg, "cross-device") ||
		strings.Contains(msg, "different device") ||
		strings.Contains(msg, "different disk") ||
		strings.Contains(msg, "not same device")
}

func copyRegularFileAndRemoveSource(src, dst string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cross-filesystem fallback only supports regular files")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return ErrFileChanged
	}
	target, version := fileops.DiskHandleSnapshot(src, in, opened)
	out, err := os.CreateTemp(filepath.Dir(dst), ".reasonix-move-*")
	if err != nil {
		return err
	}
	tmpPath := out.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	current, err := os.Stat(src)
	if err != nil {
		return err
	}
	currentTarget, currentVersion := fileops.DiskSnapshot(src, current)
	if target != currentTarget || version != currentVersion {
		return ErrFileChanged
	}
	if err := in.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("destination committed but source removal failed: %w", err)
	}
	return nil
}
