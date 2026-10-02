package event

import (
	goaldomain "reasonix/internal/goal"
	"reasonix/internal/nilutil"
)

// Todo is the v2 execution protocol's complete current-turn todo item. It is
// intentionally flat; legacy hierarchy and sign-off fields never enter this
// runtime projection.
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// PendingInteraction is an immutable identity from the controller's shared
// interaction registry. Answer content and authorization are never exposed in
// the replaceable runtime snapshot.
type PendingInteraction struct {
	RequestID    string `json:"requestId"`
	ToolCallID   string `json:"toolCallId,omitempty"`
	Kind         string `json:"kind"`
	HeadID       string `json:"headId"`
	TurnID       string `json:"turnId"`
	RuntimeEpoch string `json:"runtimeEpoch"`
}

// MaintenanceState is the currently active controller-owned maintenance job.
// It is absent for ordinary turns and idle sessions.
type MaintenanceState struct {
	OperationID       string `json:"operationId"`
	OperationRevision uint64 `json:"operationRevision,omitempty"`
	RuntimeEpoch      string `json:"runtimeEpoch,omitempty"`
	Kind              string `json:"kind"`
	Activity          string `json:"activity"`
	Status            string `json:"status,omitempty"`
	ErrorCode         string `json:"errorCode,omitempty"`
	Detail            string `json:"detail,omitempty"`
	Applied           bool   `json:"applied,omitempty"`
	InputTokens       int    `json:"inputTokens,omitempty"`
	ResultTokens      int    `json:"resultTokens,omitempty"`
	Messages          int    `json:"messages,omitempty"`
}

// RuntimeStateSnapshot is a host-only, replaceable observation. It is never a
// transcript or durable turn record. Running retains the legacy admission gate.
type RuntimeStateSnapshot struct {
	SchemaVersion    int                  `json:"schemaVersion"`
	HostID           string               `json:"hostId,omitempty"`
	SessionID        string               `json:"sessionId,omitempty"`
	SessionCodec     string               `json:"sessionCodec,omitempty"`
	ProjectionEpoch  string               `json:"projectionEpoch"`
	RuntimeEpoch     string               `json:"runtimeEpoch"`
	ActivityRevision uint64               `json:"activityRevision"`
	Revision         uint64               `json:"revision"`
	Phase            string               `json:"phase"`
	Running          bool                 `json:"running"`
	TurnID           string               `json:"turnId"`
	TurnStatus       TurnStatus           `json:"turnStatus"`
	TurnEventSeq     uint64               `json:"turnEventSeq"`
	CommittedSeq     uint64               `json:"committedEventSeq"`
	DurableSeq       uint64               `json:"durableEventSeq"`
	Persistence      string               `json:"persistenceStatus"`
	PersistenceErr   string               `json:"persistenceError,omitempty"`
	HeadID           string               `json:"headId"`
	PendingPrompt    bool                 `json:"pendingPrompt"`
	Interactions     []PendingInteraction `json:"pendingInteractions"`
	Todos            []Todo               `json:"todos"`
	TodoWritten      bool                 `json:"todoWritten"`
	CancelRequested  bool                 `json:"cancelRequested"`
	Cancellable      bool                 `json:"cancellable"`
	BackgroundJobs   int                  `json:"backgroundJobs"`
	Activity         string               `json:"activity"`
	Recovery         *RecoveryStatus      `json:"recovery,omitempty"`
	Goal             *goaldomain.View     `json:"goal,omitempty"`
	GoalError        string               `json:"goalError,omitempty"`
	Maintenance      *MaintenanceState    `json:"maintenance,omitempty"`
}

func (s RuntimeStateSnapshot) ActiveWork() bool {
	return s.Running || s.PendingPrompt || s.BackgroundJobs > 0
}

// RuntimeStateSink is independent of Emit: a state refresh must not become a
// new ledger record, extension invocation, or model-visible message.
type RuntimeStateSink interface{ RuntimeStateChanged(RuntimeStateSnapshot) }

func PublishRuntimeState(sink Sink, snapshot RuntimeStateSnapshot) {
	if nilutil.IsNil(sink) {
		return
	}
	if target, ok := sink.(RuntimeStateSink); ok {
		target.RuntimeStateChanged(snapshot)
	}
}

func (f AuditForwarder) RuntimeStateChanged(s RuntimeStateSnapshot) { PublishRuntimeState(f.Inner, s) }
func (s *syncSink) RuntimeStateChanged(snapshot RuntimeStateSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	PublishRuntimeState(s.inner, snapshot)
}
func (c *coalescer) RuntimeStateChanged(snapshot RuntimeStateSnapshot) {
	c.enqueueCapability(func() { PublishRuntimeState(c.inner, snapshot) })
}
