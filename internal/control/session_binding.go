package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/extension/dispatch"
	"reasonix/internal/permissionpreset"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// NativeLegacySession identifies a path-backed controller across hot rebuilds.
func (c *Controller) NativeLegacySession() bool {
	if c == nil {
		return false
	}
	c.v3BindingMu.RLock()
	defer c.v3BindingMu.RUnlock()
	return c.nativeLegacySession
}

// ResumeNativeSession switches to an already validated, path-backed transcript.
// Hosts serialize admission and acquire its path lease before calling this.
func (c *Controller) ResumeNativeSession(s *agent.Session, path string) error {
	if s == nil || strings.TrimSpace(path) == "" {
		return errors.New("native session and path are required")
	}
	if service := c.SessionService(); service != nil {
		if ref, found, err := service.ExistingCanonicalForLegacy(path, s); err != nil {
			return err
		} else if found {
			_, err = c.OpenSession(context.Background(), ref)
			return err
		}
	}
	if err := c.Snapshot(); err != nil {
		return err
	}
	_, previousRuntime, _ := c.v3Binding()
	if err := c.ReleaseSessionRuntimeBinding(); err != nil {
		return err
	}
	c.v3BindingMu.Lock()
	c.exclusiveSession = false
	c.nativeLegacySession = true
	c.v3BindingMu.Unlock()
	// The retired canonical cache is owned by its service, never by the path
	// adapter. Drop the borrowed pointer before rebinding legacy state.
	c.turnEvents.mu.Lock()
	if previousRuntime != nil {
		c.turnEvents.v3 = nil
		c.turnEvents.v3Runtime = nil
		c.turnEvents.v3Path = ""
		c.turnEvents.v3Release = nil
	}
	c.turnEvents.mu.Unlock()
	c.Resume(s, path)
	return c.turnEventLedgerError()
}

func bindInitialSessionRuntime(opts Options) (*session.Runtime, *session.ClientBinding) {
	runtime := opts.SessionRuntime
	if opts.SessionService == nil || runtime == nil {
		return runtime, nil
	}
	binding, err := opts.SessionService.Bind(runtime)
	if err != nil {
		return nil, nil
	}
	return runtime, binding
}

// ReleaseSessionRuntimeBinding drops this controller's client reference without
// tearing down the controller itself. Hosts use it when a session is handed
// back to another runtime while keeping the current process alive.
func (c *Controller) ReleaseSessionRuntimeBinding() error {
	if c == nil {
		return nil
	}
	c.v3BindingMu.Lock()
	binding := c.sessionBinding
	runtime := c.sessionRuntime
	c.sessionBinding = nil
	c.sessionRuntime = nil
	c.v3BindingMu.Unlock()
	c.unbindExecutionControl(runtime)
	if binding != nil {
		return binding.Release(context.Background())
	}
	return nil
}

// ReleaseSessionForHandoff hands the bound identity to another runtime without
// allocating a replacement: it flushes the runtime, drops this controller's
// client binding and empties the in-memory transcript, leaving the controller in
// the never-bound exclusive state whose next turn or NewSession allocates a
// fresh identity lazily. Closing the runtime, which drops the writer lock, stays
// with the host: the host owns the rollback (OpenSession) when that close is
// refused, and this controller is exactly re-attachable until then.
func (c *Controller) ReleaseSessionForHandoff() error {
	if c == nil {
		return nil
	}
	if _, runtime, exclusive := c.v3Binding(); !exclusive || runtime == nil {
		return session.ErrSessionNotRunning
	}
	if err := c.Snapshot(); err != nil {
		return err
	}
	if err := c.ReleaseSessionRuntimeBinding(); err != nil {
		return err
	}
	// Under snapshotMu so the swap cannot interleave with an in-flight save.
	// Emptying the transcript keeps a later Snapshot a no-op and keeps the
	// handed-off conversation out of the identity the next turn allocates.
	c.snapshotMu.Lock()
	if c.executor != nil {
		c.executor.SetSession(agent.NewSession(c.basePrompt()))
	}
	c.snapshotMu.Unlock()
	// With no runtime bound an exclusive controller has no event store, so
	// history, transcript pages and admission answer from nothing — the released
	// runtime's cached store must not keep serving the handed-off conversation.
	c.rebindTurnEvents("")
	return nil
}

