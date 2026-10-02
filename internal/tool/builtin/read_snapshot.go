package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"reasonix/internal/fileops"
	"reasonix/internal/tool"
)

// Full/range reads may capture a bounded source for version-safe paging. A
// preview never scans a large file merely to establish a whole-file identity.

func (r readFile) ResolveReadPath(args json.RawMessage) (string, error) {
	// Path identity must remain available even when another argument is
	// invalid, so a failed continuation still belongs to its bounded task.
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Path) == "" {
		return "", fmt.Errorf("path is required")
	}
	return resolveReadablePath(r.workDir, p.Path, r.paths).Path, nil
}

func (r readFile) ExecuteRead(ctx context.Context, args json.RawMessage) (string, tool.ReadResultEnvelope, error) {
	p, err := parseReadFileParams(args)
	if err != nil {
		return "", tool.ReadResultEnvelope{}, err
	}
	rp := resolveReadablePath(r.workDir, p.Path, r.paths)
	if confineRead(r.forbidRoots, rp.Path) {
		return "", tool.ReadResultEnvelope{}, &os.PathError{Op: "open", Path: rp.DisplayPath, Err: os.ErrNotExist}
	}
	source := tool.ReadResultSource{CanonicalPath: rp.Path}
	r.captured = &source
	var output string
	store := fileops.FromContext(ctx)
	if content, ok := r.overlayText(ctx, rp); ok {
		source.Kind = tool.ReadSourceOverlay
		version := fileops.OverlayVersion(content)
		source.Identity = string(version)
		// Both output and identity derive from this exact buffer instance.
		output, err = r.scan(readContextReader{ctx, strings.NewReader(content)}, p.Offset, p.Limit)
		if err == nil {
			store.ObservePresent(overlayObservationTarget(r.overlay, rp.Path), version)
		}
	} else {
		source.Kind = tool.ReadSourceDisk
		f, openErr := os.Open(rp.Path)
		if openErr != nil {
			if os.IsNotExist(openErr) {
				store.ObserveAbsent(fileops.DiskTarget(rp.Path, nil))
				return "", tool.ReadResultEnvelope{}, &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSNotFound, Path: rp.DisplayPath, Recovery: "the file is absent; create it only if the task requires a new file"}, Cause: &os.PathError{Op: "read", Path: rp.DisplayPath, Err: os.ErrNotExist}}
			}
			return "", tool.ReadResultEnvelope{}, fmt.Errorf("read %s: %s", rp.DisplayPath, rp.ErrorText(openErr))
		}
		defer f.Close()
		before, statErr := f.Stat()
		if statErr != nil {
			return "", tool.ReadResultEnvelope{}, fmt.Errorf("stat %s: %s", rp.DisplayPath, rp.ErrorText(statErr))
		}
		if before.IsDir() {
			return "", tool.ReadResultEnvelope{}, fmt.Errorf("%s is a directory, not a file — use the ls tool to list it, or read a specific file inside it", rp.DisplayPath)
		}
		target, version := fileops.DiskHandleSnapshot(rp.Path, f, before)
		source.Identity = string(version)
		// The window and both metadata samples come from the same handle. Reading a
		// small window therefore never scans the rest of a large file to mint a
		// version, while a concurrent replacement cannot authorize the pathname.
		output, err = r.scanEncoded(readContextReader{ctx, f}, p.Offset, p.Limit)
		if err == nil {
			handleAfter, handleErr := f.Stat()
			pathAfter, pathErr := os.Stat(rp.Path)
			handleTarget, handleVersion := fileops.DiskHandleSnapshot(rp.Path, f, handleAfter)
			pathTarget, pathVersion := fileops.DiskSnapshot(rp.Path, pathAfter)
			if handleErr == nil && pathErr == nil && handleTarget == target && handleVersion == version && pathTarget == target && pathVersion == version {
				store.ObservePresent(target, version)
			} else {
				err = &tool.OperationError{Diagnostic: tool.OperationDiagnostic{Code: tool.FSStaleVersion, Path: rp.DisplayPath, Recovery: "the file changed while it was being read; read it again"}, Cause: ErrFileChanged}
			}
		}
	}
	source.Snapshot = tool.SourceSnapshot(source.Kind, source.CanonicalPath, source.Identity)
	env, _ := r.ReadEnvelope(ctx, args, output)
	return output, env, err
}

type readContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r readContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
