// Package sessioncatalog maintains a disposable SQLite projection of Reasonix
// session sidecars. Session JSONL/event/meta files remain authoritative; every
// row in this package may be discarded and rebuilt.
package sessioncatalog

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/historywork"
)

const (
	SchemaVersion       = 15
	repairEngineVersion = 1
	DefaultLimit        = 50
	MaxLimit            = 200
)

type TurnsState string

const (
	TurnsUnknown TurnsState = "unknown"
	TurnsValid   TurnsState = "valid"
	TurnsCorrupt TurnsState = "corrupt"
)

type Health string

const (
	HealthOK       Health = "ok"
	HealthMissing  Health = "missing"
	HealthCorrupt  Health = "corrupt"
	HealthDegraded Health = "degraded"
)

type State string

const (
	StateOpening    State = "opening"
	StateReady      State = "ready"
	StateDegraded   State = "degraded"
	StateRebuilding State = "rebuilding"
	StateClosed     State = "closed"
)

type Mode string

const (
	ModeDisk   Mode = "disk"
	ModeMemory Mode = "memory"
)

type Status struct {
	State                State            `json:"state"`
	Mode                 Mode             `json:"mode"`
	Path                 string           `json:"path,omitempty"`
	Revision             uint64           `json:"revision"`
	Indexed              int64            `json:"indexed"`
	Total                int64            `json:"total"`
	RepairPending        int64            `json:"repairPending"`
	RepairActive         int64            `json:"repairActive"`
	RepairDeferred       int64            `json:"repairDeferred"`
	RepairBlocked        int64            `json:"repairBlocked"`
	NextRepairAt         int64            `json:"nextRepairAt,omitempty"`
	RepairErrorKinds     map[string]int64 `json:"repairErrorKinds,omitempty"`
	LastRepairDurationMS int64            `json:"lastRepairDurationMs,omitempty"`
	PhysicalSessions     int64            `json:"physicalSessions"`
	LogicalSessions      int64            `json:"logicalSessions"`
	RecoveryGroups       int64            `json:"recoveryGroups"`
	RecoveryBranches     int64            `json:"recoveryBranches"`
	RecoveryDiverged     int64            `json:"recoveryDiverged"`
	CleanupEligible      int64            `json:"cleanupEligible"`
	// RepairReason records the last integrity condition that caused a
	// directory to be scanned instead of trusting its persisted projection.
	// It is diagnostic-only; the transcript and sidecars remain authoritative.
	RepairReason    string `json:"repairReason,omitempty"`
	SourceCount     int64  `json:"sourceCount"`
	LastRepairAt    int64  `json:"lastRepairAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	QuarantinedPath string `json:"quarantinedPath,omitempty"`
}

type Options struct {
	Path          string
	InMemory      bool
	DisableRepair bool
	// MetadataOnly never reads transcripts or repairs content as a side effect
	// of discovering sessions. Explicit content readers own that work.
	MetadataOnly bool
	// DeferredMetadataIntegrity is restricted to advisory metadata catalogs.
	// The owner must replace the catalog after Invalidated closes.
	DeferredMetadataIntegrity bool
	RevisionFloor             uint64
	StartPaused               bool // Desktop resumes discovery after the shell and watchers are ready.
	Maintenance               *historywork.Coordinator
	MissingGrace              time.Duration
	QueueCapacity             int
	Now                       func() time.Time
	OnRevision                func(uint64, []string, string)
	// OnDiscovery observes root admission and scan boundaries without source
	// paths or content. It must return promptly and must not call the catalog.
	OnDiscovery func(DiscoveryEvent)
	// repairSession replaces the filesystem repair. Open installs it before
	// starting repairLoop, so scheduler tests can drive the real wake path
	// without racing the hook assignment.
	repairSession     func(context.Context, string) (agent.SessionListingRepairResult, error)
	verifyMetadata    func(context.Context) error
	waitMetadataRetry func(context.Context, time.Duration) error
}

type DirectoryTarget struct {
	Path          string `json:"path"`
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	mutationSeq   uint64
}

type ProjectRecord struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	Title         string `json:"title"`
	Color         string `json:"color,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
	SortOrder     int    `json:"sortOrder,omitempty"`
}

type TopicMetadata struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	TopicID       string `json:"topicId"`
	Title         string `json:"title"`
	TitleSource   string `json:"titleSource,omitempty"`
	Pinned        bool   `json:"pinned,omitempty"`
	SortOrder     int    `json:"sortOrder,omitempty"`
	CreatedAt     int64  `json:"createdAt,omitempty"`
}