// releaseSessionRuntimeBinding is the final controller teardown wrapper. It
// keeps the handoff-only release available without making normal Close paths
// responsible for surfacing a late binding-release error.
func (c *Controller) releaseSessionRuntimeBinding(service *session.Service) {
	if err := c.ReleaseSessionRuntimeBinding(); err != nil {
		slog.Warn("controller: release exclusive v3 binding", "err", err)
	} else if service == nil {
		slog.Warn("controller: exclusive v3 runtime has no service binding")
	}
}

// BindFreshSession creates and publishes a fresh identity-bound session. The caller
// may provide an id allocated by its protocol; an empty id lets persistence
// allocate one. Publication happens only after the initial event batch is
// accepted, so failure leaves the currently-bound session usable.
func (c *Controller) BindFreshSession(ctx context.Context, sessionID string) (session.SessionRef, error) {
	return c.BindFreshSessionWithOptions(ctx, session.CreateOptions{SessionID: sessionID})
}

// BindFreshSessionWithOptions creates a fresh identity with immutable host
// ownership metadata before publishing the runtime.
func (c *Controller) BindFreshSessionWithOptions(ctx context.Context, options session.CreateOptions) (session.SessionRef, error) {
	return c.bindFreshSessionWithCommit(ctx, options, nil)
}

func (c *Controller) bindFreshSessionWithCommit(ctx context.Context, options session.CreateOptions, commit func(context.Context, session.SessionRef) error) (session.SessionRef, error) {
	service := c.SessionCreationService()
	if c == nil || service == nil || c.executor == nil {
		return session.SessionRef{}, errors.New("v3 session service is unavailable")
	}
	prepared, err := service.PrepareCreate(ctx, options)
	if err != nil {
		return session.SessionRef{}, err
	}
	candidate := prepared.Runtime()
	fresh := agent.NewSession(c.basePrompt())
	if err := seedRuntimeSession(ctx, candidate, "session-create", fresh.Snapshot(), c.ModelRef(), c.ModelSelectionIdentity()); err != nil {
		_ = service.Discard(context.Background(), prepared)
		return session.SessionRef{}, err
	}
	if _, err := candidate.Session().Flush(ctx); err != nil {
		_ = service.Discard(context.Background(), prepared)
		return session.SessionRef{}, err
	}
	owner, err := service.Publish(prepared)
	if err != nil {
		_ = service.Discard(context.Background(), prepared)
		return session.SessionRef{}, err
	}
	if _, err = c.publishSessionRuntimeWithCommit(ctx, candidate, fresh, true, commit, service); err != nil {
		// This attempt published the identity, so an owner-scoped close is the
		// correct cleanup. It still refuses while any client is bound.
		_ = owner.Close(context.Background())
		return session.SessionRef{}, err
	}
	return candidate.Ref(), nil
}

// ContinueLegacySession freezes and migrates the selected legacy head, then
// publishes the returned immutable v3 identity. The source remains only as a
// display/import locator and is never rebound as the execution store.
func (c *Controller) ContinueLegacySession(ctx context.Context, sourcePath, headID string) (session.SessionRef, error) {
	return c.continueLegacySession(ctx, sourcePath, headID, true, session.CreateOptions{})
}

// ContinueLegacySessionWithOptions installs immutable Desktop ownership in
// the same publication that materializes the imported session.
func (c *Controller) ContinueLegacySessionWithOptions(ctx context.Context, sourcePath, headID string, options session.CreateOptions) (session.SessionRef, error) {
	return c.continueLegacySession(ctx, sourcePath, headID, true, options)
}

// ContinueLegacySessionForRebuildWithOptions performs the same fail-atomic
// import while an Agent generation is being replaced for the same logical
// session, and publishes host-owned immutable metadata in that same
// transaction. The SessionTemp generation belongs to the logical session, so
// this path must not rotate it merely because persistence crossed the
// legacy/v3 boundary.
func (c *Controller) ContinueLegacySessionForRebuildWithOptions(ctx context.Context, sourcePath, headID string, options session.CreateOptions) (session.SessionRef, error) {
	return c.continueLegacySession(ctx, sourcePath, headID, false, options)
}

