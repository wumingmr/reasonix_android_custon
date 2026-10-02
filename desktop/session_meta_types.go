package main

import (
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/session"
)

// Meta describes the session for the frontend's header and status line.
type Meta struct {
	Label                 string                      `json:"label"`
	Ready                 bool                        `json:"ready"`
	Runtime               SessionRuntimeView          `json:"runtime"`
	StartupErr            string                      `json:"startupErr,omitempty"`
	HistoricalSource      *SessionSourceRef           `json:"historicalSource,omitempty"`
	EventChannel          string                      `json:"eventChannel"`
	SessionPath           string                      `json:"sessionPath,omitempty"`
	SessionID             string                      `json:"sessionId,omitempty"`
	Session               *session.SessionRef         `json:"session,omitempty"`
	SessionRevision       int64                       `json:"sessionRevision,omitempty"`
	SessionDigest         string                      `json:"sessionDigest,omitempty"`
	SessionGeneration     uint64                      `json:"sessionGeneration"`
	RuntimeStateSnapshot  *event.RuntimeStateSnapshot `json:"runtimeStateSnapshot,omitempty"`
	Cwd                   string                      `json:"cwd"`
	WorkspaceRoot         string                      `json:"workspaceRoot,omitempty"`
	WorkspaceName         string                      `json:"workspaceName,omitempty"`
	WorkspacePath         string                      `json:"workspacePath,omitempty"`
	GitBranch             string                      `json:"gitBranch,omitempty"`
	ImageInputEnabled     bool                        `json:"imageInputEnabled"`
	VisionFallbackEnabled bool                        `json:"visionFallbackEnabled,omitempty"`
	AutoApproveTools      bool                        `json:"autoApproveTools"`
	Bypass                bool                        `json:"bypass"` // legacy JSON key for YOLO/full-access tool auto-approval
	CollaborationMode     string                      `json:"collaborationMode"`
	ToolApprovalMode      string                      `json:"toolApprovalMode"`
	// TokenMode and AgentPreset are deprecated dual-write wire values pinned to
	// their safe defaults; one-version-old frontends still parse them.
	TokenMode   string           `json:"tokenMode"`
	AgentPreset string           `json:"agentPreset,omitempty"`
	Goal        string           `json:"goal,omitempty"`
	GoalStatus  string           `json:"goalStatus,omitempty"`
	GoalView    *goaldomain.View `json:"goalView,omitempty"`
	GoalRuntime *GoalRuntimeView `json:"goalRuntime,omitempty"`
	// Nil means no authoritative snapshot; non-nil empty means clear the panel.
	CanonicalTodos *[]evidence.TodoItem `json:"canonicalTodos,omitempty"`
	// PinnedFiles holds metadata about standing pinned context files for this tab.
	PinnedFiles []PinnedFileInfo `json:"pinnedFiles,omitempty"`
	// Remote marks a remote session tab; its readiness is carried by the
	// remote-tab state channel rather than a local controller.
	Remote *RemoteTabRef `json:"remote,omitempty"`
}

type GoalRuntimeView struct {
	TurnsUsed        int    `json:"turnsUsed"`
	TurnsLimit       int    `json:"turnsLimit"` // Deprecated: always 0.
	TokensUsed       int    `json:"tokensUsed"`
	RequestsUsed     int    `json:"requestsUsed,omitempty"`
	WorkDurationMs   int64  `json:"workDurationMs,omitempty"`
	TokensLimit      int    `json:"tokensLimit"` // Deprecated: always 0; retained for bridge compatibility.
	NoProgressTurns  int    `json:"noProgressTurns"`
	NoProgressLimit  int    `json:"noProgressLimit"` // Deprecated: always 0.
	LastReason       string `json:"lastReason,omitempty"`
	StopCause        string `json:"stopCause,omitempty"`
	BudgetExtensions int    `json:"budgetExtensions"` // Deprecated: always 0.
}
