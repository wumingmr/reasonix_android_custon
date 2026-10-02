package main

import "reasonix/internal/session"

// ProjectNode is one node in the sidebar project tree (a project folder or a
// topic leaf).
type ProjectNode struct {
	Source                       *SessionSourceRef   `json:"source,omitempty"`
	Historical                   bool                `json:"historical,omitempty"`
	HistoricalBranch             bool                `json:"historicalBranch,omitempty"`
	PreparationStatus            string              `json:"preparationStatus,omitempty"`
	IdentityAliases              []string            `json:"identityAliases,omitempty"`
	LifecycleGeneration          uint64              `json:"lifecycleGeneration,omitempty"`
	TabID                        string              `json:"tabId,omitempty"`
	Session                      *session.SessionRef `json:"session,omitempty"`
	ParentSession                *session.SessionRef `json:"parentSession,omitempty"`
	SessionOrigin                string              `json:"sessionOrigin,omitempty"`
	CanArchive                   bool                `json:"canArchive,omitempty"`
	Key                          string              `json:"key"`  // stable key for React
	Kind                         string              `json:"kind"` // "project" | "topic" | "session" | "global_folder" | "global_topic" | "global_session"
	Label                        string              `json:"label"`
	Root                         string              `json:"root,omitempty"` // project workspace root
	TopicID                      string              `json:"topicId,omitempty"`
	SessionPath                  string              `json:"sessionPath,omitempty"`
	Preview                      string              `json:"preview,omitempty"`
	ProjectColor                 string              `json:"projectColor,omitempty"`
	Turns                        int                 `json:"turns,omitempty"`
	TurnsState                   string              `json:"turnsState,omitempty"`
	Health                       string              `json:"health,omitempty"`
	CreatedAt                    int64               `json:"createdAt,omitempty"`
	LastActivityAt               int64               `json:"lastActivityAt,omitempty"`
	ResultSequence               uint64              `json:"resultSequence,omitempty"`
	Open                         bool                `json:"open,omitempty"`
	Running                      bool                `json:"running,omitempty"`
	Status                       string              `json:"status,omitempty"`
	Pinned                       bool                `json:"pinned,omitempty"`
	SortOrder                    int                 `json:"sortOrder"` // manual topic order index (0-based); -1 when unknown
	Recovered                    bool                `json:"recovered,omitempty"`
	RecoveryReason               string              `json:"recoveryReason,omitempty"`
	RecoveryDigest               string              `json:"recoveryDigest,omitempty"`
	RecoveryParentID             string              `json:"recoveryParentId,omitempty"`
	RecoveryState                string              `json:"recoveryState,omitempty"`
	RecoveryBranchCount          int                 `json:"recoveryBranchCount,omitempty"`
	RecoveryUnresolvedCount      int                 `json:"recoveryUnresolvedCount,omitempty"`
	RecoveryCleanupEligibleCount int                 `json:"recoveryCleanupEligibleCount,omitempty"`
	// RecoveryCopyCount is retained for compatibility with older desktop
	// frontends. Ordinary project-tree payloads intentionally leave it at zero:
	// physical recovery copies are an internal persistence detail.
	RecoveryCopyCount int           `json:"recoveryCopyCount,omitempty"`
	IsolatedWorktree  bool          `json:"isolatedWorktree,omitempty"`
	Remote            *RemoteTabRef `json:"remote,omitempty"`
	RuntimeOnly       bool          `json:"runtimeOnly,omitempty"`
	Children          []ProjectNode `json:"children,omitempty"`
}