func (c *Controller) continueLegacySession(ctx context.Context, sourcePath, headID string, rotateSessionTemp bool, options session.CreateOptions) (session.SessionRef, error) {
	service, _, _ := c.v3Binding()
	if c == nil || service == nil || c.executor == nil {
		return session.SessionRef{}, errors.New("v3 session service is unavailable")
	}
	restoreLegacyEvents, err := c.releaseLegacyEventStoreForImport(ctx)
	if err != nil {
		return session.SessionRef{}, fmt.Errorf("freeze legacy event source: %w", err)
	}
	published := false
	defer func() {
		if !published {
			restoreLegacyEvents()
		}
	}()
	candidate, _, err := service.ContinueImportedWithHeader(ctx, sourcePath, headID, options)
	if err != nil {
		return session.SessionRef{}, err
	}
	owner, err := service.Owner(candidate)
	if err != nil {
		return session.SessionRef{}, err
	}
	if err := seedRuntimeConfig(ctx, candidate, "legacy-import-config", c.ModelRef(), c.ModelSelectionIdentity()); err != nil {
		_ = owner.Close(context.Background())
		return session.SessionRef{}, err
	}
	messages := candidate.Session().ExecutionSnapshot().Projection.ModelMessages
	prepared := agent.NewSession("").CloneWithMessages(messages)
	if _, err = c.publishSessionRuntime(candidate, prepared, rotateSessionTemp); err != nil {
		_ = owner.Close(context.Background())
		return session.SessionRef{}, err
	}
	published = true
	return candidate.Ref(), nil
}

// ContinuePrototypeSession imports the retired sidecar codec through the restricted
// fail-closed bridge, then publishes the final linear session identity.
func (c *Controller) ContinuePrototypeSession(ctx context.Context, sourceDir string) (session.SessionRef, error) {
	service, _, _ := c.v3Binding()
	if c == nil || service == nil || c.executor == nil {
		return session.SessionRef{}, errors.New("v3 session service is unavailable")
	}
	candidate, _, err := service.ContinuePrototype(ctx, sourceDir)
	if err != nil {
		return session.SessionRef{}, err
	}
	owner, err := service.Owner(candidate)
	if err != nil {
		return session.SessionRef{}, err
	}
	if err := seedRuntimeConfig(ctx, candidate, "prototype-import-config", c.ModelRef(), c.ModelSelectionIdentity()); err != nil {
		_ = owner.Close(context.Background())
		return session.SessionRef{}, err
	}
	prepared := agent.NewSession("").CloneWithMessages(candidate.Session().ExecutionSnapshot().Projection.ModelMessages)
	if _, err = c.publishSessionRuntime(candidate, prepared, true); err != nil {
		_ = owner.Close(context.Background())
		return session.SessionRef{}, err
	}
	return candidate.Ref(), nil
}

// OpenSession attaches this Controller to an existing immutable session identity.
// Opening never creates a missing session and publication retains the current
// binding until the target projection and writer are ready.
//
// Attaching grants only a ClientBinding, so a failed publication withdraws this
// client's own grant instead of disposing a runtime another client may already
// be using. A retired stored codec is the one exception: importing it publishes
// a brand-new identity that this attempt owns outright.
func (c *Controller) OpenSession(ctx context.Context, ref session.SessionRef) (session.SessionRef, error) {
	service, current, _ := c.v3Binding()
	if c == nil || service == nil || c.executor == nil {
		return session.SessionRef{}, errors.New("v3 session service is unavailable")
	}
	if current != nil && current.Ref() == ref {
		// After a reclaim the controller still renders a runtime whose store
		// the service closed, so the next turn append would hit a closed
		// recovery database. Only a still-active instance may skip the re-open.
		if active, ok := service.Runtime(ref); ok && active == current {
			return ref, nil
		}
	}
	binding, err := service.Open(ctx, ref)
	if err != nil {
		return session.SessionRef{}, err
	}
	target := binding.Runtime()
	published, err := c.publishAttachedSession(target, "attach-existing", nil)
	if err == nil {
		// publishSessionRuntime installs the controller's own client grant; this
		// temporary attach grant is no longer needed.
		_ = binding.Release(context.Background())
		return published, nil
	}
	// Only this client's grant is withdrawn. A runtime another client still
	// holds keeps its binding count above zero and is left untouched.
	if releaseErr := binding.Release(context.Background()); releaseErr != nil {
		slog.Warn("controller: release failed v3 attach binding", "err", releaseErr)
	}
	return session.SessionRef{}, err
}

