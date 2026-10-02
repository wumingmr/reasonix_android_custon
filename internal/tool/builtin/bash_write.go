package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"reasonix/internal/permissionpreset"
	"reasonix/internal/sandbox"
	"reasonix/internal/tool"
)

func (b bash) Schema() json.RawMessage {
	if b.resolved().Kind == sandbox.ShellPowerShell {
		return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"PowerShell command to execute"},"description":{"type":"string","description":"Clear 5-10 word active-voice description shown in the UI"},"timeout_ms":{"type":"integer","minimum":1,"description":"Optional foreground timeout in milliseconds, capped by the configured shell timeout"},"run_in_background":{"type":"boolean","description":"Run without a foreground timeout and return a pwsh job id immediately. Read it with job_output or stop it with job_kill."},"additional_write_dirs":{"type":"array","items":{"type":"string"},"description":"Directories this command must write outside the workspace. Directories only, no globs. Accepts absolute paths, workspace-relative paths, ~, and ${HOME}. Request the smallest set needed; the host will not infer paths from the command text."},"sandbox_permissions":{"type":"string","enum":["workspace-write","danger-full-access"],"description":"Optional per-call permission escalation. Use workspace-write for an authorized write while the session is read-only. danger-full-access is accepted only after a host-recorded denial and explicit authorization."},"justification":{"type":"string","description":"Required when additional_write_dirs or sandbox_permissions is set. Explain why the access is needed."},"denial_id":{"type":"string","description":"Host-issued denial identifier required when retrying with danger-full-access."}},"required":["command","description"]}`)
	}
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute"},"timeout_ms":{"type":"integer","minimum":1,"description":"Optional foreground timeout in milliseconds, capped by the configured shell timeout"},"run_in_background":{"type":"boolean","description":"Run detached: returns a job id immediately and keeps running across turns (no foreground timeout). Read it with job_output or stop it with job_kill."},"preserve_background_processes":{"type":"boolean","description":"After the shell command exits normally, keep any process-group members it intentionally left behind. Use only for deliberate daemonization, browser/GUI/session launchers such as playwright-cli open, or nohup/disown/setsid; cancellation and timeouts still kill the process group."},"additional_write_dirs":{"type":"array","items":{"type":"string"},"description":"Directories this command must write outside the workspace. Directories only, no globs. Accepts absolute paths, workspace-relative paths, ~, and ${HOME}. Request the smallest set needed; the host will not infer paths from the command text."},"sandbox_permissions":{"type":"string","enum":["workspace-write","danger-full-access"],"description":"Optional per-call permission escalation. Use workspace-write for an authorized write while the session is read-only. danger-full-access is accepted only after a host-recorded denial and explicit authorization."},"justification":{"type":"string","description":"Required when additional_write_dirs or sandbox_permissions is set. Explain why the access is needed."},"denial_id":{"type":"string","description":"Host-issued denial identifier required when retrying with danger-full-access."}},"required":["command"]}`)
}

func (b bash) DeclareWriteAccess(args json.RawMessage) (tool.WriteAccessDeclaration, error) {
	var p bashParams
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.WriteAccessDeclaration{}, fmt.Errorf("invalid args: %w", err)
	}
	if err := validateBashWriteDirs(p); err != nil {
		return tool.WriteAccessDeclaration{}, err
	}
	return tool.WriteAccessDeclaration{
		Directories:     append([]string(nil), p.AdditionalWriteDirs...),
		Justification:   strings.TrimSpace(p.Justification),
		RequestedPreset: strings.TrimSpace(p.SandboxPermissions),
		DenialID:        strings.TrimSpace(p.DenialID),
	}, nil
}

func validateBashWriteDirs(p bashParams) error {
	preset := strings.TrimSpace(p.SandboxPermissions)
	if preset != "" && preset != string(permissionpreset.WorkspaceWrite) && preset != string(permissionpreset.DangerFullAccess) {
		return fmt.Errorf("sandbox_permissions must be workspace-write or danger-full-access")
	}
	if preset != "" && strings.TrimSpace(p.Justification) == "" {
		return fmt.Errorf("justification is required when sandbox_permissions is set")
	}
	if preset == string(permissionpreset.DangerFullAccess) && strings.TrimSpace(p.DenialID) == "" {
		return fmt.Errorf("denial_id is required when sandbox_permissions is danger-full-access")
	}
	if len(p.AdditionalWriteDirs) == 0 {
		return nil
	}
	if strings.TrimSpace(p.Justification) == "" {
		return fmt.Errorf("justification is required when additional_write_dirs is set")
	}
	for _, dir := range p.AdditionalWriteDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return fmt.Errorf("additional_write_dirs entries must be non-empty directories")
		}
		if strings.ContainsAny(dir, "*?[") {
			return fmt.Errorf("additional_write_dirs %q must be a concrete directory, not a glob", dir)
		}
	}
	return nil
}

