package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	filelock "reasonix/internal/identitylock"
)

// The sibling staging files remain inode witnesses until publication is marked
// complete. Recovery only removes targets that still refer to those witnesses.
// Filesystems without exclusive hard-link publication fail safely.
type exportPublication struct {
	Targets  []string `json:"targets"`
	Complete bool     `json:"complete"`
}

func writeExportPublication(dir string, manifest exportPublication) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeStreamingExport(filepath.Join(dir, "publication.json"), func(w io.Writer) error { _, err := w.Write(data); return err })
}
func recoverExportPublications(parent string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".reasonix-export-batch-") {
			continue
		}
		dir := filepath.Join(parent, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "publication.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var manifest exportPublication
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("invalid export recovery manifest: %w", err)
		}
		if !manifest.Complete {
			for i, name := range manifest.Targets {
				if name != filepath.Base(name) || name == "." || name == ".." {
					return errors.New("invalid export recovery target")
				}
				witness, err := os.Lstat(filepath.Join(dir, fmt.Sprintf("page-%06d", i)))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				target := filepath.Join(parent, name)
				info, err := os.Lstat(target)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if info.Mode().IsRegular() && os.SameFile(info, witness) {
					if err := os.Remove(target); err != nil {
						return err
					}
				}
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
func publishJournaledImages(ctx context.Context, source string, targets []string) (err error) {
	parent := filepath.Dir(targets[0])
	release, err := filelock.Acquire(ctx, filepath.Join(parent, ".reasonix-export-publication.lock"))
	if err != nil {
		return err
	}
	defer release()
	if err = recoverExportPublications(parent); err != nil {
		return err
	}
	for _, target := range targets {
		if _, e := os.Lstat(target); e == nil {
			return fmt.Errorf("export file already exists: %s", filepath.Base(target))
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	dir, err := os.MkdirTemp(parent, ".reasonix-export-batch-")
	if err != nil {
		return err
	}
	manifest := exportPublication{Targets: make([]string, len(targets))}
	for i, target := range targets {
		manifest.Targets[i] = filepath.Base(target)
	}
	if err = writeExportPublication(dir, manifest); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, recoverExportPublications(parent))
		}
	}()
	for i := range targets {
		src, e := os.Open(filepath.Join(source, fmt.Sprintf("page-%06d", i)))
		if e != nil {
			return e
		}
		dst, e := os.OpenFile(filepath.Join(dir, fmt.Sprintf("page-%06d", i)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			src.Close()
			return e
		}
		_, e = copyExportContext(ctx, dst, src)
		src.Close()
		if e == nil {
			e = dst.Sync()
		}
		e = errors.Join(e, dst.Close())
		if e != nil {
			return e
		}
	}
	for i, target := range targets {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = os.Link(filepath.Join(dir, fmt.Sprintf("page-%06d", i)), target); err != nil {
			return fmt.Errorf("publish image without replacing existing files: %w", err)
		}
	}
	manifest.Complete = true
	if err = writeExportPublication(dir, manifest); err != nil {
		return err
	}
	// A complete journal can safely be cleaned by the next export if removal fails.
	_ = os.RemoveAll(dir)
	return nil
}
