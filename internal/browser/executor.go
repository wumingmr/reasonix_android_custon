package browser

import (
	"context"
	"errors"
	"time"
)

// Executor is the host-neutral browser contract. The local Electron shell and
// the remote SSH broker both implement it; the tools in this package are its
// only callers and never learn which host answers.
type Executor interface {
	Tabs(ctx context.Context) ([]Tab, error)
	Open(ctx context.Context, req OpenRequest) (Tab, error)
	Navigate(ctx context.Context, req NavigateRequest) (Tab, error)
	Snapshot(ctx context.Context, req SnapshotRequest) (Snapshot, error)
	Screenshot(ctx context.Context, req ScreenshotRequest) (Screenshot, error)
	Act(ctx context.Context, req ActRequest) (ActResult, error)
	Downloads(ctx context.Context, req DownloadsRequest) ([]Download, error)
	Close(ctx context.Context, req CloseRequest) error
}

// FilePreviewer is an optional desktop-only capability. It turns a file that
// the current task is already allowed to read into a task-owned browser tab.
// CLI/CDP executors intentionally need not implement it.
type FilePreviewer interface {
	PreviewFile(ctx context.Context, req FilePreviewRequest) (Tab, error)
}

// Availability is an optional Executor capability: a host whose grant can
// lapse (session switch, connection generation change) reports it here so the
// tools fail closed before any call is dispatched.
type Availability interface {
	Available(ctx context.Context) bool
}

var (
	// ErrStaleReference means the ref or documentToken predates a navigation,
	// page replacement, or take-over; the caller must snapshot again.
	ErrStaleReference = errors.New("browser: stale reference")
	// ErrTakenOver means the user is operating the tab; agent access resumes
	// only after a fresh snapshot once the tab leaves human mode.
	ErrTakenOver = errors.New("browser: tab taken over by the user")
	// ErrNoGrant means no current browser grant covers this task or tab.
	ErrNoGrant = errors.New("browser: no browser grant")
	// ErrUnknownOutcome means a write's receipt was lost: the action may or
	// may not have run and must never be replayed.
	ErrUnknownOutcome = errors.New("browser: operation outcome unknown")
)

// Navigate actions.
const (
	NavigateURL     = "url"
	NavigateBack    = "back"
	NavigateForward = "forward"
	NavigateReload  = "reload"
)

// Act actions.
const (
	ActionClick  = "click"
	ActionType   = "type"
	ActionPress  = "press"
	ActionScroll = "scroll"
	ActionSelect = "select"
	ActionUpload = "upload"
)

// Act outcomes.
const (
	OutcomeExecuted    = "executed"
	OutcomeNotExecuted = "not_executed"
	OutcomeUnknown     = "unknown"
)

// Tab is one browser tab owned by the current task.
type Tab struct {
	ID        string
	URL       string
	Title     string
	Loading   bool
	Temporary bool
	Error     string
}

type OpenRequest struct {
	OperationID string
	URL         string
	Temporary   bool
}

type FilePreviewRequest struct {
	OperationID string
	Source      string
	Path        string
	ToolCallID  string
}

// NavigateRequest moves a bound tab; URL is consulted only for NavigateURL.
type NavigateRequest struct {
	OperationID string
	TabID       string
	URL         string
	Action      string
}

type CloseRequest struct {
	OperationID string
	TabID       string
}

type SnapshotRequest struct {
	TabID    string
	Selector string
}

// Snapshot is the accessibility-style tree of one document version;
// DocumentToken binds every ref in Tree to that version.
type Snapshot struct {
	DocumentToken string
	URL           string
	Title         string
	Tree          string
	Refs          int
	Observation   *Observation `json:"observation,omitempty"`
}

type Observation struct {
	URL              string `json:"url"`
	TimeMS           int64  `json:"timeMs"`
	DocumentEpoch    int64  `json:"documentEpoch"`
	ViewportRevision int64  `json:"viewportRevision"`
	CSSWidth         int    `json:"cssWidth"`
	CSSHeight        int    `json:"cssHeight"`
}

type ScreenshotRequest struct {
	TabID    string
	Ref      string
	FullPage bool
}

// Screenshot names the task-owned file the host wrote; image bytes never
// travel through control frames.
type Screenshot struct {
	Observation      *Observation `json:"observation,omitempty"`
	Path             string
	MIME             string
	Width            int
	Height           int
	ObservationToken string `json:"observationToken,omitempty"`
	CSSWidth         int    `json:"cssWidth,omitempty"`
	CSSHeight        int    `json:"cssHeight,omitempty"`
}

// ActRequest is one reserved write. OperationID is minted by the model,
// unique per attempt, and rejected forever once used.
type ActRequest struct {
	OperationID   string
	TabID         string
	DocumentToken string
	Action        string
	Ref           string
	Text          string
	Keys          string
	Options       []string
	Files         []string
	Submit        bool
	DeltaX        int
	DeltaY        int
}

// ActResult is the host's receipt; Reason explains a not_executed Outcome.
type ActResult struct {
	Executed      bool
	Reason        string
	DocumentToken string
	Outcome       string
}

type DownloadsRequest struct {
	TabID   string
	WaitFor time.Duration
}

type Download struct {
	ID    string
	URL   string
	Path  string
	State string
	Bytes int64
}
