package main

import (
	"reasonix/internal/control"
	"reasonix/internal/gitcmd"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
)

// TabMeta is the frontend-facing shape of one tab.
type TabMeta struct {
	ID               string        `json:"id"`
	Scope            string        `json:"scope"`
	WorkspaceRoot    string        `json:"workspaceRoot"`
	WorkspaceID      string        `json:"workspaceId,omitempty"`
	WorkspaceName    string        `json:"workspaceName"`
	WorkspacePath    string        `json:"workspacePath,omitempty"`
	GitBranch        string        `json:"gitBranch,omitempty"`
	IsolatedWorktree bool          `json:"isolatedWorktree,omitempty"`
	Remote           *RemoteTabRef `json:"remote,omitempty"`
	// RemoteState seeds restored remote shells before their first state event.
	RemoteState string `json:"remoteState,omitempty"`
	// ForkTargetsSupported reports whether this tab can fork a turn into an
	// independent child session. False for a local tab and for a remote serve
	// before its handshake; always emitted, so absence never means "unsupported".
	ForkTargetsSupported           bool                `json:"forkTargetsSupported"`
	InteractionTargetSupported     bool                `json:"interactionTargetSupported"`
	ExtensionFormInstanceSupported bool                `json:"extensionFormInstanceSupported"`
	TopicID                        string              `json:"topicId"`
	TopicTitle                     string              `json:"topicTitle"`
	SessionPath                    string              `json:"sessionPath,omitempty"`
	SessionID                      string              `json:"sessionId,omitempty"`
	Session                        *session.SessionRef `json:"session,omitempty"`
	SessionRevision                int64               `json:"sessionRevision,omitempty"`
	SessionDigest                  string              `json:"sessionDigest,omitempty"`
	SessionGeneration              uint64              `json:"sessionGeneration,omitempty"`
	ReadOnly                       bool                `json:"readOnly,omitempty"`
	// TakenOver marks a local or remote tab spectating a session whose writer is
	// on the other side of a cooperative handoff.
	TakenOver            bool                         `json:"takenOver,omitempty"`
	ProjectColor         string                       `json:"projectColor,omitempty"`
	Label                string                       `json:"label"`
	Ready                bool                         `json:"ready"`
	Runtime              SessionRuntimeView           `json:"runtime"`
	Running              bool                         `json:"running"`
	TurnStartedAt        int64                        `json:"turnStartedAt,omitempty"`
	PendingPrompt        bool                         `json:"pendingPrompt,omitempty"`
	RemoteControlled     bool                         `json:"remoteControlled,omitempty"`
	BackgroundJobs       int                          `json:"backgroundJobs,omitempty"`
	CancelRequested      bool                         `json:"cancelRequested,omitempty"`
	Cancellable          bool                         `json:"cancellable"`
	TurnID               string                       `json:"turnId,omitempty"`
	TurnStatus           string                       `json:"turnStatus,omitempty"`
	TurnEventSeq         uint64                       `json:"turnEventSeq,omitempty"`
	TurnReplayAfter      uint64                       `json:"turnReplayAfterSeq,omitempty"`
	Mode                 string                       `json:"mode"`
	CollaborationMode    string                       `json:"collaborationMode"`
	ToolApprovalMode     string                       `json:"toolApprovalMode"`
	TokenMode            string                       `json:"tokenMode"`
	AgentPreset          string                       `json:"agentPreset,omitempty"`
	QualityFloor         string                       `json:"qualityFloor,omitempty"`
	FloorInferred        bool                         `json:"floorInferred,omitempty"`
	Goal                 string                       `json:"goal,omitempty"`
	GoalStatus           string                       `json:"goalStatus,omitempty"`
	GoalView             *goaldomain.View             `json:"goalView,omitempty"`
	Recovered            bool                         `json:"recovered,omitempty"`
	RecoveryReason       string                       `json:"recoveryReason,omitempty"`
	RecoveryDigest       string                       `json:"recoveryDigest,omitempty"`
	RecoveryParentID     string                       `json:"recoveryParentId,omitempty"`
	VersionKind          string                       `json:"versionKind,omitempty"`
	VersionState         string                       `json:"versionState,omitempty"`
	ParentVersionID      string                       `json:"parentVersionId,omitempty"`
	StartupErr           string                       `json:"startupErr,omitempty"`
	HistoricalSource     *SessionSourceRef            `json:"historicalSource,omitempty"`
	Authentication       *control.AuthenticationState `json:"authentication,omitempty"`
	ModelSettingsPending bool                         `json:"modelSettingsPending,omitempty"`
	Active               bool                         `json:"active"`
	Cwd                  string                       `json:"cwd"`
	// repo is the tab session's git identity for WorkspaceRoot; never serialized.
	repo gitcmd.Repo
}

// setAuthenticationMeta only exposes pending settings belonging to this runtime.
// A replacement controller must never inherit an older generation's hint.
func (m *TabMeta) setAuthenticationMeta(tab *WorkspaceTab) {
	if auth, ok := tab.Ctrl.(interface {
		AuthenticationState() control.AuthenticationState
	}); ok {
		state := auth.AuthenticationState()
		m.Authentication = &state
	}
	if extras := tab.metaExtras.Load(); extras != nil && extras.controller == tab.Ctrl {
		m.ModelSettingsPending = extras.modelSettingsPending
	}
}