func validateBashParams(p bashParams) error {
	if p.Command == "" {
		return fmt.Errorf("command is required")
	}
	if p.TimeoutMS < 0 {
		return fmt.Errorf("timeout_ms must be positive")
	}
	return validateBashWriteDirs(p)
}

func bashPreflightFailure(ex *tool.ShellExecution, start time.Time, err error) (tool.DetailedResult, error) {
	ex.State = tool.ShellStateNotRun
	ex.FailurePhase = tool.ShellPhasePreflight
	ex.MutationRisk = tool.ShellMutationNotStarted
	ex.DurationMs = time.Since(start).Milliseconds()
	return tool.DetailedResult{Execution: ex}, err
}

func bashLaunchFailure(ex *tool.ShellExecution, start time.Time, err error) (tool.DetailedResult, error) {
	ex.State = tool.ShellStateNotRun
	// prepareLaunch has not entered the native runner yet. Its failures are
	// missing host dependencies (sandbox backend or session temp), not ACL/token
	// authorization and not a child-process launch.
	ex.FailurePhase = tool.ShellPhaseDependency
	ex.MutationRisk = tool.ShellMutationNotStarted
	ex.DurationMs = time.Since(start).Milliseconds()
	return tool.DetailedResult{Execution: ex}, err
}

func (b bash) appendWriteHints(ctx context.Context, out string, err error, p bashParams, wrapped bool) string {
	out = appendSessionDataHint(out, b.guard.CommandHint(b.workDir, p.Command))
	if wrapped {
		out = appendSandboxWriteHint(out, err, p, b.specForCall(ctx), string(sandbox.PermissionPresetFrom(ctx)), b.workDir)
	}
	return out
}

func (b bash) specForCall(ctx context.Context) sandbox.Spec {
	spec := b.sb
	preset := sandbox.PermissionPresetFrom(ctx)
	switch preset {
	case permissionpreset.ReadOnly:
		spec.Mode = "enforce"
		spec.ReadOnly = true
		spec.WriteRoots = nil
		spec.MinimalWrites = true
	case permissionpreset.WorkspaceWrite:
		// Permission presets own the enforcement decision. A legacy
		// [sandbox].bash="off" cannot silently turn workspace access into an
		// unconfined shell.
		spec.Mode = "enforce"
		spec.ReadOnly = false
		spec.MinimalWrites = true
		if len(spec.WriteRoots) == 0 && strings.TrimSpace(b.workDir) != "" {
			spec.WriteRoots = []string{b.workDir}
		}
	case permissionpreset.DangerFullAccess:
		spec.Mode = "off"
		spec.ReadOnly = false
	}
	// Windows has no OS-level shell sandbox: demanding one made every
	// restricted-preset shell call fail closed (#10292). Presets stay tool-layer
	// boundaries there and bash runs as the OS user after the approval gate.
	if !sandbox.OSSandboxSupported() {
		spec.Mode = "off"
	}
	if preset == permissionpreset.WorkspaceWrite {
		if b.rootSet != nil {
			spec.WriteRoots = b.rootSet.EffectiveSandboxRoots(ctx)
		} else if extra := sandbox.PerCallWriteRoots(ctx); len(extra) > 0 {
			spec.WriteRoots = sandbox.CollapseWriteRoots(append(append([]string{}, spec.WriteRoots...), extra...))
		}
	}
	if spec.ProtectedWriteRoots == nil && b.guard.stateRoot != "" {
		spec.ProtectedWriteRoots = sandbox.ProtectedWriteRoots(b.guard.stateRoot)
	}
	return spec
}

func bashWriteDeniedHint() string {
	return "The OS sandbox blocked a write outside the approved writable roots. Retry the same command with structured additional_write_dirs naming the exact directories (no globs), plus a justification. Example: {\"command\":\"mkdir -p ~/.local/bin && cp tool ~/.local/bin/tool\",\"additional_write_dirs\":[\"~/.local\"],\"justification\":\"install the user-requested local command\"}. Do not retry unconfined and do not omit the directories."
}

