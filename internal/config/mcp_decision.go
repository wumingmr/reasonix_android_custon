package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// MCPDecision is a server's resolved enable state. Pending and Changed are both
// off, but for a reason the user can act on: a project declared the server and
// nobody approved this declaration of it.
type MCPDecision int

const (
	MCPDecisionOff MCPDecision = iota
	MCPDecisionOn
	MCPDecisionPending
	MCPDecisionChanged
)

// Code is the stable identity of d for listings and the model.
func (d MCPDecision) Code() string {
	switch d {
	case MCPDecisionOn:
		return "enabled"
	case MCPDecisionPending:
		return "awaiting_user_decision"
	case MCPDecisionChanged:
		return "changed_since_enabled"
	default:
		return "disabled"
	}
}

// AwaitsUser reports a project-declared server the user has not approved in its
// current form.
func (d MCPDecision) AwaitsUser() bool {
	return d == MCPDecisionPending || d == MCPDecisionChanged
}

// RepositoryDeclared reports an entry whose file arrives with the workspace:
// project reasonix.toml, .mcp.json, or unknown provenance.
func RepositoryDeclared(entry PluginEntry) bool {
	scope, _, _, _ := activationIdentity(entry, "")
	return scope == MCPActivationWorkspace
}

// DeclaredDefaultOn is a server's state when no decision is recorded or the
// store cannot be read. Project configuration arrives with a checkout, so it
// cannot choose commands or endpoints the host starts without the user.
func DeclaredDefaultOn(entry PluginEntry) bool {
	return !RepositoryDeclared(entry) && entry.ShouldAutoStart()
}

func undecided(entry PluginEntry) MCPDecision {
	switch {
	case DeclaredDefaultOn(entry):
		return MCPDecisionOn
	case RepositoryDeclared(entry) && entry.ShouldAutoStart():
		return MCPDecisionPending
	}
	return MCPDecisionOff
}

// Decision resolves entry in workspace. A recorded decision for a
// project-declared server holds only for the declaration it was made on.
func (s *MCPActivationStore) Decision(entry PluginEntry, workspace string) (MCPDecision, error) {
	scope, workspaceFP, source, owner := ActivationIdentity(entry, workspace)
	if s == nil {
		return undecided(entry), nil
	}
	row, found, err := s.lookupRow(MCPActivationOverride{
		Scope: scope, Workspace: workspaceFP, Source: source, Owner: owner, Server: entry.Name,
	})
	switch {
	case err != nil:
		return undecided(entry), err
	case !found:
		return undecided(entry), nil
	case !row.Enabled:
		return MCPDecisionOff, nil
	case scope == MCPActivationWorkspace && row.Identity != projectDeclarationDigest(entry, workspace):
		return MCPDecisionChanged, nil
	}
	return MCPDecisionOn, nil
}

// IsEnabled reports whether entry may start in workspace.
func (s *MCPActivationStore) IsEnabled(entry PluginEntry, workspace string) (bool, error) {
	d, err := s.Decision(entry, workspace)
	if err != nil {
		return false, err
	}
	return d == MCPDecisionOn, nil
}

// MCPServerDecision resolves entry against the default store; an unreadable
// store leaves a project-declared server awaiting the user.
func MCPServerDecision(entry PluginEntry, workspace string) MCPDecision {
	d, _ := DefaultMCPActivationStore().Decision(entry, workspace)
	return d
}

// MCPServerEnabled resolves entry against the default store, failing closed.
func MCPServerEnabled(entry PluginEntry, workspace string) bool {
	return MCPServerDecision(entry, workspace) == MCPDecisionOn
}

// RecordExplicitStart records the user's approval when they start a
// project-declared server by hand, so the choice outlives the session.
func RecordExplicitStart(entry PluginEntry, workspace string) error {
	if !RepositoryDeclared(entry) || MCPServerDecision(entry, workspace) == MCPDecisionOn {
		return nil
	}
	return DefaultMCPActivationStore().SetServerEnabled(entry, workspace, true)
}

// MCPLaunchLine renders what a server would run, for a person deciding on it.
// Every env key is named; values show for launchEnvShown, whose keys change
// which code a process loads. The full declaration is `reasonix mcp get`.
func MCPLaunchLine(entry PluginEntry) string {
	var parts []string
	for _, k := range sortedKeys(entry.Env) {
		if launchEnvShown(k) {
			parts = append(parts, k+"="+strconv.Quote(entry.Env[k]))
		} else {
			parts = append(parts, k+"=…")
		}
	}
	if t := strings.ToLower(strings.TrimSpace(entry.Type)); t != "" && t != "stdio" {
		parts = append(parts, t, RedactMCPURL(entry.URL))
		if len(entry.Headers) > 0 {
			parts = append(parts, "headers:"+strings.Join(sortedKeys(entry.Headers), ","))
		}
		return strings.Join(parts, " ")
	}
	return strings.TrimSpace(strings.Join(append(append(parts, entry.Command), entry.Args...), " "))
}