type SessionRecord struct {
	Path              string `json:"path"`
	pathKey           string
	enqueueSequence   uint64
	metadataUnchanged bool
	Directory         string     `json:"directory"`
	Scope             string     `json:"scope"`
	WorkspaceRoot     string     `json:"workspaceRoot,omitempty"`
	TopicID           string     `json:"topicId,omitempty"`
	TopicTitle        string     `json:"topicTitle,omitempty"`
	CustomTitle       string     `json:"customTitle,omitempty"`
	CreatedAt         int64      `json:"createdAt,omitempty"`
	LastActivityAt    int64      `json:"lastActivityAt,omitempty"`
	Preview           string     `json:"preview,omitempty"`
	Turns             int        `json:"turns"`
	TurnsState        TurnsState `json:"turnsState"`
	Recovered         bool       `json:"recovered,omitempty"`
	RecoveryReason    string     `json:"recoveryReason,omitempty"`
	RecoveryDigest    string     `json:"recoveryDigest,omitempty"`
	ParentID          string     `json:"parentId,omitempty"`
	RecoveryPreferred bool       `json:"recoveryPreferred,omitempty"`
	// RecoveryCopy is true only when real content is still covered by the parent.
	RecoveryCopy bool `json:"recoveryCopy,omitempty"`
	// RecoveryGroupID clusters a lineage of normal + recovery branches that
	// share content ancestry. Empty for ordinary non-recovery sessions.
	RecoveryGroupID string `json:"recoveryGroupId,omitempty"`
	// RecoveryRole is normal | covered_copy | adopted | preferred | diverged.
	RecoveryRole string `json:"recoveryRole,omitempty"`
	// RecoveryCanonical marks the unique leaf that covers the group and should
	// be opened by default. Never moves or rewrites files.
	RecoveryCanonical bool `json:"recoveryCanonical,omitempty"`
	// LogicalTopicID is the ordinary-list topic for this physical file. Recovery
	// copies are re-anchored onto the root conversation's topic in the catalog
	// only; authoritative sidecars keep their original topic_id.
	LogicalTopicID string `json:"logicalTopicId,omitempty"`
	// OrdinaryVisible is true only for the single logical representative that
	// may appear in the ordinary project tree.
	OrdinaryVisible bool `json:"ordinaryVisible,omitempty"`
	// LogFormat is 2 for sessions whose event log is the append-only DAG; the
	// head fields mirror its selected head and are zero for schema 1.
	LogFormat          int    `json:"logFormat,omitempty"`
	HeadCount          int    `json:"headCount,omitempty"`
	SelectedHeadID     string `json:"selectedHeadId,omitempty"`
	heads              []HeadRecord
	ContentFingerprint string `json:"contentFingerprint,omitempty"`
	MetaFingerprint    string `json:"metaFingerprint,omitempty"`
	Health             Health `json:"health"`
	MissingSince       int64  `json:"missingSince,omitempty"`
}

// Recovery role constants for catalog lineage classification.
const (
	RecoveryRoleNormal      = "normal"
	RecoveryRoleCoveredCopy = "covered_copy"
	RecoveryRoleAdopted     = "adopted"
	RecoveryRolePreferred   = "preferred"
	RecoveryRoleDiverged    = "diverged"
)

type TopicKey struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	TopicID       string `json:"topicId"`
	workspaceKey  string
}

type TopicRecord struct {
	Scope                        string     `json:"scope"`
	WorkspaceRoot                string     `json:"workspaceRoot,omitempty"`
	TopicID                      string     `json:"topicId"`
	Title                        string     `json:"title"`
	TitleSource                  string     `json:"titleSource,omitempty"`
	Pinned                       bool       `json:"pinned,omitempty"`
	SortOrder                    int        `json:"sortOrder,omitempty"`
	Turns                        int        `json:"turns"`
	TurnsState                   TurnsState `json:"turnsState"`
	CreatedAt                    int64      `json:"createdAt,omitempty"`
	LastActivityAt               int64      `json:"lastActivityAt,omitempty"`
	RecoveryState                string     `json:"recoveryState,omitempty"`
	RecoveryBranchCount          int        `json:"recoveryBranchCount,omitempty"`
	RecoveryUnresolvedCount      int        `json:"recoveryUnresolvedCount,omitempty"`
	RecoveryCleanupEligibleCount int        `json:"recoveryCleanupEligibleCount,omitempty"`
	// RepresentativePath is the automatic open target for this logical topic.
	RepresentativePath string          `json:"representativePath,omitempty"`
	Health             Health          `json:"health"`
	Sessions           []SessionRecord `json:"sessions"`
}

type TopicPageRequest struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Query         string `json:"query,omitempty"`
	TimeFilter    string `json:"timeFilter,omitempty"`
	SortMode      string `json:"sortMode,omitempty"`
	// IncludeTopicIDsJSON and ExcludeTopicIDsJSON carry a JSON string array into
	// SQLite's json_each table function. They keep large sidebar groups bounded
	// to one SQL parameter instead of expanding one placeholder per topic.
	IncludeTopicIDsJSON string `json:"-"`
	ExcludeTopicIDsJSON string `json:"-"`
	ExcludePinned       bool   `json:"-"`
	PinnedOnly          bool   `json:"-"`
	CursorBinding       string `json:"-"`
	// ManualOrder makes sort_order the primary key within each pinned bucket.
	// It is intentionally request-scoped: users who have never reordered keep
	// the activity/created ordering even though metadata rows have a sort value.
	ManualOrder bool `json:"manualOrder,omitempty"`
}

type TopicPage struct {
	Items      []TopicRecord `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
	Revision   uint64        `json:"revision"`
}

type SessionPageRequest struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	Directory     string `json:"-"`
	Cursor        string `json:"cursor,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Query         string `json:"query,omitempty"`
	TimeFilter    string `json:"timeFilter,omitempty"`
}

type SessionPage struct {
	Items       []SessionRecord `json:"items"`
	NextCursor  string          `json:"nextCursor,omitempty"`
	Revision    uint64          `json:"revision"`
	StaleCursor bool            `json:"staleCursor,omitempty"`
}

// DefaultPath is the disposable cache file under CacheDir ("" when unavailable).
// v10.sqlite isolates progressive maintenance state from older writers.
// Session JSONL/WAL/sidecars remain authoritative and older binaries may keep
// using their own disposable cache without cross-writing this one.
func DefaultPath() string {
	cache := strings.TrimSpace(config.CacheDir())
	if cache == "" {
		return ""
	}
	return filepath.Join(cache, "session-catalog", "v10.sqlite")
}