func looksLikeSandboxWriteDenial(out string, err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(out)
	if err != nil {
		msg += "\n" + strings.ToLower(err.Error())
	}
	for _, needle := range []string{
		"operation not permitted",
		"read-only file system",
		"erofs",
		"access is denied",
		"permissionerror: [errno 13] permission denied",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	// A bare "permission denied" can be an HTTP response or application-level
	// error. Accept it only in the standard local filesystem diagnostic shape
	// emitted by shells and file utilities.
	return localFilePermissionDenied.MatchString(msg) || windowsChildProcessDenied.MatchString(msg)
}

var localFilePermissionDenied = regexp.MustCompile(`(?m)^(?:bash|zsh|sh|dash|fish|mkdir|touch|cp|mv|rm|ln|install|tee|cat|chmod|chown):[^\n]*permission denied\b`)
var windowsChildProcessDenied = regexp.MustCompile(`\b(?:spawn(?:sync)?|exec(?:file|sync)?)\s+eperm\b`)

func appendSandboxWriteHint(out string, err error, p bashParams, spec sandbox.Spec, preset, workDir string) string {
	if !spec.Enforce() || strings.TrimSpace(preset) == string(permissionpreset.DangerFullAccess) {
		return out
	}
	// git reports some refused config writes and still exits 0, so the note
	// rides on the protected path's identity, not on the exit status.
	if named := gitMetadataNamedIn(out, spec, workDir); len(named) > 0 {
		hint := gitMetadataDeniedHint(named)
		if denialID := sandbox.IssueDenial(p.Command, preset); denialID != "" {
			hint += " If the user asked for exactly this change, request danger-full-access for this exact retry with denial_id " + denialID + "."
		}
		return appendSessionDataHint(out, hint)
	}
	if !looksLikeSandboxWriteDenial(out, err) {
		return out
	}
	hint := bashWriteDeniedHint()
	if windowsChildProcessDenied.MatchString(strings.ToLower(out + "\n" + err.Error())) {
		hint = "The command encountered a permission denial under the OS sandbox. Additional writable directories may not resolve a child-process or named-object denial."
	} else if dirs := gitWorktreeWriteDirs(workDir, out, spec.WriteRoots); len(dirs) > 0 {
		paths, _ := json.Marshal(dirs)
		hint = "Git worktree metadata is outside the writable workspace. Retry this command with additional_write_dirs: " + string(paths) + " and a justification; the host will request approval for these directories."
	} else if len(p.AdditionalWriteDirs) > 0 {
		hint = "The command encountered a permission denial under the OS sandbox. Additional writable directories may not resolve a child-process or named-object denial."
	}
	if denialID := sandbox.IssueDenial(p.Command, preset); denialID != "" {
		hint += " If the command cannot be expressed with additional_write_dirs, request danger-full-access for this exact retry with denial_id " + denialID + "."
	}
	return appendSessionDataHint(out, hint)
}

// gitMetadataNamedIn returns the protected Git metadata paths a failed
// command's output names, spelled as it names them. The paths are the host's
// own identities; nothing here reads the wording around them.
func gitMetadataNamedIn(output string, spec sandbox.Spec, workDir string) []string {
	var named []string
	for _, path := range sandbox.GitMetadataPaths(spec) {
		for _, spelling := range gitMetadataSpellings(path, workDir) {
			if containsPathToken(output, spelling) {
				named = append(named, spelling)
				break
			}
		}
	}
	return named
}

// containsPathToken reports whether path occurs in output as a whole path, not
// as the tail or prefix of a longer one: `.git` inside `main/.git/objects` is
// not the workspace's `.git`. A directory spelling ends in a separator.
func containsPathToken(output, path string) bool {
	dirSpelling := strings.HasSuffix(path, string(filepath.Separator))
	for from := 0; ; {
		i := strings.Index(output[from:], path)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(path)
		if (start == 0 || !isPathByte(output[start-1])) && (dirSpelling || end == len(output) || !isPathByte(output[end])) {
			return true
		}
		from = start + 1
	}
}

func isPathByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._-/\\~", c) >= 0
}

