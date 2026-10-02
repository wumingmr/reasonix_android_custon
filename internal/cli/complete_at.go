package cli

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"reasonix/internal/control"
	"reasonix/internal/fileref"
)

// activeAtToken finds the @-reference token under the cursor. cursor is a byte
// offset into val; when out of range the scan uses the end of the string.
// The '@' must start the line or follow whitespace, so emails like "a@b" don't
// trigger it. A backslash-escaped space or tab is part of the token.
//
// Returns (at, end, query, ok):
//   - [at, end) is the full token span to replace on accept (including '@'),
//     extending past the caret to the next unescaped whitespace so mid-token
//     accept never leaves a dangling suffix ("@foo|bar" → "@file.md ", not
//     "@file.mdbar").
//   - query is only the text after '@' up to the caret, used for menu filtering
//     ("@fo|o" filters as "fo", not "foo").
func activeAtToken(val string, cursor int) (at, end int, query string, ok bool) {
	if cursor < 0 || cursor > len(val) {
		cursor = len(val)
	}
	for i := cursor - 1; i >= 0; i-- {
		switch val[i] {
		case ' ', '\t':
			if i > 0 && val[i-1] == '\\' {
				i-- // escaped whitespace stays inside the token
				continue
			}
			return 0, 0, "", false
		case '\n':
			return 0, 0, "", false
		case '@':
			if i == 0 || val[i-1] == ' ' || val[i-1] == '\t' || val[i-1] == '\n' {
				end = tokenEnd(val, i+1)
				queryEnd := min(max(cursor, i+1), end)
				return i, end, val[i+1 : queryEnd], true
			}
			return 0, 0, "", false
		}
	}
	return 0, 0, "", false
}

// tokenEnd returns the exclusive byte end of a path/ref token starting at from
// (just after '@'). Stops at unescaped whitespace or newline.
func tokenEnd(val string, from int) int {
	for i := from; i < len(val); i++ {
		switch val[i] {
		case ' ', '\t':
			if i > 0 && val[i-1] == '\\' {
				continue
			}
			return i
		case '\n':
			return i
		}
	}
	return len(val)
}

// atItems builds the @-reference menu for a token. A "server:uri" token whose
// server is connected lists that server's MCP resources; otherwise the token is
// a path and we list one directory level (never a recursive walk), plus — at the
// top level — any matching MCP resources.
func (m *chatTUI) atItems(token string) []compItem {
	if i := strings.Index(token, ":"); i > 0 && m.isMCPServer(token[:i]) {
		return m.resourceItems(token[:i], token[i+1:])
	}
	return m.fileItems(token)
}

// fileItems lists one directory level for a path token. dir is the part up to
// the last '/', frag the part after; entries of dir starting with frag are
// offered (directories descend, files complete). Hidden entries are skipped
// unless frag starts with '.'. Top-level tokens also surface MCP resources.
func (m *chatTUI) fileItems(token string) []compItem {
	dir, frag := splitPathToken(token)
	// The typed token may carry backslash-escaped spaces (the form completion
	// itself inserts); filesystem lookups need the real path while inserts keep
	// the escaped grammar.
	fsFrag := control.UnescapeRefPath(frag)
	workspaceRoot := ""
	if m.ctrl != nil {
		workspaceRoot = m.ctrl.WorkspaceRoot()
	}
	readDir := control.UnescapeRefPath(dir)
	if workspaceRoot != "" {
		if readDir == "" {
			readDir = workspaceRoot
		} else if !filepath.IsAbs(readDir) {
			readDir = filepath.Join(workspaceRoot, filepath.FromSlash(readDir))
		}
	} else if readDir == "" {
		readDir = "."
	}
	entries, err := os.ReadDir(readDir)
	if err != nil {
		entries = nil
	}
	// Directories first, then files; both groups sorted naturally.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return fileref.NaturalLess(entries[i].Name(), entries[j].Name())
	})

	showHidden := strings.HasPrefix(fsFrag, ".")
	var items []compItem
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, fsFrag) {
			continue
		}
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			items = append(items, compItem{label: name + "/", insert: "@" + dir + control.EscapeRefPath(name) + "/", hint: "dir", descend: true})
		} else {
			items = append(items, compItem{label: name, insert: "@" + dir + control.EscapeRefPath(name)})
		}
		if len(items) >= maxCompItems {
			break
		}
	}

	// At the top level (still naming the first segment) MCP resources share the
	// '@' namespace, so offer the matching ones too.
	if !strings.Contains(token, "/") {
		seen := map[string]bool{}
		for _, it := range items {
			seen[strings.TrimPrefix(it.insert, "@")] = true
		}
		remaining := min(maxCompItems-len(items), maxFileSearchItems)
		results := m.searchFileRefs(fsFrag)
		if len(results) > remaining {
			results = results[:remaining]
		}
		for _, path := range results {
			escaped := control.EscapeRefPath(path)
			if seen[escaped] {
				continue
			}
			items = append(items, compItem{label: path, insert: "@" + escaped, hint: "file"})
			if len(items) >= maxCompItems {
				break
			}
		}
		items = append(items, m.resourceItems("", token)...)
	}
	return items
}

// searchFileRefs memoizes the bounded basename walk so re-rendering the menu
// for an unchanged @token fragment doesn't re-walk the workspace each keystroke.
func (m *chatTUI) searchFileRefs(frag string) []string {
	if m.fileSearchCache == nil {
		m.fileSearchCache = map[string][]string{}
	}
	if r, ok := m.fileSearchCache[frag]; ok {
		return r
	}
	searchRoot := "."
	if m.ctrl != nil {
		if wr := m.ctrl.WorkspaceRoot(); wr != "" {
			searchRoot = wr
		}
	}
	results := fileref.Search(searchRoot, frag, maxFileSearchItems)
	paths := make([]string, 0, len(results))
	for _, r := range results {
		paths = append(paths, r.Path)
	}
	m.fileSearchCache[frag] = paths
	return paths
}

// splitPathToken splits a path token into (dir, frag): dir keeps its trailing
// slash ("internal/" ), frag is the segment being typed.
func splitPathToken(token string) (dir, frag string) {
	if i := strings.LastIndex(token, "/"); i >= 0 {
		return token[:i+1], token[i+1:]
	}
	return "", token
}

// isMCPServer reports whether name is a connected MCP server.
func (m *chatTUI) isMCPServer(name string) bool {
	if m.host == nil {
		return false
	}
	return slices.Contains(m.host.ServerNames(), name)
}

// resourceItems lists MCP resources as @server:uri completions. When server is
// "" (top level) it matches by the whole "server:uri" prefix; otherwise it lists
// the named server's resources filtered by the uri prefix.
func (m *chatTUI) resourceItems(server, frag string) []compItem {
	if m.host == nil {
		return nil
	}
	var items []compItem
	for _, r := range m.host.Resources() {
		ref := r.Server + ":" + r.URI
		switch {
		case server == "":
			if !strings.HasPrefix(ref, frag) {
				continue
			}
		case r.Server == server:
			if !strings.HasPrefix(r.URI, frag) {
				continue
			}
		default:
			continue
		}
		label := r.Name
		if label == "" {
			label = "resource"
		}
		items = append(items, compItem{label: "@" + ref, insert: "@" + ref, hint: label})
	}
	return items
}