// publishAttachedSession publishes the prepared projection for an already-resolved
// runtime. retire is used only when this attempt owns a newly published
// identity; pass nil to withdraw a client grant instead.
func (c *Controller) publishAttachedSession(candidate *session.Runtime, reason string, retire func(context.Context) error) (session.SessionRef, error) {
	if candidate == nil {
		return session.SessionRef{}, errors.New("v3 session runtime is unavailable")
	}
	prepared := agent.NewSession("").CloneWithMessages(candidate.Session().ExecutionSnapshot().Projection.ModelMessages)
	if _, err := c.publishSessionRuntime(candidate, prepared, true); err != nil {
		if retire != nil {
			_ = retire(context.Background())
		}
		return session.SessionRef{}, err
	}
	return candidate.Ref(), nil
}

// SetSessionTitle records mutable title state in the canonical event stream.
func (c *Controller) SetSessionTitle(ctx context.Context, title string) error {
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil {
		return session.ErrSessionNotRunning
	}
	payload, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		return err
	}
	snapshot := runtime.Session().ExecutionSnapshot()
	_, err = c.appendSessionBatch(ctx, runtime.Session(), session.Batch{
		OperationID: "session-title:" + agent.NewMessageID(),
		TurnID:      snapshot.Projection.TurnID,
		Events:      []session.Event{{Kind: "session/title", Payload: payload}},
	})
	return err
}

func seedRuntimeSession(ctx context.Context, runtime *session.Runtime, operationID string, messages []provider.Message, modelRef, modelIdentity string) error {
	if runtime == nil {
		return nil
	}
	events := make([]session.Event, 0, len(messages)+1)
	for _, message := range messages {
		if message.ID == "" {
			return errors.New("initial v3 message has no stable id")
		}
		payload, err := json.Marshal(map[string]any{"message": message})
		if err != nil {
			return err
		}
		events = append(events, session.Event{Kind: "message/complete", Payload: payload})
	}
	if strings.TrimSpace(modelRef) != "" {
		config, err := sessionConfigEvent(modelRef, modelIdentity)
		if err != nil {
			return err
		}
		events = append(events, config)
	}
	if len(events) == 0 {
		return nil
	}
	_, err := runtime.Session().AppendBatch(ctx, operationID, events)
	return err
}

func seedRuntimeConfig(ctx context.Context, runtime *session.Runtime, operationID, modelRef, modelIdentity string) error {
	if runtime == nil || strings.TrimSpace(modelRef) == "" {
		return nil
	}
	event, err := sessionConfigEvent(modelRef, modelIdentity)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(event.Payload)
	_, err = runtime.Session().AppendBatch(ctx, fmt.Sprintf("%s:%x", operationID, digest[:16]), []session.Event{event})
	return err
}

func sessionConfigEvent(modelRef, modelIdentity string) (session.Event, error) {
	payload, err := json.Marshal(map[string]string{"modelRef": modelRef, "modelIdentity": modelIdentity})
	if err != nil {
		return session.Event{}, err
	}
	return session.Event{Kind: "session/config", Payload: payload}, nil
}

func (c *Controller) publishSessionRuntime(candidate *session.Runtime, prepared *agent.Session, rotateSessionTemp bool) (*session.Runtime, error) {
	return c.publishSessionRuntimeWithCommit(context.Background(), candidate, prepared, rotateSessionTemp, nil)
}

