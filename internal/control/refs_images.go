package control

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// inputImages resolves image @-references in the turn input to data URLs so the
// turn can carry them to a vision-capable model. Best-effort: an unreadable image
// is skipped — the @ref still lands as text via ResolveRefs.
func (c *Controller) inputImages(line string) []string {
	if !c.imageInputEnabled() {
		return nil
	}
	return c.resolveInputImageCandidates(line)
}

// resolveInputImageCandidates resolves authorized image references without
// consulting the active model capability. The parent controller uses this only
// to hand candidates to a child; the child decides whether to embed them.
func (c *Controller) resolveInputImageCandidates(line string) []string {
	var urls []string
	seen := map[string]bool{}
	for _, r := range append(c.detectRefs(line), bareVisionRefs(line)...) {
		url, err := c.resolveReferenceImage(r)
		if err != nil || url == "" || seen[url] {
			continue
		}
		seen[url] = true
		urls = append(urls, url)
	}
	return urls
}

func (c *Controller) resolveReferenceImage(r ref) (string, error) {
	baseDir := c.workspaceRoot
	if r.baseDir != "" {
		baseDir = r.baseDir
	}
	return c.visionRefImageValue(r, baseDir)
}

func visionFileImageDataURL(path, baseDir string) (string, error) {
	absPath, absBase, ok := resolveAbsRef(path, baseDir)
	if !ok {
		return "", os.ErrNotExist
	}
	if absBase == "" {
		return "", fmt.Errorf("workspace root is required for file image references")
	}

	root, err := os.OpenRoot(absBase)
	if err != nil {
		return "", err
	}
	defer root.Close()

	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("image path must not be a symlink")
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > maxImageAttachmentBytes {
		return "", fmt.Errorf("image must be between 1 byte and 64 MB")
	}
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, opened) {
		return "", fmt.Errorf("image changed while opening")
	}
	return dataURLFromImageReader(f, path)
}

func dataURLFromImageReader(r io.Reader, path string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxImageAttachmentBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || len(raw) > maxImageAttachmentBytes {
		return "", fmt.Errorf("image must be between 1 byte and 64 MB")
	}
	mime := detectedImageMime(raw)
	if mime == "" {
		return "", fmt.Errorf("%s is not a supported image", path)
	}
	raw, mime = compressForVision(raw, mime)
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}
