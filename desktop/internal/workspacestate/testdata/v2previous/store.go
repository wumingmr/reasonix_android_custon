package workspacestate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"reasonix/internal/filelock"
	"reasonix/internal/fileutil"
)

const (
	SchemaVersion     = 2
	GlobalWorkspaceID = "global"
)

var (
	ErrUnsupportedVersion = errors.New("workspace state version is unsupported")
	ErrWorkspaceNotFound  = errors.New("workspace is not registered")
	ErrSessionNotFound    = errors.New("session is not registered")
	ErrMutationConflict   = errors.New("workspace mutation conflicts with persisted state")
)

type Workspace struct {
	ID         string    `json:"id"`
	Root       string    `json:"root"`
	Title      string    `json:"title"`
	SessionIDs []string  `json:"sessionIds"`
	Visible    bool      `json:"visible"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	extra      map[string]json.RawMessage
}

type PendingCreate struct {
	OperationID   string    `json:"operationId"`
	WorkspaceID   string    `json:"workspaceId"`
	SessionID     string    `json:"sessionId"`
	CreatedAt     time.Time `json:"createdAt"`
	ArchiveSource string    `json:"archiveSource,omitempty"`
	extra         map[string]json.RawMessage
}

type State struct {
	Version            int                      `json:"version"`
	Generation         uint64                   `json:"generation"`
	Initialized        bool                     `json:"initialized"`
	WorkspaceIDs       []string                 `json:"workspaceIds"`
	Workspaces         map[string]Workspace     `json:"workspaces"`
	ArchivedSessionIDs []string                 `json:"archivedSessionIds"`
	PendingCreates     map[string]PendingCreate `json:"pendingCreates"`
	SessionStates      map[string]SessionState  `json:"sessionStates"`
	SourceMappings     map[string]SourceMapping `json:"sourceMappings"`
	PendingOperations  map[string]Operation     `json:"pendingOperations"`
	RecoveryEntries    map[string]RecoveryEntry `json:"recoveryEntries"`
	Presentation       map[string]Presentation  `json:"presentation"`
	extra              map[string]json.RawMessage
}

type Store struct {
	path          string
	mu            sync.Mutex
	beforeUpgrade func(context.Context) error
}

func NewStore(path string, beforeUpgrade ...func(context.Context) error) *Store {
	store := &Store{path: filepath.Clean(path)}
	if len(beforeUpgrade) > 0 {
		store.beforeUpgrade = beforeUpgrade[0]
	}
	return store
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) Load(ctx context.Context) (State, error) {
	if s == nil || strings.TrimSpace(s.path) == "" || s.path == "." {
		return State{}, errors.New("workspace state path is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	state, err := load(s.path)
	if err != nil {
		return State{}, err
	}
	return cloneState(state)
}

func (s *Store) EnsureWorkspace(ctx context.Context, workspace Workspace) error {
	workspace.ID = strings.TrimSpace(workspace.ID)
	if workspace.ID == "" {
		return errors.New("workspace id is required")
	}
	return s.mutate(ctx, func(state *State) error {
		now := time.Now().UTC()
		current, exists := state.Workspaces[workspace.ID]
		if exists {
			if current.Root != workspace.Root {
				return ErrMutationConflict
			}
			return nil
		}
		workspace.SessionIDs = []string{}
		workspace.CreatedAt, workspace.UpdatedAt = now, now
		state.Workspaces[workspace.ID] = workspace
		state.WorkspaceIDs = append(state.WorkspaceIDs, workspace.ID)
		return nil
	})
}

func (s *Store) RenameWorkspace(ctx context.Context, workspaceID, title string) error {
	return s.mutate(ctx, func(state *State) error {
		workspace, ok := state.Workspaces[strings.TrimSpace(workspaceID)]
		if !ok {
			return ErrWorkspaceNotFound
		}
		workspace.Title = strings.TrimSpace(title)
		workspace.UpdatedAt = time.Now().UTC()
		state.Workspaces[workspace.ID] = workspace
		return nil
	})
}

func (s *Store) SetWorkspaceVisible(ctx context.Context, workspaceID string, visible bool) error {
	return s.mutate(ctx, func(state *State) error {
		workspace, ok := state.Workspaces[strings.TrimSpace(workspaceID)]
		if !ok {
			return ErrWorkspaceNotFound
		}
		workspace.Visible = visible
		workspace.UpdatedAt = time.Now().UTC()
		state.Workspaces[workspace.ID] = workspace
		return nil
	})
}

func (s *Store) MoveWorkspace(ctx context.Context, workspaceID, beforeWorkspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	beforeWorkspaceID = strings.TrimSpace(beforeWorkspaceID)
	return s.mutate(ctx, func(state *State) error {
		if _, ok := state.Workspaces[workspaceID]; !ok {
			return ErrWorkspaceNotFound
		}
		if beforeWorkspaceID != "" {
			if _, ok := state.Workspaces[beforeWorkspaceID]; !ok {
				return ErrWorkspaceNotFound
			}
		}
		state.WorkspaceIDs = insertBefore(remove(state.WorkspaceIDs, workspaceID), workspaceID, beforeWorkspaceID)
		return nil
	})
}

func (s *Store) BeginCreate(ctx context.Context, pending PendingCreate) error {
	pending.OperationID = strings.TrimSpace(pending.OperationID)
	pending.WorkspaceID = strings.TrimSpace(pending.WorkspaceID)
	pending.SessionID = strings.TrimSpace(pending.SessionID)
	if pending.OperationID == "" || pending.WorkspaceID == "" || pending.SessionID == "" {
		return errors.New("pending create requires operation, workspace, and session ids")
	}
	return s.mutate(ctx, func(state *State) error {
		if _, ok := state.Workspaces[pending.WorkspaceID]; !ok {
			return ErrWorkspaceNotFound
		}
		if state.SessionStates[pending.SessionID].Lifecycle == Deleted {
			return ErrMutationConflict
		}
		if current, ok := state.PendingCreates[pending.SessionID]; ok {
			if current.OperationID == pending.OperationID && current.WorkspaceID == pending.WorkspaceID {
				return nil
			}
			return ErrMutationConflict
		}
		if owner, ok := sessionOwner(*state, pending.SessionID); ok && owner != pending.WorkspaceID {
			return ErrMutationConflict
		}
		if pending.CreatedAt.IsZero() {
			pending.CreatedAt = time.Now().UTC()
		}
		state.PendingCreates[pending.SessionID] = pending
		return nil
	})
}

func (s *Store) AttachSession(ctx context.Context, operationID, workspaceID, sessionID, beforeSessionID string) error {
	return s.attachSession(ctx, operationID, workspaceID, sessionID, beforeSessionID, "", nil)
}

// AttachSessionFromSourceIfUnchanged publishes a derived child only while its
// resolved source is still active, in the same workspace, and at the same
// lifecycle generation.
func (s *Store) AttachSessionFromSourceIfUnchanged(
	ctx context.Context,
	operationID, workspaceID, sessionID, beforeSessionID, sourceSessionID string,
	sourceGeneration uint64,
) error {
	return s.attachSession(ctx, operationID, workspaceID, sessionID, beforeSessionID, sourceSessionID, &sourceGeneration)
}

func (s *Store) attachSession(
	ctx context.Context,
	operationID, workspaceID, sessionID, beforeSessionID, sourceSessionID string,
	sourceGeneration *uint64,
) error {
	operationID, workspaceID, sessionID = strings.TrimSpace(operationID), strings.TrimSpace(workspaceID), strings.TrimSpace(sessionID)
	if workspaceID == "" || sessionID == "" {
		return errors.New("attach requires workspace and session ids")
	}
	return s.mutate(ctx, func(state *State) error {
		if sourceGeneration != nil {
			sourceSessionID = strings.TrimSpace(sourceSessionID)
			sourceState := state.SessionStates[sourceSessionID]
			sourceOwner, owned := sessionOwner(*state, sourceSessionID)
			if sourceSessionID == "" || !owned || sourceOwner != workspaceID ||
				sourceState.Lifecycle != Active || sourceState.Generation != *sourceGeneration {
				return ErrMutationConflict
			}
		}
		if state.SessionStates[sessionID].Lifecycle == Deleted {
			return ErrMutationConflict
		}
		workspace, ok := state.Workspaces[workspaceID]
		if !ok {
			return ErrWorkspaceNotFound
		}
		if owner, owned := sessionOwner(*state, sessionID); owned {
			if owner != workspaceID {
				return ErrMutationConflict
			}
			delete(state.PendingCreates, sessionID)
			return nil
		}
		if operationID != "" {
			pending, ok := state.PendingCreates[sessionID]
			if !ok || pending.OperationID != operationID || pending.WorkspaceID != workspaceID {
				return ErrMutationConflict
			}
		}
		workspace.SessionIDs = insertBefore(workspace.SessionIDs, sessionID, beforeSessionID)
		workspace.UpdatedAt = time.Now().UTC()
		state.Workspaces[workspaceID] = workspace
		delete(state.PendingCreates, sessionID)
		return nil
	})
}

// CommitRotation atomically publishes a prepared replacement into its
// workspace and, for Clear, archives the source without removing its stable
// position from the registry.
func (s *Store) CommitRotation(ctx context.Context, operationID, workspaceID, sessionID, beforeSessionID, archiveSessionID string) error {
	operationID, workspaceID, sessionID = strings.TrimSpace(operationID), strings.TrimSpace(workspaceID), strings.TrimSpace(sessionID)
	archiveSessionID = strings.TrimSpace(archiveSessionID)
	if operationID == "" || workspaceID == "" || sessionID == "" {
		return errors.New("rotation commit requires operation, workspace, and session ids")
	}
	return s.mutate(ctx, func(state *State) error {
		workspace, ok := state.Workspaces[workspaceID]
		if !ok {
			return ErrWorkspaceNotFound
		}
		pending, ok := state.PendingCreates[sessionID]
		if !ok || pending.OperationID != operationID || pending.WorkspaceID != workspaceID {
			if owner, attached := sessionOwner(*state, sessionID); !attached || owner != workspaceID {
				return ErrMutationConflict
			}
		} else if owner, attached := sessionOwner(*state, sessionID); attached && owner != workspaceID {
			return ErrMutationConflict
		} else if !attached {
			workspace.SessionIDs = insertBefore(workspace.SessionIDs, sessionID, beforeSessionID)
			workspace.UpdatedAt = time.Now().UTC()
			state.Workspaces[workspaceID] = workspace
		}
		delete(state.PendingCreates, sessionID)
		if archiveSessionID != "" {
			if _, exists := sessionOwner(*state, archiveSessionID); !exists {
				return ErrSessionNotFound
			}
			setLifecycle(state, archiveSessionID, Archived)
		}
		return nil
	})
}

func (s *Store) AbortCreate(ctx context.Context, sessionID string) error {
	return s.mutate(ctx, func(state *State) error {
		delete(state.PendingCreates, strings.TrimSpace(sessionID))
		return nil
	})
}

func (s *Store) MoveSession(ctx context.Context, workspaceID, sessionID, beforeSessionID string) error {
	return s.moveSession(ctx, workspaceID, sessionID, beforeSessionID, nil)
}

// MoveSessionIfUnchanged reorders one active session only while the caller's
// resolved lifecycle generation and workspace owner remain current.
func (s *Store) MoveSessionIfUnchanged(ctx context.Context, workspaceID, sessionID, beforeSessionID string, generation uint64) error {
	return s.moveSession(ctx, workspaceID, sessionID, beforeSessionID, &generation)
}

func (s *Store) moveSession(ctx context.Context, workspaceID, sessionID, beforeSessionID string, generation *uint64) error {
	return s.mutate(ctx, func(state *State) error {
		workspace, ok := state.Workspaces[strings.TrimSpace(workspaceID)]
		if !ok {
			return ErrWorkspaceNotFound
		}
		if !contains(workspace.SessionIDs, sessionID) {
			return ErrSessionNotFound
		}
		status := state.SessionStates[sessionID]
		if status.Lifecycle != Active {
			return ErrSessionNotFound
		}
		if generation != nil && status.Generation != *generation {
			return ErrMutationConflict
		}
		workspace.SessionIDs = insertBefore(remove(workspace.SessionIDs, sessionID), sessionID, beforeSessionID)
		workspace.UpdatedAt = time.Now().UTC()
		state.Workspaces[workspace.ID] = workspace
		status.Generation++
		state.SessionStates[sessionID] = status
		return nil
	})
}

func (s *Store) ArchiveSession(ctx context.Context, sessionID string) error {
	return s.SetLifecycle(ctx, []string{sessionID}, Archived)
}

func (s *Store) RestoreSession(ctx context.Context, sessionID string) error {
	return s.SetLifecycle(ctx, []string{sessionID}, Active)
}

func (s *Store) Contains(ctx context.Context, sessionID string) (bool, error) {
	state, err := s.Load(ctx)
	if err != nil {
		return false, err
	}
	_, ok := sessionOwner(state, strings.TrimSpace(sessionID))
	return ok, nil
}

// WithSessionUnchanged serializes a durable metadata commit with lifecycle and
// workspace changes, including writers in other processes. The callback must
// not call the registry; it may only commit session content metadata.
func (s *Store) WithSessionUnchanged(ctx context.Context, id, workspaceID string, generation uint64, commit func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := filelock.Acquire(ctx, s.path+".lock")
	if err != nil {
		return err
	}
	defer release()
	state, err := load(s.path)
	if err != nil {
		return err
	}
	owner, ok := sessionOwner(state, id)
	status := state.SessionStates[id]
	if !ok || status.Lifecycle != Active {
		return ErrSessionNotFound
	}
	if owner != workspaceID || status.Generation != generation {
		return ErrMutationConflict
	}
	return commit()
}

func (s *Store) mutate(ctx context.Context, change func(*State) error) error {
	if s == nil || strings.TrimSpace(s.path) == "" || s.path == "." {
		return errors.New("workspace state path is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	release, err := filelock.Acquire(ctx, s.path+".lock")
	if err != nil {
		return err
	}
	defer release()
	upgrading := false
	if body, err := os.ReadFile(s.path); err == nil {
		var header struct {
			Version int `json:"version"`
		}
		upgrading = json.Unmarshal(body, &header) == nil && header.Version == 1
	}
	if s.beforeUpgrade != nil {
		body, readErr := os.ReadFile(s.path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		var header struct {
			Version int `json:"version"`
		}
		if len(body) > 0 && json.Unmarshal(body, &header) == nil && header.Version == 1 {
			if err := s.beforeUpgrade(ctx); err != nil {
				return err
			}
		}
	}
	if err := backupV1(s.path); err != nil {
		return err
	}
	state, err := load(s.path)
	if err != nil {
		return err
	}
	before, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := change(&state); err != nil {
		return err
	}
	after, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if bytes.Equal(before, after) && !upgrading {
		return nil
	}
	state.Generation++
	state.Initialized = true
	normalize(&state)
	if err := validate(state); err != nil {
		return err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFileStrict(s.path, append(body, '\n'), 0o600)
}

func load(path string) (State, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(body, &state); err != nil {
		return State{}, fmt.Errorf("decode workspace state: %w", err)
	}
	if state.Version != 1 && state.Version != SchemaVersion {
		return State{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, state.Version)
	}
	if state.Version == 1 {
		state.SessionStates = map[string]SessionState{}
		for _, id := range state.ArchivedSessionIDs {
			state.SessionStates[id] = SessionState{Lifecycle: Archived, Generation: state.Generation}
		}
		state.Version = SchemaVersion
	} else {
		if state.WorkspaceIDs == nil || state.Workspaces == nil || state.SessionStates == nil {
			return State{}, fmt.Errorf("%w: missing required registry fields", ErrUnsupportedVersion)
		}
		for _, workspace := range state.Workspaces {
			for _, id := range workspace.SessionIDs {
				if _, ok := state.SessionStates[id]; !ok {
					return State{}, fmt.Errorf("%w: missing session lifecycle", ErrUnsupportedVersion)
				}
			}
		}
	}
	normalize(&state)
	if err := validate(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func newState() State {
	state := State{Version: SchemaVersion, WorkspaceIDs: []string{}, Workspaces: map[string]Workspace{}, ArchivedSessionIDs: []string{}, PendingCreates: map[string]PendingCreate{}}
	normalize(&state)
	return state
}

func normalize(state *State) {
	if state.SessionStates == nil {
		state.SessionStates = map[string]SessionState{}
	}
	if state.SourceMappings == nil {
		state.SourceMappings = map[string]SourceMapping{}
	}
	if state.PendingOperations == nil {
		state.PendingOperations = map[string]Operation{}
	}
	if state.RecoveryEntries == nil {
		state.RecoveryEntries = map[string]RecoveryEntry{}
	}
	if state.Presentation == nil {
		state.Presentation = map[string]Presentation{}
	}
	if state.WorkspaceIDs == nil {
		state.WorkspaceIDs = []string{}
	}
	if state.Workspaces == nil {
		state.Workspaces = map[string]Workspace{}
	}
	if state.ArchivedSessionIDs == nil {
		state.ArchivedSessionIDs = []string{}
	}
	if state.PendingCreates == nil {
		state.PendingCreates = map[string]PendingCreate{}
	}
	for id, workspace := range state.Workspaces {
		if workspace.SessionIDs == nil {
			workspace.SessionIDs = []string{}
		}
		state.Workspaces[id] = workspace
		for _, sessionID := range workspace.SessionIDs {
			if _, ok := state.SessionStates[sessionID]; !ok {
				state.SessionStates[sessionID] = SessionState{Lifecycle: Active, Generation: state.Generation}
			}
		}
	}
	state.ArchivedSessionIDs = []string{}
	for id, status := range state.SessionStates {
		if status.Lifecycle == Archived {
			state.ArchivedSessionIDs = append(state.ArchivedSessionIDs, id)
		}
	}
	slices.Sort(state.ArchivedSessionIDs)
}

func validate(state State) error {
	if err := validateLifecycleState(state); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, id := range state.WorkspaceIDs {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("workspace state has duplicate workspace %q", id)
		}
		seen[id] = struct{}{}
		workspace, ok := state.Workspaces[id]
		if !ok || workspace.ID != id {
			return fmt.Errorf("workspace state has invalid workspace %q", id)
		}
	}
	owners := map[string]string{}
	for id, workspace := range state.Workspaces {
		for _, sessionID := range workspace.SessionIDs {
			if owner, duplicate := owners[sessionID]; duplicate {
				return fmt.Errorf("session %q belongs to both %q and %q", sessionID, owner, id)
			}
			owners[sessionID] = id
		}
	}
	return nil
}

func sessionOwner(state State, sessionID string) (string, bool) {
	for id, workspace := range state.Workspaces {
		if contains(workspace.SessionIDs, sessionID) {
			return id, true
		}
	}
	return "", false
}

func insertBefore(ids []string, id, before string) []string {
	ids = remove(ids, id)
	if before != "" {
		for i, current := range ids {
			if current == before {
				return append(append(append([]string{}, ids[:i]...), id), ids[i:]...)
			}
		}
	}
	return append(ids, id)
}

func remove(ids []string, target string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != target {
			result = append(result, id)
		}
	}
	return result
}

func contains(ids []string, target string) bool {
	return slices.Contains(ids, target)
}

func cloneState(state State) (State, error) {
	body, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	var clone State
	if err := json.Unmarshal(body, &clone); err != nil {
		return State{}, err
	}
	normalize(&clone)
	return clone, nil
}

func (s *State) UnmarshalJSON(body []byte) error {
	type plain State
	var decoded plain
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	for _, key := range []string{"version", "generation", "initialized", "workspaceIds", "workspaces", "archivedSessionIds", "pendingCreates", "sessionStates", "sourceMappings", "pendingOperations", "recoveryEntries", "presentation"} {
		delete(fields, key)
	}
	*s = State(decoded)
	s.extra = fields
	return nil
}

func (s State) MarshalJSON() ([]byte, error) {
	type plain State
	body, err := json.Marshal(plain(s))
	if err != nil {
		return nil, err
	}
	return mergeUnknown(body, s.extra)
}

func (w *Workspace) UnmarshalJSON(body []byte) error {
	type plain Workspace
	var decoded plain
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	for _, key := range []string{"id", "root", "title", "sessionIds", "visible", "createdAt", "updatedAt"} {
		delete(fields, key)
	}
	*w = Workspace(decoded)
	w.extra = fields
	return nil
}

func (w Workspace) MarshalJSON() ([]byte, error) {
	type plain Workspace
	body, err := json.Marshal(plain(w))
	if err != nil {
		return nil, err
	}
	return mergeUnknown(body, w.extra)
}

func mergeUnknown(known []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return known, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(known, &fields); err != nil {
		return nil, err
	}
	for key, value := range extra {
		if _, exists := fields[key]; !exists {
			fields[key] = bytes.Clone(value)
		}
	}
	return json.Marshal(fields)
}
