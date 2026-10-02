package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/browser"
)

const fileBrowserPreviewTimeout = 60 * time.Second

// FileBrowserPreviewRequest is the additive desktop RPC contract used by file
// links, present cards, automatic delivery, and the browser_preview tool.
// ExpectedSessionGeneration may be zero for an older caller; current callers
// send it whenever they already hold tab metadata.
type FileBrowserPreviewRequest struct {
	Source                    string `json:"source"`
	Path                      string `json:"path"`
	ToolCallID                string `json:"toolCallId,omitempty"`
	OperationID               string `json:"operationId"`
	ExpectedSessionGeneration uint64 `json:"expectedSessionGeneration,omitempty"`
	// UserInitiated is accepted only by the renderer RPC. It lets an explicit
	// refresh replace the file URL while the user owns the tab without granting
	// the agent control of that page. Agent tool requests never set this field.
	UserInitiated bool `json:"userInitiated,omitempty"`
}

type FileBrowserPreviewResult struct {
	TabID             string `json:"tabId"`
	URL               string `json:"url"`
	Status            string `json:"status"`
	Error             string `json:"error,omitempty"`
	SessionGeneration uint64 `json:"sessionGeneration"`
}

type fileBrowserPreviewBinding struct {
	BrowserTabID      string
	URL               string
	SessionGeneration uint64
	release           func() // revokes the exact minted resource without App.mu
}

type preparedFileBrowserPreview struct {
	url      string
	identity string
	release  func()
}

// OpenFileBrowserPreviewForTab validates the resource and opens it through the
// same task grant the agent browser tools use. That makes the visible page and
// the page the agent can inspect one and the same Electron WebContentsView.
func (a *App) OpenFileBrowserPreviewForTab(tabID string, request FileBrowserPreviewRequest) (FileBrowserPreviewResult, error) {
	ctx, cancel := context.WithTimeout(a.bootContext(), fileBrowserPreviewTimeout)
	defer cancel()
	return a.openFileBrowserPreview(ctx, tabID, request, nil)
}

func normalizeFileBrowserPreviewRequest(request FileBrowserPreviewRequest) (FileBrowserPreviewRequest, error) {
	request.Source = strings.TrimSpace(request.Source)
	request.Path = strings.TrimSpace(request.Path)
	request.OperationID = strings.TrimSpace(request.OperationID)
	if request.Source == "" {
		request.Source = "workspace"
	}
	if request.Source != "workspace" && request.Source != "presented" && request.Source != "reference" {
		return FileBrowserPreviewRequest{}, fmt.Errorf("unsupported file preview source %q", request.Source)
	}
	if request.Path == "" || request.OperationID == "" {
		return FileBrowserPreviewRequest{}, errors.New("path and operationId are required")
	}
	if request.Source == "presented" && strings.TrimSpace(request.ToolCallID) == "" {
		return FileBrowserPreviewRequest{}, errors.New("toolCallId is required for a presented file")
	}
	return request, nil
}

func (a *App) openFileBrowserPreview(ctx context.Context, tabID string, request FileBrowserPreviewRequest, supplied browser.Executor) (FileBrowserPreviewResult, error) {
	request, err := normalizeFileBrowserPreviewRequest(request)
	if err != nil {
		return FileBrowserPreviewResult{}, err
	}
	// Serialize publications without making lifecycle cleanup wait on host RPC.
	a.filePreviews.operations.Lock()
	defer a.filePreviews.operations.Unlock()
	tab, generation, err := a.fileBrowserPreviewTab(tabID, request.ExpectedSessionGeneration)
	if err != nil {
		return FileBrowserPreviewResult{}, err
	}
	exec := supplied
	if exec == nil {
		exec = a.hostBrowserExecutorForTab(tab.ID)
	}
	if exec == nil {
		return FileBrowserPreviewResult{}, errors.New("the built-in browser is unavailable")
	}
	prepared, err := a.prepareFileBrowserPreview(tabID, request)
	if err != nil {
		return FileBrowserPreviewResult{}, err
	}
	key := strings.Join([]string{tabID, fmt.Sprint(generation), request.Source, strings.TrimSpace(request.ToolCallID), filepath.Clean(prepared.identity)}, "\x00")

	op, binding, reusable := a.filePreviews.begin(key, prepared)
	defer a.filePreviews.end(op)
	published := false
	defer func() {
		if !published {
			prepared.release()
		}
	}()
	if reusable {
		tabs, listErr := exec.Tabs(ctx)
		if listErr != nil {
			return FileBrowserPreviewResult{}, listErr
		}
		found := false
		for _, candidate := range tabs {
			if candidate.ID != binding.BrowserTabID {
				continue
			}
			found = true
			// Navigating away explicitly releases the file binding. A later
			// delivery opens a fresh tab instead of overwriting the user's page.
			if candidate.URL != binding.URL {
				a.filePreviews.retire(key, binding.URL)
				reusable = false
			}
			break
		}
		if !found {
			a.filePreviews.retire(key, binding.URL)
			reusable = false
		}
	}

	var browserTab browser.Tab
	if reusable {
		navigateRequest := browser.NavigateRequest{
			OperationID: request.OperationID, TabID: binding.BrowserTabID,
			URL: prepared.url, Action: browser.NavigateURL,
		}
		if request.UserInitiated {
			if renderer, ok := exec.(interface {
				navigateFilePreview(context.Context, browser.NavigateRequest) (browser.Tab, error)
			}); ok {
				browserTab, err = renderer.navigateFilePreview(ctx, navigateRequest)
			} else {
				browserTab, err = exec.Navigate(ctx, navigateRequest)
			}
		} else {
			browserTab, err = exec.Navigate(ctx, navigateRequest)
		}
	} else {
		browserTab, err = exec.Open(ctx, browser.OpenRequest{OperationID: request.OperationID, URL: prepared.url})
	}
	if err != nil {
		return FileBrowserPreviewResult{}, err
	}
	err = a.publishFileBrowserPreview(tab, generation, key, op, fileBrowserPreviewBinding{
		BrowserTabID: browserTab.ID, URL: prepared.url, SessionGeneration: generation, release: prepared.release,
	})
	if reusable && binding.URL != prepared.url {
		binding.release()
	}
	if err != nil {
		if reusable {
			a.filePreviews.retire(key, binding.URL)
		}
		// The request can be cancelled after the host has created the tab.
		// Cleanup needs its own bounded context or Close would fail immediately.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelCleanup()
		_ = exec.Close(cleanupCtx, browser.CloseRequest{OperationID: request.OperationID + "-stale-close", TabID: browserTab.ID})
		return FileBrowserPreviewResult{}, err
	}
	published = true
	if recovery, ok := exec.(interface {
		rememberFilePreview(context.Context, string, FileBrowserPreviewRequest)
	}); ok {
		recovery.rememberFilePreview(ctx, browserTab.ID, request)
	}
	status := "opened"
	errorText := browserTab.Error
	if browserTab.Loading {
		status = "loading"
	}
	if errorText != "" {
		status = "failed"
	}
	return FileBrowserPreviewResult{TabID: browserTab.ID, URL: prepared.url, Status: status, Error: errorText, SessionGeneration: generation}, nil
}

