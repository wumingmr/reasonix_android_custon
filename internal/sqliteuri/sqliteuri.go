// Package sqliteuri builds SQLite file URIs for local disk databases.
package sqliteuri

import (
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// Disk converts a local disk path into an absolute SQLite file URI. Query
// parameters are encoded separately from the path so path punctuation cannot
// be interpreted as SQLite options.
func Disk(path string, query url.Values) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("sqlite disk path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return disk(abs, query, runtime.GOOS), nil
}

func disk(absPath string, query url.Values, goos string) string {
	slash := filepath.ToSlash(absPath)
	if goos == "windows" {
		slash = strings.ReplaceAll(slash, `\`, "/")
		if len(slash) >= 2 && slash[1] == ':' {
			slash = "/" + slash
		}
	}
	u := &url.URL{Scheme: "file", Path: slash}
	u.RawQuery = query.Encode()
	return u.String()
}
