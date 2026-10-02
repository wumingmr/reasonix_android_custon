package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf16"

	"reasonix/internal/pathidentity"
)

type failure struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Code  uint64 `json:"windows_errno,omitempty"`
	Stage string `json:"stage,omitempty"`
	Path  string `json:"path,omitempty"`
}

type outcome struct {
	OK       bool                   `json:"ok"`
	Path     string                 `json:"path,omitempty"`
	Mode     string                 `json:"mode,omitempty"`
	Size     int64                  `json:"size,omitempty"`
	Volume   string                 `json:"volume_serial,omitempty"`
	FileID   string                 `json:"file_id,omitempty"`
	Identity *pathidentity.Identity `json:"identity,omitempty"`
	Errors   []failure              `json:"errors,omitempty"`
}

type observation struct {
	Path        string  `json:"path"`
	UTF16Length int     `json:"utf16_length"`
	Lstat       outcome `json:"lstat"`
	Readlink    outcome `json:"readlink"`
	Eval        outcome `json:"filepath_eval_symlinks"`
	Resolve     outcome `json:"reasonix_resolve_follow_leaf"`
	Preserve    outcome `json:"reasonix_resolve_preserve_leaf"`
	Native      outcome `json:"windows_native"`
}

// Each row describes exactly one node; errors.As would attribute inner metadata
// to its wrappers. The loop explicitly unwraps and reports those nodes next.
//
//nolint:errorlint // Direct assertions preserve the diagnostic chain's node boundaries.
func failureOutcome(err error) outcome {
	r := outcome{OK: err == nil}
	for depth := 0; err != nil && depth < 12; depth++ {
		f := failure{Type: fmt.Sprintf("%T", err), Text: err.Error()}
		if e, ok := err.(syscall.Errno); ok {
			f.Code = uint64(e)
		}
		if e, ok := err.(*pathidentity.Error); ok {
			f.Stage, f.Path = e.Stage, e.Path
		}
		if e, ok := err.(*os.PathError); ok {
			f.Stage, f.Path = e.Op, e.Path
		}
		r.Errors = append(r.Errors, f)
		err = errors.Unwrap(err)
	}
	return r
}

func inspect(path string) observation {
	r := observation{Path: path, UTF16Length: len(utf16.Encode([]rune(path)))}
	info, err := os.Lstat(path)
	r.Lstat = failureOutcome(err)
	if err == nil {
		r.Lstat.Mode, r.Lstat.Size = info.Mode().String(), info.Size()
	}
	link, err := os.Readlink(path)
	r.Readlink = failureOutcome(err)
	r.Readlink.Path = link
	resolved, err := filepath.EvalSymlinks(path)
	r.Eval = failureOutcome(err)
	r.Eval.Path = resolved
	for _, follow := range []bool{true, false} {
		identity, err := pathidentity.Resolve(path, pathidentity.Options{FollowLeaf: follow})
		result := failureOutcome(err)
		if err == nil {
			result.Identity = &identity
		}
		if follow {
			r.Resolve = result
		} else {
			r.Preserve = result
		}
	}
	r.Native = nativePath(path)
	return r
}

func defaultPaths(current *user.User, extra []string) []string {
	identity := strings.TrimSpace(current.Uid)
	if identity == "" {
		identity = strings.TrimSpace(current.Username)
	}
	if identity == "" {
		identity = strings.TrimSpace(current.HomeDir)
	}
	digest := sha256.Sum256([]byte(identity))
	locks := filepath.Join(current.HomeDir, ".reasonix", "locks", fmt.Sprintf("config-edits-%x", digest[:8]))
	paths := []string{locks}
	entries, err := os.ReadDir(locks)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".lock") {
				paths = append(paths, filepath.Join(locks, entry.Name()))
				if len(paths) >= 100 {
					break
				}
			}
		}
	}
	// Never create this entry: it exercises the existing-ancestor branch.
	paths = append(paths, filepath.Join(locks, "reasonix-probe-uncreated-child", "absent.lock"))
	paths = append(paths, extra...)
	var result []string
	seen := map[string]bool{}
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		for current := absolute; !seen[current]; current = filepath.Dir(current) {
			seen[current] = true
			result = append(result, current)
			if filepath.Dir(current) == current {
				break
			}
		}
	}
	return result
}

func redactValue(value any, current *user.User) any {
	switch value := value.(type) {
	case string:
		if current.Uid != "" {
			value = strings.ReplaceAll(value, current.Uid, "<SID>")
		}
		name := filepath.Base(current.HomeDir)
		if name != "" && name != "." && name != string(filepath.Separator) {
			// PathError appends ": <message>"; quoted paths can end in quotes.
			re := regexp.MustCompile(`(?i)([\\/])` + regexp.QuoteMeta(name) + `([\\/:"'\s]|$)`)
			value = re.ReplaceAllString(value, "${1}<USER>${2}")
		}
		return value
	case []any:
		for i, item := range value {
			value[i] = redactValue(item, current)
		}
		return value
	case map[string]any:
		for key, item := range value {
			value[key] = redactValue(item, current)
		}
		return value
	default:
		return value
	}
}
