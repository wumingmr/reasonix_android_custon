package main

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/session"
	"reasonix/internal/worktree"
)

const rewindForkAttachError = "conversation fork was created but could not be opened; open the recovery branch from session history"

// forkTabBeforePublishHookForTest forces the persistence-to-publish interleaving.
var forkTabBeforePublishHookForTest atomic.Pointer[func()]

type forkedSessionTabOpen struct {
	tab                 TabMeta
	workspaceReferenced bool
}

// ForkWorktreeResultView distinguishes a real isolated fork from a safe shared
// fallback and from a dirty-source refusal. The ordinary ForkForTab contract is
// intentionally unchanged for embedded frontend/backend compatibility.
type ForkWorktreeResultView struct {
	Tab              TabMeta `json:"tab"`
	Isolated         bool    `json:"isolated"`
	FallbackToShared bool    `json:"fallbackToShared,omitempty"`
	SourceDirty      bool    `json:"sourceDirty,omitempty"`
	Branch           string  `json:"branch,omitempty"`
}

// forkForTabWithOptions forks the requested source tab, optionally creating an
// isolated Git worktree for the new tab so changes in the fork do not mutate the
// source workspace.
func (a *App) forkForTabWithOptions(tabID string, turn int, isolateWorkspace bool) (ForkWorktreeResultView, error) {
	sourceTab, ctrl := a.tabAndCtrlByID(tabID)
	if sourceTab == nil || ctrl == nil {
		return ForkWorktreeResultView{}, nil
	}
	if a.tabIsReadOnly(sourceTab) {
		return ForkWorktreeResultView{}, readOnlyChannelErr()
	}
	if err := a.ensureTabControllerWorkspace(sourceTab); err != nil {
		return ForkWorktreeResultView{}, err
	}
	a.mu.RLock()
	if a.tabs[sourceTab.ID] != sourceTab || sourceTab.Ctrl == nil {
		a.mu.RUnlock()
		return ForkWorktreeResultView{}, nil
	}
	ctrl = sourceTab.Ctrl
	scope := sourceTab.Scope
	srcRoot := sourceTab.WorkspaceRoot
	a.mu.RUnlock()

	result := ForkWorktreeResultView{}
	var created worktree.Result
	if isolateWorkspace {
		if scope != "project" || strings.TrimSpace(srcRoot) == "" {
			result.FallbackToShared = true
		} else {
			srcRepo, _ := workspaceRepo(srcRoot, ctrl)
			avail := inspectDeliveryWorktree(a.bootContext(), srcRepo)
			if !avail.Available {
				result.FallbackToShared = true
			} else if avail.SourceDirty {
				result.SourceDirty = true
				return result, nil
			} else {
				var createErr error
				created, createErr = func() (worktree.Result, error) {
					releaseAdmission, err := a.beginWorkspaceRuntimeAdmission(srcRoot)
					if err != nil {
						return worktree.Result{}, err
					}
					defer releaseAdmission()
					return createDeliveryWorktree(a.bootContext(), srcRepo, config.DeliveryWorktreeDir())
				}()
				if createErr != nil {
					return ForkWorktreeResultView{}, fmt.Errorf("create isolated fork worktree: %w", createErr)
				}
				if created.SourceDirty {
					if rollbackErr := rollbackDeliveryWorktree(a.bootContext(), created); rollbackErr != nil {
						return ForkWorktreeResultView{}, fmt.Errorf("source changed while creating isolated worktree at %s; automatic cleanup failed: %w", created.WorktreeRoot, rollbackErr)
					}
					result.SourceDirty = true
					return result, nil
				}
				result.Isolated = true
				result.Branch = created.Branch
			}
		}
	}

	// Chat forks always become independent sessions so the source remains in
	// the sidebar and the child can be addressed, renamed, and reopened on its
	// own. This also applies to schema-2 transcripts; in-log heads remain an
	// implementation detail for recovery and rewind operations.
	newPath, err := ctrl.ForkSession(turn, "")
	if err != nil {
		return ForkWorktreeResultView{}, a.rollbackUnusedForkWorktree(created, err)
	}
	exclusiveV3 := false
	if identity, ok := ctrl.(control.IdentityLifecycle); ok {
		exclusiveV3 = identity.UsesExclusiveSession()
	}
	if !exclusiveV3 {
		if err := copyPinnedContextState(ctrl.SessionPath(), newPath); err != nil {
			cleanupErr := removeDesktopSessionArtifacts(newPath)
			return ForkWorktreeResultView{}, a.rollbackUnusedForkWorktree(created, errors.Join(err, cleanupErr))
		}
	}
	locator := forkedSessionLocator{SessionPath: newPath}
	if exclusiveV3 {
		locator = forkedSessionLocator{SessionID: newPath}
		if err := a.attachForkedDesktopSession(a.bootContext(), sourceTab, newPath); err != nil {
			return ForkWorktreeResultView{}, a.rollbackUnusedForkWorktree(created, fmt.Errorf("publish fork workspace membership: %w", err))
		}
	}
	opened, err := a.openForkedSessionTabWithWorkspace(sourceTab, locator, created.WorkspaceRoot)
	result.Tab = opened.tab
	if err != nil {
		if opened.workspaceReferenced {
			return result, err
		}
		return ForkWorktreeResultView{}, a.rollbackUnusedForkWorktree(created, err)
	}
	if result.Tab.ID == "" {
		if opened.workspaceReferenced {
			return result, errors.New(rewindForkAttachError)
		}
		return ForkWorktreeResultView{}, a.rollbackUnusedForkWorktree(created, errors.New(rewindForkAttachError))
	}
	return result, nil
}