func (a *App) fileBrowserPreviewTab(tabID string, expected uint64) (*WorkspaceTab, uint64, error) {
	a.mu.RLock()
	tab := a.tabs[tabID]
	if tab == nil {
		a.mu.RUnlock()
		return nil, 0, errors.New("workspace tab is no longer available")
	}
	generation := tab.SessionGeneration
	a.mu.RUnlock()
	if expected != 0 && generation != expected {
		return nil, generation, errors.New("the session changed before the preview opened")
	}
	return tab, generation, nil
}

func (a *App) prepareFileBrowserPreview(tabID string, request FileBrowserPreviewRequest) (preparedFileBrowserPreview, error) {
	var preview FilePreview
	var identity string
	var err error
	switch request.Source {
	case "presented":
		preview = a.ReadPresentedFileForTab(tabID, request.ToolCallID, request.Path)
		identity, err = a.ResolvePresentedPathForTab(tabID, request.ToolCallID, request.Path)
	case "reference":
		preview = a.ReadReferenceFileForTab(tabID, request.Path)
		identity, err = a.ResolveReferencePathForTab(tabID, request.Path)
	default:
		preview = a.ReadFileForTab(tabID, request.Path)
		identity, err = a.ResolveWorkspacePathForTab(tabID, request.Path)
	}
	if err != nil {
		if preview.URL != "" {
			a.revokeWorkspaceMediaPath(preview.URL)
		}
		return preparedFileBrowserPreview{}, err
	}
	if preview.Err != "" {
		if preview.URL != "" {
			a.revokeWorkspaceMediaPath(preview.URL)
		}
		return preparedFileBrowserPreview{}, errors.New(preview.Err)
	}
	if preview.URL == "" {
		return preparedFileBrowserPreview{}, errors.New("this file type cannot be opened in the built-in browser")
	}
	origin, err := a.ensureWorkspacePreviewOrigin()
	if err != nil {
		a.revokeWorkspaceMediaPath(preview.URL)
		return preparedFileBrowserPreview{}, err
	}
	a.extendWorkspaceBrowserPreviewToken(preview.URL)
	store := a.ensureMediaTokenStore()
	token := strings.SplitN(strings.TrimPrefix(preview.URL, "/__reasonix_workspace_media/"), "/", 2)[0]
	return preparedFileBrowserPreview{url: origin + preview.URL, identity: identity, release: func() { store.revoke(token) }}, nil
}

func (a *App) releaseFileBrowserPreviewTab(browserTabID string) {
	if browserTabID == "" {
		return
	}
	a.filePreviews.releaseMatching(func(_ string, binding fileBrowserPreviewBinding) bool { return binding.BrowserTabID == browserTabID })
}

func (a *App) releaseFileBrowserPreviewURL(rawURL string) {
	if rawURL == "" {
		return
	}
	a.filePreviews.releaseMatching(func(_ string, binding fileBrowserPreviewBinding) bool { return binding.URL == rawURL })
}

func (a *App) releaseFileBrowserPreviewsForTask(tabID string) {
	if tabID == "" {
		return
	}
	prefix := tabID + "\x00"
	a.filePreviews.releaseMatching(func(key string, _ fileBrowserPreviewBinding) bool { return strings.HasPrefix(key, prefix) })
}