func (c *Controller) publishSessionRuntimeWithCommit(ctx context.Context, candidate *session.Runtime, prepared *agent.Session, rotateSessionTemp bool, commit func(context.Context, session.SessionRef) error, creationService ...*session.Service) (*session.Runtime, error) {
	if candidate == nil || prepared == nil {
		return nil, errors.New("v3 runtime publication candidate is unavailable")
	}
	service, _, _ := c.v3Binding()
	if len(creationService) != 0 {
		service = creationService[0]
	}
	if service == nil {
		return nil, errors.New("v3 session service is unavailable")
	}
	if current, ok := service.Runtime(candidate.Ref()); !ok || current != candidate {
		return nil, errors.New("v3 runtime candidate is not the exact published service instance")
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	snapshot := candidate.Session().ExecutionSnapshot()
	projection := snapshot.Projection
	if err := validateSessionDomainProjection(projection); err != nil {
		return nil, err
	}
	preset, err := c.presetForSessionPublication(ctx, candidate, snapshot)
	if err != nil {
		return nil, err
	}
	binding, err := service.Bind(candidate)
	if err != nil {
		return nil, fmt.Errorf("bind v3 runtime: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = binding.Release(context.Background())
		}
	}()
	// Commit desktop membership before publication; failures preserve the old binding.
	if commit != nil {
		if err := commit(ctx, candidate.Ref()); err != nil {
			return nil, err
		}
	}
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	// Domain parsing was validated above. Restore it before swapping the
	// client binding so a future validation failure cannot expose a partially
	// published controller or require closing a shared runtime.
	if err := c.restoreSessionDomainProjection(projection); err != nil {
		return nil, err
	}
	c.mu.Lock()
	oldGen := c.turns.generation
	c.mu.Unlock()
	// Serve restores the selected remote session's choice. Other frontends keep
	// their current permission posture when changing canonical identities.
	c.promptResolveMu.Lock()
	c.permissionStateMu.Lock()
	c.v3BindingMu.Lock()
	old := c.sessionRuntime
	oldBinding := c.sessionBinding
	c.sessionRuntime = candidate
	c.sessionService = service
	c.sessionBinding = binding
	c.exclusiveSession = true
	c.nativeLegacySession = false
	c.v3BindingMu.Unlock()
	if c.servePresetRestore {
		c.approval.setMode(preset)
		if c.subagentGate != nil {
			c.subagentGate.Update(preset)
		}
		c.refreshInteractiveGate()
	}
	c.permissionRevision.Add(1)
	c.permissionStateMu.Unlock()
	c.promptResolveMu.Unlock()
	c.bindAttachmentService()
	c.bindExecutionControl()
	if old != nil && old != candidate {
		old.UnbindExecution(oldGen)
	}
	c.mu.Lock()
	// Legacy paths are import inputs only. Retaining one as the live path lets
	// unrelated compatibility helpers recreate sidecars beside a read-only
	// source. The immutable SessionRef is the sole execution identity.
	c.sessionPath = ""
	c.mu.Unlock()
	c.executor.SetSession(prepared)
	// The immutable session projection is the only Goal restore source. A true
	// session switch installs a fresh, disarmed lifecycle; OpenSession's exact-runtime
	// fast path returns before this point and therefore preserves live activation.
	c.installGoalLifecycle(candidate)
	// Transcript pages are a derived cache. A session switch invalidates the
	// prior identity immediately; the next query rebuilds from the exact v3
	// projection without reading or writing a legacy sidecar.
	c.turnEvents.mu.Lock()
	if c.turnEvents.projection != nil {
		c.turnEvents.projection.CloseFollowers()
	}
	c.turnEvents.projection = nil
	c.turnEvents.projectionErr = nil
	c.turnEvents.mu.Unlock()
	c.rebindCheckpoints("")
	c.ResetPlannerSession()
	// The inbox belongs to the live runtime generation, not to the imported
	// legacy path. Close the pre-bind queue before rotating the session temp so
	// later Agent rebuilds attach to the same current generation.
	c.pauseInboxOnRotate()
	if rotateSessionTemp {
		c.rotateSessionTemp()
	}
	c.rebindInbox()
	c.refreshRuntimeState(event.Event{})
	published = true
	if oldBinding != nil && oldBinding != binding {
		if err := oldBinding.Release(context.Background()); err != nil {
			slog.Warn("controller: retire previous v3 binding after publication", "err", err)
		}
	}
	return old, nil
}

func explicitSessionPermissionPreset(source *session.Session, projection session.Projection) (string, uint64) {
	sequence := projection.PermissionPresetSequence
	manifest := source.Manifest()
	if sequence == 0 || (manifest.Source != nil && sequence <= manifest.InheritedEvents) {
		// A fork does not inherit a broad permission grant. It may start at
		// the narrower inherited read-only posture until the child makes an
		// explicit choice of its own.
		if manifest.Source != nil && projection.PermissionPreset == ToolApprovalReadOnly {
			return ToolApprovalReadOnly, 0
		}
		return "", 0
	}
	return projection.PermissionPreset, sequence
}

func (c *Controller) presetForSessionPublication(ctx context.Context, candidate *session.Runtime, snapshot session.Snapshot) (string, error) {
	if !c.servePresetRestore {
		return "", nil
	}
	preset, sequence := explicitSessionPermissionPreset(candidate.Session(), snapshot.Projection)
	if sequence > snapshot.DurableSequence {
		// Failed writes leave accepted events in memory; never publish an unflushed preset.
		if _, err := candidate.Session().FlushThrough(ctx, sequence); err != nil {
			return "", fmt.Errorf("persist session permission preset: %w", err)
		}
	}
	return string(permissionpreset.NormalizeDefault(preset)), nil
}

// EnableServeSessionPermissionPresets opts a Serve-owned controller into
// restoring per-session choices. A same-session rebuild keeps its already
// migrated live mode by passing restoreCurrent=false.
func (c *Controller) EnableServeSessionPermissionPresets(restoreCurrent bool) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	c.servePresetRestore = true
	if !restoreCurrent {
		return
	}
	_, runtime, bound := c.v3Binding()
	if !bound || runtime == nil {
		return
	}
	snapshot := runtime.Session().StateSnapshot()
	preset, sequence := explicitSessionPermissionPreset(runtime.Session(), snapshot.Projection)
	if sequence > snapshot.DurableSequence {
		preset = ToolApprovalReadOnly
	}
	c.applyToolApprovalModeLocked(string(permissionpreset.NormalizeDefault(preset)))
}