func (a *App) rollbackUnusedForkWorktree(created worktree.Result, cause error) error {
	if strings.TrimSpace(created.WorktreeRoot) == "" {
		return cause
	}
	if err := rollbackDeliveryWorktree(a.bootContext(), created); err != nil {
		return errors.Join(cause, fmt.Errorf("preserve unused isolated worktree at %s after cleanup failed: %w", created.WorktreeRoot, err))
	}
	return cause
}

// openForkedSessionTab attaches an already-written fork session to a new tab.
// The source tab keeps its controller and transcript. The fork becomes active
// only while the source tab still owns focus.
func (a *App) openForkedSessionTab(sourceTab *WorkspaceTab, newPath string) (TabMeta, error) {
	locator := forkedSessionLocator{SessionPath: newPath}
	if identity, ok := sourceTab.Ctrl.(control.IdentityLifecycle); ok && identity.UsesExclusiveSession() {
		locator = forkedSessionLocator{SessionID: newPath}
	}
	opened, err := a.openForkedSessionTabWithWorkspace(sourceTab, locator, "")
	return opened.tab, err
}

// forkedSessionLocator prevents an immutable v3 session id from entering the
// legacy path catalog, where filepath.Dir("session-id") would become ".".
type forkedSessionLocator struct {
	SessionID   string
	SessionPath string
}

func normalizeForkedSessionLocator(sourceTab *WorkspaceTab, locator forkedSessionLocator) (forkedSessionLocator, error) {
	locator.SessionID = strings.TrimSpace(locator.SessionID)
	locator.SessionPath = strings.TrimSpace(locator.SessionPath)
	if sourceTab == nil || (locator.SessionID == "") == (locator.SessionPath == "") {
		return forkedSessionLocator{}, fmt.Errorf("fork tab needs exactly one session id or session path")
	}
	if locator.SessionPath == "." || (locator.SessionPath != "" && filepath.Base(locator.SessionPath) == locator.SessionPath) {
		return forkedSessionLocator{}, fmt.Errorf("fork tab needs a concrete session path")
	}
	return locator, nil
}

// prepareForkedTopic commits the presentation before a tab can publish it.
// Retries reuse the durable topic identity rather than making a second one.
func (a *App) prepareForkedTopic(locator forkedSessionLocator, scope, workspaceRoot, sourceTitle string, sourceCtrl control.SessionAPI) (string, string, string, error) {
	topicID := newTopicID()
	topicTitle := a.forkTopicTitle(sourceTitle)
	titleSource := topicTitleSourceManual
	exclusiveV3 := false
	if identity, ok := sourceCtrl.(control.IdentityLifecycle); ok {
		exclusiveV3 = identity.UsesExclusiveSession()
	}
	if exclusiveV3 != (locator.SessionID != "") {
		return "", "", "", fmt.Errorf("fork tab locator does not match the source session engine")
	}
	if exclusiveV3 {
		if err := a.workspaceRegistry().EnsureSessionTopic(a.bootContext(), locator.SessionID, topicID, topicTitle); err != nil {
			return "", "", "", err
		}
		// Presentation is durable even if the source closes before tab publication.
		root := ""
		if scope == "project" {
			root = workspaceRoot
		}
		a.emitProjectTreeChangedV2(a.currentSessionCatalogStatus().Revision, []string{root}, "membership")
		snapshot, err := a.workspaceRegistry().VerifySnapshot(a.bootContext())
		if err != nil {
			return "", "", "", err
		}
		presentation := snapshot.Session(locator.SessionID).Presentation
		topicID = presentation.TopicID
		topicTitle, titleSource = a.canonicalTabTitleWithPresentation(a.bootContext(), presentation,
			session.SessionRef{HostID: localDesktopHostID, SessionID: locator.SessionID})
		return topicID, topicTitle, titleSource, nil
	}
	titleRoot := workspaceRoot
	if scope == "global" {
		titleRoot = ""
	}
	if err := setTopicTitle(titleRoot, topicID, topicTitle); err != nil {
		return "", "", "", err
	}
	m, _ := agent.EnsureBranchMeta(locator.SessionPath)
	m.Scope = scope
	m.WorkspaceRoot = workspaceRoot
	m.TopicID = topicID
	m.TopicTitle = topicTitle
	if err := agent.SaveBranchMeta(locator.SessionPath, m); err != nil {
		return "", "", "", err
	}
	invalidateTopicSessionIndexForPath(locator.SessionPath)
	return topicID, topicTitle, titleSource, nil
}