// launchEnvShown is a security allow-list: loader and interpreter variables
// that make a process load code other than its command, and the package
// indexes npx, uvx and pip fetch that code from. Keys compare case-insensitively.
func launchEnvShown(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	switch k {
	case "PATH", "NODE_OPTIONS", "NODE_PATH", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "RUBYOPT", "RUBYLIB", "PERL5OPT", "PERL5LIB",
		"BASH_ENV", "ENV", "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "CLASSPATH",
		"PERLLIB", "PERL5DB", "ELECTRON_RUN_AS_NODE", "LUA_INIT",
		"NPM_CONFIG_REGISTRY", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL":
		return true
	}
	return strings.HasPrefix(k, "DYLD_")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// projectDeclarationDigest covers everything a project file controls about a
// launch: the declaration as written, and the inputs it draws from the
// workspace (see declarationInputs).
func projectDeclarationDigest(entry PluginEntry, workspace string) string {
	in := declarationInputsOf(entry, workspace)
	payload, _ := json.Marshal(struct {
		Declaration   json.RawMessage
		DotEnv, Files map[string]string
	}{declarationText(entry), in.DotEnv, in.Files})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// declarationText is the declaration as written in the project file.
func declarationText(entry PluginEntry) json.RawMessage {
	text, _ := json.Marshal(struct {
		Type, Command, URL string
		Args               []string
		Env, Headers       map[string]string
	}{
		strings.ToLower(strings.TrimSpace(entry.Type)), entry.Command, entry.URL,
		nonEmpty(entry.Args), nonEmptyMap(entry.Env), nonEmptyMap(entry.Headers),
	})
	return text
}

// declarationInputs is what a declaration draws from the workspace: the project
// .env values it expands and the content of workspace files it names directly
// as command or argument.
type declarationInputs struct {
	DotEnv, Files map[string]string
}

func declarationInputsOf(entry PluginEntry, workspace string) declarationInputs {
	in := declarationInputs{DotEnv: map[string]string{}, Files: map[string]string{}}
	record := func(name string) (string, bool) {
		v, ok := entry.expansionEnv[name]
		in.DotEnv[name] = v
		return v, ok
	}
	fields := append([]string{entry.Command, entry.URL}, entry.Args...)
	for _, v := range entry.Env {
		fields = append(fields, v)
	}
	for _, v := range entry.Headers {
		fields = append(fields, v)
	}
	for _, f := range fields {
		expandVarsWithLookup(f, record)
	}
	expanded := entry.ExpandedPlugin()
	for _, candidate := range append([]string{expanded.Command}, expanded.Args...) {
		if path, sum := workspaceFileDigest(workspace, candidate); sum != "" {
			in.Files[path] = sum
		}
	}
	return in
}

// within reports whether every input in reads the same as it did in before.
func (in declarationInputs) within(before declarationInputs, beforeDotEnv map[string]string) bool {
	for name, v := range in.DotEnv {
		if beforeDotEnv[name] != v {
			return false
		}
	}
	for path, sum := range in.Files {
		if before.Files[path] != sum {
			return false
		}
	}
	return true
}

// nonEmpty and nonEmptyMap fold an empty collection into nil, so a declaration
// digests the same whichever way a file round-trip spells "none".
func nonEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func nonEmptyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func workspaceFileDigest(workspace, candidate string) (string, string) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || strings.TrimSpace(workspace) == "" {
		return "", ""
	}
	path := candidate
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	rel, err := filepath.Rel(workspace, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return rel, "unreadable"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return rel, "unreadable"
	}
	return filepath.ToSlash(rel), hex.EncodeToString(h.Sum(nil))
}

// UpsertPluginKeepingDecision writes entry to its source file, keeping the
// user's decision on it; see KeepMCPDecisionAcross.
func UpsertPluginKeepingDecision(root string, entry PluginEntry) (string, error) {
	var path string
	err := KeepMCPDecisionAcross(root, entry.Name, func() (PluginEntry, error) {
		var werr error
		path, werr = UpsertPluginInSourceForRoot(root, entry)
		written, _ := NormalizePluginCommandLine(entry)
		return written, werr
	})
	return path, err
}

// KeepMCPDecisionAcross runs an edit Reasonix makes to server name's
// declaration at the user's request; write returns the declaration it wrote.
// A server the user had enabled stays enabled only if the file now holds that
// declaration and every workspace input it draws on reads as it did in the
// approved state before the edit.
func KeepMCPDecisionAcross(root, name string, write func() (PluginEntry, error)) error {
	before, ok := loadedPlugin(root, name)
	wasOn := ok && MCPServerEnabled(before, root)
	var beforeInputs declarationInputs
	if wasOn {
		beforeInputs = declarationInputsOf(before, root)
	}
	written, err := write()
	if err != nil || !wasOn {
		return err
	}
	p, ok := loadedPlugin(root, name)
	if !ok || !RepositoryDeclared(p) || string(declarationText(written)) != string(declarationText(p)) {
		return nil
	}
	if !declarationInputsOf(p, root).within(beforeInputs, before.expansionEnv) {
		return nil
	}
	return DefaultMCPActivationStore().SetServerEnabled(p, root, true)
}

func loadedPlugin(root, name string) (PluginEntry, bool) {
	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		return PluginEntry{}, false
	}
	for _, p := range cfg.Plugins {
		if p.Name == name {
			return p, true
		}
	}
	return PluginEntry{}, false
}
