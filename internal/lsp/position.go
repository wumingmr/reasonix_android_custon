package lsp

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Position is a zero-based LSP position. Character is counted in the encoding the
// server negotiated at initialize (utf-16 by default, utf-8 when both sides
// agree).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open span between two positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a file URI plus a range, the shape definition/references return.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

func pathToURI(p string) string {
	return pathToURIForOS(p, runtime.GOOS)
}

func pathToURIForOS(p, goos string) string {
	p = filepath.ToSlash(p)
	if goos == "windows" {
		p = strings.ReplaceAll(p, `\`, "/")
		if hostAndPath, ok := strings.CutPrefix(p, "//"); ok {
			host, uriPath, found := strings.Cut(hostAndPath, "/")
			if found && host != "" {
				return (&url.URL{Scheme: "file", Host: host, Path: "/" + uriPath}).String()
			}
		}
		if len(p) > 1 && p[1] == ':' {
			p = "/" + p // C:/x → /C:/x so the URI becomes file:///C:/x
		}
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

func uriToPath(uri string) (string, error) {
	return uriToPathForOS(uri, runtime.GOOS)
}

func uriToPathForOS(uri, goos string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse URI: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "file") || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("URI is not a local file URI")
	}
	if strings.ContainsRune(u.Path, 0) {
		return "", errors.New("file URI path contains NUL")
	}
	host := u.Hostname()
	if u.Host != "" && (host == "" || u.Port() != "") {
		return "", errors.New("file URI has an invalid authority")
	}
	if host != "" && !strings.EqualFold(host, "localhost") {
		if goos != "windows" {
			return "", fmt.Errorf("remote file URI authority %q is not local on %s", host, goos)
		}
		if u.Path == "" || u.Path == "/" {
			return "", errors.New("UNC file URI is missing a share path")
		}
		return `\\` + host + `\` + strings.ReplaceAll(strings.TrimPrefix(u.Path, "/"), "/", `\`), nil
	}
	p := u.Path
	if p == "" {
		return "", errors.New("file URI path is empty")
	}
	if goos == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	if goos == "windows" {
		return strings.ReplaceAll(p, "/", `\`), nil
	}
	return p, nil
}

// locate finds symbol on the 1-based line of content and returns the LSP position
// of its first byte, converting the byte column into the server's encoding.
func locate(content string, line1 int, symbol, enc string) (Position, error) {
	lines := strings.Split(content, "\n")
	if line1 < 1 || line1 > len(lines) {
		return Position{}, fmt.Errorf("line %d out of range (file has %d lines)", line1, len(lines))
	}
	text := strings.TrimSuffix(lines[line1-1], "\r")
	before, _, ok := strings.Cut(text, symbol)
	if !ok {
		return Position{}, fmt.Errorf("symbol %q not found on line %d", symbol, line1)
	}
	return Position{Line: line1 - 1, Character: encodeChar(before, enc)}, nil
}

func encodeChar(prefix, enc string) int {
	if enc == encodingUTF8 {
		return len(prefix)
	}
	return len(utf16.Encode([]rune(prefix)))
}

const (
	encodingUTF8  = "utf-8"
	encodingUTF16 = "utf-16"
)
