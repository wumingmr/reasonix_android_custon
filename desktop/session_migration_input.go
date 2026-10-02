package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Used only for an input that needs import, and again if its cheap stat stamp
// changes during import. Completed unchanged receipts retain the stat-only path.
func desktopMigrationInputDigest(files []string) (string, error) {
	paths := append([]string(nil), files...)
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%q:missing\n", path)
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("migration input is not a regular file")
		}
		fileHash := sha256.New()
		if strings.HasSuffix(path, ".jsonl.meta") {
			body, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				return "", err
			}
			// These fields are read-model projections. Keep ancestry, workspace,
			// selected-head semantics, configuration and every unknown field.
			for _, key := range []string{"revision", "content_digest", "writer_id", "schema_version", "turns", "preview", "listing_revision", "listing_content_digest"} {
				delete(fields, key)
			}
			body, err = json.Marshal(fields)
			if err != nil {
				return "", err
			}
			_, _ = fileHash.Write(body)
		} else {
			file, err := os.Open(path)
			if err != nil {
				return "", err
			}
			_, readErr := io.Copy(fileHash, file)
			if err := errors.Join(readErr, file.Close()); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(h, "%q:%x\n", path, fileHash.Sum(nil))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