func gitMetadataSpellings(path, workDir string) []string {
	out := []string{path}
	if base, err := sandbox.ResolveAbsPath(workDir); err == nil {
		if rel, err := filepath.Rel(base, path); err == nil && filepath.IsLocal(strings.TrimSuffix(rel, string(filepath.Separator))) {
			if strings.HasSuffix(path, string(filepath.Separator)) {
				rel += string(filepath.Separator)
			}
			out = append(out, rel)
		}
	}
	return out
}

func gitMetadataDeniedHint(named []string) string {
	return "[host] " + sandbox.GitMetadataDeniedCode + ": " + strings.Join(named, ", ") +
		" is Git configuration or hooks that the host's own git reads, so the sandbox keeps it read-only; a write there did not happen even if the command exited 0. additional_write_dirs cannot grant it, and everything else under .git stays writable."
}

func gitWorktreeWriteDirs(workDir, output string, writeRoots []string) []string {
	gitDir, commonDir := linkedGitMetadataDirs(workDir)
	if gitDir == "" {
		return nil
	}
	match := gitWriteDeniedPath.FindStringSubmatch(output)
	if len(match) != 2 || !filepath.IsAbs(match[1]) {
		return nil
	}
	denied, err := sandbox.ResolveAbsPath(match[1])
	if err != nil {
		return nil
	}
	objects := filepath.Join(commonDir, "objects")
	refs := filepath.Join(commonDir, "refs")
	logs := filepath.Join(commonDir, "logs")
	if !sandbox.PathWithin(gitDir, denied) && !sandbox.PathWithin(objects, denied) && !sandbox.PathWithin(refs, denied) && !sandbox.PathWithin(logs, denied) {
		return nil
	}
	paths := []string{gitDir, objects}
	if sandbox.PathWithin(refs, denied) {
		paths = append(paths, refs)
	}
	if sandbox.PathWithin(logs, denied) {
		paths = append(paths, logs)
	}
	var missing []string
	for _, path := range paths {
		covered := false
		for _, root := range writeRoots {
			if sandbox.PathWithin(root, path) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, path)
		}
	}
	return missing
}

var gitWriteDeniedPath = regexp.MustCompile(`(?im)['"]([^'"\n]+)['"]:[ \t]*(?:operation not permitted|read-only file system|permission denied)`)

func linkedGitMetadataDirs(workDir string) (string, string) {
	for dir := filepath.Clean(workDir); filepath.IsAbs(dir); dir = filepath.Dir(dir) {
		gitPath := filepath.Join(dir, ".git")
		pointer, err := readSmallGitFile(gitPath)
		if err == nil {
			if !strings.HasPrefix(pointer, "gitdir: ") {
				return "", ""
			}
			gitDir := strings.TrimSpace(strings.TrimPrefix(pointer, "gitdir: "))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(dir, gitDir)
			}
			gitDir, err = filepath.EvalSymlinks(gitDir)
			if err != nil {
				return "", ""
			}
			common, err := readSmallGitFile(filepath.Join(gitDir, "commondir"))
			if err != nil {
				return "", ""
			}
			commonDir := common
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(gitDir, commonDir)
			}
			commonDir, err = filepath.EvalSymlinks(commonDir)
			if err != nil || filepath.Dir(gitDir) != filepath.Join(commonDir, "worktrees") {
				return "", ""
			}
			backlink, err := readSmallGitFile(filepath.Join(gitDir, "gitdir"))
			if err != nil {
				return "", ""
			}
			if !filepath.IsAbs(backlink) {
				backlink = filepath.Join(gitDir, backlink)
			}
			backlink, err = sandbox.ResolveAbsPath(backlink)
			if err != nil {
				return "", ""
			}
			actualGitPath, err := sandbox.ResolveAbsPath(gitPath)
			if err != nil || backlink != actualGitPath {
				return "", ""
			}
			for _, path := range []string{"objects", "refs"} {
				info, err := os.Lstat(filepath.Join(commonDir, path))
				if err != nil || !info.IsDir() {
					return "", ""
				}
			}
			if info, err := os.Lstat(filepath.Join(commonDir, "HEAD")); err != nil || !info.Mode().IsRegular() {
				return "", ""
			}
			return gitDir, commonDir
		} else if !os.IsNotExist(err) {
			return "", ""
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return "", ""
}

func readSmallGitFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", fmt.Errorf("not a small regular Git metadata file: %s", path)
	}
	data, err := os.ReadFile(path)
	return strings.TrimSpace(string(data)), err
}