func validateSessionDomainProjection(projection session.Projection) error {
	if len(projection.PlanState) > 0 {
		var plan struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(projection.PlanState, &plan); err != nil {
			return fmt.Errorf("restore v3 plan state: %w", err)
		}
	}
	if len(projection.GoalState) > 0 {
		var goal goalState
		if err := json.Unmarshal(projection.GoalState, &goal); err != nil {
			return fmt.Errorf("restore v3 goal state: %w", err)
		}
	}
	return nil
}

func (c *Controller) restoreSessionDomainProjection(projection session.Projection) error {
	var plan struct {
		Enabled bool `json:"enabled"`
	}
	if len(projection.PlanState) > 0 {
		if err := json.Unmarshal(projection.PlanState, &plan); err != nil {
			return fmt.Errorf("restore v3 plan state: %w", err)
		}
	}
	c.mu.Lock()
	c.sessionSettings.planMode = plan.Enabled
	c.mu.Unlock()
	if setter, ok := c.runner.(interface{ SetPlanMode(bool) }); ok {
		setter.SetPlanMode(plan.Enabled)
	} else if c.executor != nil {
		c.executor.SetPlanMode(plan.Enabled)
	}
	if err := c.goals.restoreGoalEvent(projection.GoalState); err != nil {
		return fmt.Errorf("restore v3 goal state: %w", err)
	}
	if c.executor != nil {
		c.executor.RestoreDeliveryCheckpoint(c.goals.deliveryState())
	}
	return nil
}

func (c *Controller) v3Binding() (*session.Service, *session.Runtime, bool) {
	if c == nil {
		return nil, nil, false
	}
	c.v3BindingMu.RLock()
	service, runtime, exclusive := c.sessionService, c.sessionRuntime, c.exclusiveSession
	c.v3BindingMu.RUnlock()
	return service, runtime, exclusive
}

// SessionBinding exposes the host-owned service/runtime pair for an Agent
// rebuild. Callers must attach the pair to the replacement Controller; they
// must not close or republish the writer themselves.
func (c *Controller) SessionBinding() (*session.Service, *session.Runtime, bool) {
	service, runtime, exclusive := c.v3Binding()
	return service, runtime, exclusive && service != nil && runtime != nil
}

