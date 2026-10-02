package winaclresidue

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type residueKind string

const (
	residueDeny  residueKind = "deny"
	residueGrant residueKind = "grant"
)

type residueEntry struct {
	kind residueKind
	path string
}

// markerOwnerPID extracts the owning PID from a "<pid>[-nonce].txt" name.
func markerOwnerPID(name string) (string, bool) {
	if !strings.HasSuffix(name, ".txt") {
		return "", false
	}
	pid, _, _ := strings.Cut(strings.TrimSuffix(name, ".txt"), "-")
	if pid == "" {
		return "", false
	}
	if _, err := strconv.ParseUint(pid, 10, 32); err != nil {
		return "", false
	}
	return pid, true
}

// readResidueMarker parses "<kind>\t<path>" lines. A tab separates the fields
// so paths with spaces survive; unrecognized lines are skipped rather than
// guessed at, so a corrupt marker cannot cause a wrong ACE removal.
func readResidueMarker(path string) []residueEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []residueEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		kindStr, p, ok := strings.Cut(strings.TrimRight(scanner.Text(), "\r\n"), "\t")
		if !ok || p == "" {
			continue
		}
		switch kind := residueKind(kindStr); kind {
		case residueDeny, residueGrant:
			out = append(out, residueEntry{kind: kind, path: p})
		}
	}
	return out
}

// isWindowsSystemRoot reports whether path lies under a shared system
// directory. Markers are untrusted input under %TEMP%; stripping the built-in
// package SIDs from System32 or Program Files would remove factory ACEs.
// Paths are compared with backslash separators so the rule is the same on
// every host that inspects a Windows marker.
func isWindowsSystemRoot(path string) bool {
	clean := windowsPathKey(path)
	for _, envVar := range []string{"SystemRoot", "windir", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		root := os.Getenv(envVar)
		if root == "" {
			continue
		}
		root = windowsPathKey(root)
		if clean == root || strings.HasPrefix(clean, root+`\`) {
			return true
		}
	}
	return false
}

func windowsPathKey(path string) string {
	return strings.ToLower(strings.TrimRight(strings.ReplaceAll(filepath.Clean(path), "/", `\`), `\`))
}

func dedupeSIDStrings(sids []string) []string {
	out := make([]string, 0, len(sids))
	seen := map[string]bool{}
	for _, sid := range sids {
		if sid == "" || seen[sid] {
			continue
		}
		seen[sid] = true
		out = append(out, sid)
	}
	return out
}