// openForkedSessionTabWithWorkspace attaches an already-written fork session to a new tab,
// optionally overriding the workspace root (e.g. for isolated Git worktrees).
func (a *App) openForkedSessionTabWithWorkspace(sourceTab *WorkspaceTab, locator forkedSessionLocator, workspaceRootOverride string) (forkedSessionTabOpen, error) {
	locator, err := normalizeForkedSessionLocator(sourceTab, locator)
	if err != nil {
		return forkedSessionTabOpen{}, err
	}
	a.mu.RLock()
	if a.tabs[sourceTab.ID] != sourceTab {
		a.mu.RUnlock()
		return forkedSessionTabOpen{}, nil
	}
	scope := sourceTab.Scope
	workspaceRoot := sourceTab.WorkspaceRoot
	if strings.TrimSpace(workspaceRootOverride) != "" {
		workspaceRoot = workspaceRootOverride
	}
	sourceTitle := sourceTab.TopicTitle
	model := sourceTab.model
	effort := cloneStringPtr(sourceTab.effort)
	mode := currentTabMode(sourceTab)
	disabledMCP := cloneServerViewMap(sourceTab.disabledMCP)
	mcpOrder := append([]string(nil), sourceTab.mcpOrder...)
	sourceCtrl := sourceTab.Ctrl
	a.mu.RUnlock()
	// A fork is a new session: the source's preset was chosen for the source.
	toolApprovalMode := a.sessionPresets.restore(locator.SessionID, newSessionPreset(config.LoadForEdit(config.UserConfigPath())))
	mode = tabModeFromAxes(tabModeHasPlan(mode), toolApprovalMode == control.ToolApprovalDangerFullAccess)
	if scope == "project" {
		releaseAdmission, err := a.beginWorkspaceRuntimeAdmission(workspaceRoot)
		if err != nil {
			return forkedSessionTabOpen{}, err
		}
		defer releaseAdmission()
	}

	topicID, topicTitle, titleSource, err := a.prepareForkedTopic(locator, scope, workspaceRoot, sourceTitle, sourceCtrl)
	if err != nil {
		return forkedSessionTabOpen{}, err
	}
	opened := forkedSessionTabOpen{workspaceReferenced: strings.TrimSpace(workspaceRootOverride) != ""}

	if opened.workspaceReferenced && scope == "project" {
		rememberWorkspace(workspaceRoot)
		if err := prependTopicInProjectsFile(workspaceRoot, topicID, true); err != nil {
			slog.Warn("desktop: persist isolated fork topic", "workspace", workspaceRoot, "topic", topicID, "err", err)
		}
		if err := a.registerProjectRoot(workspaceRoot); err != nil {
			return forkedSessionTabOpen{}, err
		}
	}
	if hook := forkTabBeforePublishHookForTest.Load(); hook != nil {
		(*hook)()
	}

	a.mu.Lock()
	if a.tabs[sourceTab.ID] != sourceTab {
		a.mu.Unlock()
		return opened, nil
	}
	newTabID := a.newUniqueTabIDLocked()
	childPath, childID := locator.SessionPath, locator.SessionID
	tab := &WorkspaceTab{
		ID:               newTabID,
		Scope:            scope,
		WorkspaceRoot:    workspaceRoot,
		TopicID:          topicID,
		TopicTitle:       topicTitle,
		topicTitleSource: titleSource,
		SessionPath:      childPath,
		SessionID:        childID,
		model:            model,
		effort:           effort,
		mode:             mode,
		toolApprovalMode: toolApprovalMode,
		disabledMCP:      disabledMCP,
		mcpOrder:         mcpOrder,
	}
	tab.sink = &tabEventSink{tabID: newTabID, app: a}
	a.tabs[newTabID] = tab
	a.tabOrder = append(a.tabOrder, newTabID)
	activateFork := a.activeTabID == sourceTab.ID
	if activateFork {
		a.activeTabID = newTabID
	}
	a.saveTabsLocked()
	meta := a.tabMeta(tab, activateFork)
	a.mu.Unlock()

	if opened.workspaceReferenced && scope == "project" {
		if activateFork {
			saveWorkspace(workspaceRoot)
		}
	}
	if childPath != "" {
		a.emitProjectTreeChangedForSessionDirs(sessionDirectoryForPath(childPath))
	} else {
		a.emitProjectTreeChangedEvent()
	}
	a.startTabControllerBuild(tab)
	opened.tab = meta
	return opened, nil
}

// attachForkedRewindTab fails closed when the durable branch cannot be attached
// to a tab. In particular, callers must not treat the source tab as the rewind
// target and accidentally resubmit the edited prompt into the parent session.
func (a *App) attachForkedRewindTab(sourceTab *WorkspaceTab, view RewindResultView) RewindResultView {
	meta, err := a.openForkedSessionTab(sourceTab, view.Branch)
	if err != nil || meta.ID == "" {
		slog.Warn("rewind: fork created but tab attach failed", "err", err)
		view.OK = false
		view.Partial = true
		view.Error = rewindForkAttachError
		return view
	}
	view.TabID = meta.ID
	view.Tab = &meta
	return view
}