// SessionService exposes the host query/management owner without requiring
// an active runtime. Cold history listing must not create an Agent or writer.
func (c *Controller) SessionService() *session.Service {
	service, _, _ := c.v3Binding()
	return service
}

// SessionCreationService keeps newly created sessions in the current store
// even while this controller is attached to a historical directory.
func (c *Controller) SessionCreationService() *session.Service {
	if c == nil {
		return nil
	}
	c.v3BindingMu.RLock()
	defer c.v3BindingMu.RUnlock()
	if c.sessionCreateService != nil {
		return c.sessionCreateService
	}
	return c.sessionService
}

// UsesExclusiveSession reports the configured execution contract even when
// a lazy fresh session has not yet been allocated. Hosts use it to avoid
// manufacturing a legacy path during rebuild preparation.
func (c *Controller) UsesExclusiveSession() bool {
	service, _, exclusive := c.v3Binding()
	return exclusive && service != nil
}

func (c *Controller) sessionEngineEnabled() bool {
	_, _, exclusive := c.v3Binding()
	return exclusive
}

type SessionRotationRequest struct {
	Source     session.SessionRef
	SourcePath string
	Reason     string
}

type SessionRotationPlan struct {
	CreateOptions session.CreateOptions
	Commit        func(context.Context, session.SessionRef) error
}

// rotateExclusiveSession implements /new and /clear without allocating a
// legacy transcript path. clear additionally deletes the closed source v3
// directory; new leaves it available in history.
func (c *Controller) rotateExclusiveSession(clear bool) error {
	service, runtime, _ := c.v3Binding()
	if service == nil {
		return errors.New("exclusive v3 session runtime is unavailable")
	}
	reason := "new"
	if clear {
		reason = "clear"
	}
	if runtime == nil {
		// A handoff released the identity without a replacement: with no source
		// to flush, end or plan from, allocation is the whole rotation — the
		// step the next turn would otherwise take lazily.
		ref, err := c.bindFreshSessionWithCommit(context.Background(), session.CreateOptions{}, nil)
		if err != nil {
			return err
		}
		c.startExclusiveSession(ref, reason)
		return nil
	}
	oldRef := runtime.Ref()
	if err := c.Snapshot(); err != nil {
		return err
	}
	if err := c.extensionSessionPhase(context.Background(), extension.PointSessionRotate, dispatch.PhaseRotate, oldRef.SessionID); err != nil {
		return err
	}
	c.hooks.SessionEnd(context.Background(), reason)
	c.extensionSessionEvent(extension.PointSessionEnd, dispatch.PhaseEnd, oldRef.SessionID)
	createOptions := session.CreateOptions{}
	var commitRotation func(context.Context, session.SessionRef) error
	if c.onSessionRotation != nil {
		plan, planErr := c.onSessionRotation(context.Background(), SessionRotationRequest{Source: oldRef, Reason: reason})
		if planErr != nil {
			return planErr
		}
		createOptions, commitRotation = plan.CreateOptions, plan.Commit
	}
	ref, err := c.bindFreshSessionWithCommit(context.Background(), createOptions, commitRotation)
	if err != nil {
		return err
	}
	if commitRotation == nil && clear {
		if err := service.Delete(context.Background(), oldRef); err != nil {
			return fmt.Errorf("new session %s is active; delete cleared session: %w", ref.SessionID, err)
		}
	}
	c.startExclusiveSession(ref, reason)
	return nil
}

// startExclusiveSession runs the session-start side of a rotation once the
// fresh identity is published.
func (c *Controller) startExclusiveSession(ref session.SessionRef, reason string) {
	c.ClearGoal()
	c.mu.Lock()
	c.startedOnce = true
	c.mu.Unlock()
	c.hooks.SetSessionID(ref.SessionID)
	c.enqueueHookContexts(c.hooks.SessionStart(context.Background(), reason))
	c.extensionSessionEvent(extension.PointSessionStart, dispatch.PhaseStart, ref.SessionID)
	c.clearSessionWriteAccess()
}
