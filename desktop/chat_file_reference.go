package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Chat file references let the renderer submit answer-named paths for host
// verification. Resolution reads no content and every action revalidates the
// path, because one successful lookup is not a standing authorization.

const (
	// chatFileReferenceBatchLimit caps one request; the renderer splits larger
	// sets. chatFileReferenceMaxChars keeps a pathological candidate from
	// becoming a path lookup.
	chatFileReferenceBatchLimit = 64
	chatFileReferenceMaxChars   = 4096
)

// ChatFileReferenceRequest is one deduplicated renderer candidate. Key is the
// renderer's own identity for the candidate and is echoed back untouched.
type ChatFileReferenceRequest struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

// ChatFileReference is the per-candidate verdict. Actions is always present so
// the menu never has to guess an affordance from a file extension.
type ChatFileReference struct {
	Key         string   `json:"key"`
	Path        string   `json:"path"`
	Status      string   `json:"status"`
	DisplayPath string   `json:"displayPath,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Actions     []string `json:"actions"`
	Reason      string   `json:"reason,omitempty"`
}

// ChatFileReferenceResult echoes the turn the renderer asked about, so a late
// response can be rejected against the answer still on screen.
type ChatFileReferenceResult struct {
	TurnKey    string              `json:"turnKey"`
	References []ChatFileReference `json:"references"`
}

// ResolveChatFileReferencesForTab verifies answer-named paths against the
// session bound to tabID. The renderer never supplies a working directory or a
// host platform; the host decides both.
func (a *App) ResolveChatFileReferencesForTab(tabID, turnKey string, candidates []ChatFileReferenceRequest) ChatFileReferenceResult {
	out := ChatFileReferenceResult{TurnKey: turnKey, References: make([]ChatFileReference, 0, len(candidates))}
	if len(candidates) > chatFileReferenceBatchLimit {
		candidates = candidates[:chatFileReferenceBatchLimit]
	}
	for _, candidate := range candidates {
		out.References = append(out.References, a.resolveChatFileReference(tabID, candidate))
	}
	return out
}

func (a *App) resolveChatFileReference(tabID string, candidate ChatFileReferenceRequest) ChatFileReference {
	ref := ChatFileReference{Key: candidate.Key, Path: candidate.Path, Actions: []string{}}
	requested := strings.TrimSpace(candidate.Path)
	switch {
	case requested == "":
		return chatReferenceFailure(ref, "unsupported", "invalid")
	case len(requested) > chatFileReferenceMaxChars:
		// The candidate stays on screen as plain text; only its link is refused.
		return chatReferenceFailure(ref, "unsupported", "too-long")
	}
	resolved, display, reason := a.chatReferencePathForTab(tabID, requested)
	if reason != "" {
		status := "unavailable"
		if reason == "invalid" || reason == "not-a-file" {
			status = "unsupported"
		}
		return chatReferenceFailure(ref, status, reason)
	}
	kind, mime := previewMediaKind(resolved)
	ref.Status = "resolved"
	ref.DisplayPath = display
	ref.Kind = kind
	ref.Actions = chatReferenceActions(kind, mime, true)
	return ref
}

func chatReferenceFailure(ref ChatFileReference, status, reason string) ChatFileReference {
	ref.Status = status
	ref.Reason = reason
	return ref
}

// chatReferencePathForTab resolves and authorizes one candidate. There is no
// trusted `present` declaration to match, so the file must already live inside
// the session workspace or inside a directory the user registered for this
// session — matching the read surface the file tree itself exposes.
//
// The returned display path stays in the same path space the dock already
// navigates: workspace-relative for files inside the workspace, absolute for an
// authorized external folder.
func (a *App) chatReferencePathForTab(tabID, candidate string) (resolved, display, reason string) {
	root, ctrl, ok := a.workspaceTargetForTab(tabID)
	if !ok {
		return "", "", "unknown-session"
	}
	source, err := localChatPathSource(candidate)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "", "", "outside-workspace"
		}
		return "", "", "invalid"
	}
	if browser := externalFolderRefBrowserFromController(ctrl); browser != nil {
		if path, refDisplay, found := browser.ExternalFolderRefLocalPath(source); found {
			return validateChatReferencePath(root, path, refDisplay)
		}
	}
	if filepath.IsAbs(source) {
		if authorizer, ok := ctrl.(interface {
			AuthorizedExternalFolderLocalPath(string) (string, bool)
		}); ok {
			if external, allowed := authorizer.AuthorizedExternalFolderLocalPath(source); allowed {
				return validateChatReferencePath(root, external, filepath.ToSlash(external))
			}
		}
	}
	base, err := workspaceBaseFromRoot(root)
	if err != nil {
		return "", "", "unknown-session"
	}
	joined, inside, err := workspacePathForBase(base, source)
	if err != nil || !inside {
		return "", "", "outside-workspace"
	}
	// The lexical join is not containment: a symlinked component can still
	// leave the workspace, so compare the real locations.
	contained, realBase, err := canonicalPathWithin(base, joined)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", "not-found"
		}
		return "", "", "outside-workspace"
	}
	relative, err := filepath.Rel(realBase, contained)
	if err != nil {
		return "", "", "outside-workspace"
	}
	return validateChatReferencePath(root, contained, filepath.ToSlash(relative))
}

func validateChatReferencePath(workspaceRoot, resolved, display string) (string, string, string) {
	info, err := os.Lstat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", "not-found"
		}
		return "", "", "unreadable"
	}
	// Containment is already proven on the real path. Directories, devices, and
	// other non-regular targets have no preview action.
	if !info.Mode().IsRegular() {
		return "", "", "not-a-file"
	}
	if !readPolicyAllowsPath(workspaceRoot, resolved) {
		return "", "", "blocked"
	}
	return resolved, display, ""
}

// chatReferenceActions lists what the host will actually perform for this file.
// SVG and HTML are text formats: they preview as images or pages but must still
// offer the source view, which is the one affordance an extension guess gets
// wrong. Local media references use the same revalidated browser preview path
// as workspace and presented files.
func chatReferenceActions(kind, mime string, local bool) []string {
	actions := []string{"preview", "reveal-tree", "copy-path", "save-copy"}
	if kind == "" || mime == "image/svg+xml" || strings.HasPrefix(mime, "text/html") {
		actions = append(actions, "source")
	}
	if local {
		actions = append(actions, "open-native", "reveal-native")
		if kind != "" {
			actions = append(actions, "browser")
		}
	}
	return actions
}

// ReadReferenceFileForTab reads a verified answer reference. The path is
// re-resolved on every call, so a deleted file, a changed permission, or a
// replaced session surfaces as a local error instead of falling back to another
// file.
func (a *App) ReadReferenceFileForTab(tabID, path string) FilePreview {
	resolved, display, reason := a.chatReferencePathForTab(tabID, path)
	if reason != "" {
		return FilePreview{Path: path, Err: reason}
	}
	return a.readFilePathForTab(tabID, display, resolved, false)
}

func (a *App) ReadReferenceFileSourceForTab(tabID, path string) FilePreview {
	resolved, display, reason := a.chatReferencePathForTab(tabID, path)
	if reason != "" {
		return FilePreview{Path: path, Err: reason}
	}
	return a.readFilePathForTab(tabID, display, resolved, true)
}

// ResolveReferencePathForTab returns the absolute path for display and copy
// actions, after the same re-validation every other reference action performs.
func (a *App) ResolveReferencePathForTab(tabID, path string) (string, error) {
	resolved, _, reason := a.chatReferencePathForTab(tabID, path)
	if reason != "" {
		return "", chatReferenceError(reason)
	}
	return resolved, nil
}

func (a *App) OpenReferencePathForTab(tabID, path string) error {
	resolved, _, reason := a.chatReferencePathForTab(tabID, path)
	if reason != "" {
		return chatReferenceError(reason)
	}
	return openWorkspacePath(resolved)
}

func (a *App) RevealReferencePathForTab(tabID, path string) error {
	resolved, _, reason := a.chatReferencePathForTab(tabID, path)
	if reason != "" {
		return chatReferenceError(reason)
	}
	return revealPath(resolved)
}

func (a *App) SaveReferencePathAsForTab(tabID, path string) (string, error) {
	resolved, _, reason := a.chatReferencePathForTab(tabID, path)
	if reason != "" {
		return "", chatReferenceError(reason)
	}
	return a.SaveLocalPathAs(resolved)
}

// chatReferenceError is the caller-facing form of a reason code. The renderer
// shows its own localized text for the codes it recognizes.
func chatReferenceError(reason string) error {
	switch reason {
	case "not-found":
		return os.ErrNotExist
	case "outside-workspace", "blocked":
		return os.ErrPermission
	default:
		return os.ErrInvalid
	}
}
