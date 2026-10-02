package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/session"
)

// Fork branches the conversation at the start of turn into a NEW session file,
// preserving the current one as the branch point, and switches to the branch. Code
// is untouched (it's a conversation operation). Like a conversation rewind it needs
// the live boundary, so it is unavailable for resumed-session turns and refused
// while a turn runs. Returns the new session path.
func (c *Controller) Fork(turn int) (string, error) {
	return c.ForkNamed(turn, "")
}

func (c *Controller) ForkNamed(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, true)
}

// ForkSession copies the conversation at the start of turn into a new session
// file without switching this controller to it. Desktop uses this to open the
// branch in a new tab while the source tab keeps its current transcript.
func (c *Controller) ForkSession(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, false)
}

func (c *Controller) forkNamed(turn int, name string, switchToFork bool) (string, error) {
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return "", c.rewindFail(fmt.Errorf("cannot fork while a turn is running"))
		}
		return "", c.rewindFail(err)
	}
	defer c.endRotation()
	return c.forkNamedReady(turn, name, switchToFork, agent.HeadKindFork)
}

// forkNamedReady forks at a completed turn boundary into an independent child
// session. The parent log remains immutable from the child's point of view;
// switchToFork controls only whether this controller adopts the child.
func (c *Controller) forkNamedReady(turn int, name string, switchToFork bool, kind string) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	if c.sessionEngineEnabled() {
		return c.forkNamedSession(turn, name, switchToFork)
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("fork needs session persistence, which is disabled"))
	}
	boundary, hasBound := c.checkpoints.boundary(turn)
	if !hasBound {
		return "", c.rewindFail(fmt.Errorf("fork unavailable for turn %d (resumed session)", turn))
	}
	// Persist the current conversation first so the branch point survives, then
	// seed a fresh session with the messages up to the fork and switch to it.
	if err := c.Snapshot(); err != nil {
		slog.Warn("controller: pre-fork snapshot", "err", err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	if boundary > len(src) {
		boundary = len(src)
	}
	forked := append([]provider.Message(nil), src[:boundary]...)
	sess := agent.NewSession("")
	sess.Messages = forked

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.SaveIfAbsent(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if err := c.publishSessionChild(newPath, forked); err != nil {
		_ = os.Remove(newPath)
		return "", c.rewindFail(fmt.Errorf("publish v3 fork: %w", err))
	}
	if _, err := sess.CopyValidContextProjection(parentPath, newPath); err != nil {
		slog.Warn("controller: fork did not inherit context projection", "err", err)
	}
	forkPreview, forkTurns := agent.SessionPreviewFromMessages(forked)
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         turn,
		ForkMessageIndex: boundary,
		Preview:          forkPreview,
		Turns:            forkTurns,
		SchemaVersion:    agent.BranchMetaCountsVersion,
		Model:            c.selection.ref,
		ModelIdentity:    c.selection.identity,
	}); err != nil {
		return "", c.rewindFail(err)
	}
	if switchToFork {
		commitTransition, err := c.prepareSessionTransition(newPath, "fork", sess)
		if err != nil {
			return "", c.rewindFail(fmt.Errorf("bind fork session: %w", err))
		}
		// See snapshotMu: the swap must not interleave with an in-flight save.
		c.snapshotMu.Lock()
		commitTransition.publish()
		// Load the child sidecar when the covered prefix survived the fork. The
		// loader rebinds its lineage key without touching the parent's sidecar.
		c.bindExecutorProjection(newPath, true)
		c.ResetPlannerSession()
		c.rebindCheckpoints(newPath)
		// A historical fork rewinds before later failures, so it starts with no
		// active recovery event even though it inherits the session preference.
		c.loadRecoveryState(newPath)
		if c.guardianSess != nil {
			c.guardianSess.Reset()
		}
		// Switching into the fork is a new logical session for temporary files.
		c.rotateSessionTemp()
		c.snapshotMu.Unlock()
	}
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("forked conversation at turn %d into a new session", turn)})
	return newPath, nil
}

func (c *Controller) CheckpointHasBoundary(turn int) bool {
	boundary, ok := c.checkpoints.boundary(turn)
	if !ok {
		return false
	}
	// After compaction or a head switch the key may point past the current
	// message log; treat those turns as "no boundary" so the UI can disable
	// the button. Len is lock-guarded for the frontend goroutines calling this.
	return boundary <= c.executor.Session().Len()
}

// Branch copies the current conversation into a child branch and switches to it.
// Unlike Fork, it branches at the current tip and does not require a checkpoint.
func (c *Controller) Branch(name string) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("branch unavailable"))
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("branch needs session persistence, which is disabled"))
	}
	// Hold the rotation gate across the Snapshot and the switch below so a turn
	// cannot start mid-branch and then have its session replaced.
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return "", c.rewindFail(fmt.Errorf("cannot branch while a turn is running"))
		}
		return "", c.rewindFail(err)
	}
	defer c.endRotation()
	if c.sessionEngineEnabled() {
		_, runtime, _ := c.v3Binding()
		if runtime == nil {
			return "", c.rewindFail(session.ErrSessionNotRunning)
		}
		turns := runtime.Session().ExecutionSnapshot().Projection.Turns
		if len(turns) == 0 {
			return "", c.rewindFail(fmt.Errorf("nothing to branch yet"))
		}
		return c.forkNamedSession(len(turns), name, true)
	}
	if !c.executor.Session().HasContent() {
		return "", c.rewindFail(fmt.Errorf("nothing to branch yet"))
	}
	if err := c.Snapshot(); err != nil {
		return "", c.rewindFail(err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	branched := append([]provider.Message(nil), src...)
	sess := agent.NewSession("")
	sess.Messages = branched

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.SaveIfAbsent(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if err := c.publishSessionChild(newPath, branched); err != nil {
		_ = os.Remove(newPath)
		return "", c.rewindFail(fmt.Errorf("publish v3 branch: %w", err))
	}
	if _, err := sess.CopyValidContextProjection(parentPath, newPath); err != nil {
		slog.Warn("controller: branch did not inherit context projection", "err", err)
	}
	branchPreview, branchTurns := agent.SessionPreviewFromMessages(branched)
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         -1,
		ForkMessageIndex: len(branched),
		Preview:          branchPreview,
		Turns:            branchTurns,
		SchemaVersion:    agent.BranchMetaCountsVersion,
		Model:            c.selection.ref,
		ModelIdentity:    c.selection.identity,
	}); err != nil {
		return "", c.rewindFail(err)
	}
	commitTransition, err := c.prepareSessionTransition(newPath, "branch", sess)
	if err != nil {
		return "", c.rewindFail(fmt.Errorf("bind branch session: %w", err))
	}
	// See snapshotMu: the swap must not interleave with an in-flight save.
	c.snapshotMu.Lock()
	commitTransition.publish()
	c.bindExecutorProjection(newPath, true)
	c.ResetPlannerSession()
	c.rebindCheckpoints(newPath)
	if c.guardianSess != nil {
		c.guardianSess.Reset()
	}
	c.carryRecoveryState(newPath)
	c.rotateSessionTemp()
	c.snapshotMu.Unlock()
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("created branch %s", agent.BranchID(newPath))})
	return newPath, nil
}

// forkNamedSession creates a child from an exact persisted turn boundary. The
// compatibility integer is resolved only against the typed turn index; no
// message count, transcript snapshot, or sidecar participates.
func (c *Controller) forkNamedSession(turn int, name string, switchToFork bool) (string, error) {
	service, parent, _ := c.v3Binding()
	if service == nil || parent == nil {
		return "", session.ErrSessionNotRunning
	}
	projection := parent.Session().ExecutionSnapshot().Projection
	completed := make([]session.TurnBoundary, 0, len(projection.Turns))
	for _, boundary := range projection.Turns {
		if boundary.EndSequence != 0 {
			completed = append(completed, boundary)
		}
	}
	if turn < 1 || turn > len(completed) {
		return "", fmt.Errorf("fork unavailable for completed turn %d", turn)
	}
	child, err := service.Fork(context.Background(), parent.Ref(), completed[turn-1].TurnID, "")
	if err != nil {
		return "", err
	}
	closeChild := true
	defer func() {
		if closeChild {
			_ = service.Close(context.Background(), child.Ref())
		}
	}()
	if title := strings.TrimSpace(name); title != "" {
		payload, marshalErr := json.Marshal(map[string]string{"title": title})
		if marshalErr != nil {
			return "", marshalErr
		}
		if _, appendErr := child.Session().AppendBatch(context.Background(), "fork-title:"+child.Ref().SessionID, []session.Event{{Kind: "session/title", Payload: payload}}); appendErr != nil {
			return "", appendErr
		}
	}
	if _, err := child.Session().Flush(context.Background()); err != nil {
		return "", err
	}
	if !switchToFork {
		return child.Ref().SessionID, nil
	}
	prepared := agent.NewSession("").CloneWithMessages(child.Session().ExecutionSnapshot().Projection.ModelMessages)
	_, err = c.publishSessionRuntime(child, prepared, true)
	if err != nil {
		return "", err
	}
	closeChild = false
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("forked conversation at completed turn %d into session %s", turn, child.Ref().SessionID)})
	return child.Ref().SessionID, nil
}

// Branches lists saved conversation branches in this controller's session dir.
func (c *Controller) Branches() ([]agent.BranchInfo, error) {
	if c.sessionDir == "" {
		return nil, fmt.Errorf("session persistence is disabled")
	}
	if err := c.Snapshot(); err != nil {
		return nil, err
	}
	branches, err := agent.ListBranches(c.sessionDir)
	if err != nil {
		return nil, err
	}
	return c.withHeadBranches(branches), nil
}

func (c *Controller) SwitchBranch(ref string) (agent.BranchInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("usage: /switch <branch id|name>"))
	}
	// Hold the rotation gate across the branch listing/load and the switch so a
	// turn cannot start between the check and the SetSession below.
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("cannot switch branches while a turn is running"))
		}
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	defer c.endRotation()
	branches, err := c.Branches()
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	match, err := resolveBranch(branches, ref)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	if !agent.IsVisibleSession(match.Path) {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("branch %q not found", ref))
	}
	if err := c.ValidateSessionModel(match.Path); err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	if match.HeadID != "" {
		loadedHead, err := agent.LoadSessionHeadReadOnly(match.Path, match.HeadID)
		if err != nil {
			return agent.BranchInfo{}, c.rewindFail(err)
		}
		loaded := agent.NewSession("")
		loaded.Messages = loadedHead.Snapshot()
		newPath := agent.NewSessionPath(c.sessionDir, c.label)
		if err := loaded.SaveIfAbsent(newPath); err != nil {
			return agent.BranchInfo{}, c.rewindFail(err)
		}
		if err := c.publishSessionChild(newPath, loaded.Messages); err != nil {
			_ = os.Remove(newPath)
			return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("migrate legacy head: %w", err))
		}
		preview, turns := agent.SessionPreviewFromMessages(loaded.Messages)
		if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
			Name: strings.TrimSpace(match.Name), ParentID: agent.BranchID(match.Path), ForkTurn: -1,
			ForkMessageIndex: len(loaded.Messages), Preview: preview, Turns: turns,
			SchemaVersion: agent.BranchMetaCountsVersion, Model: c.selection.ref, ModelIdentity: c.selection.identity,
		}); err != nil {
			return agent.BranchInfo{}, c.rewindFail(err)
		}
		match = agent.BranchInfo{BranchMeta: agent.BranchMeta{ID: agent.BranchID(newPath), Name: match.Name, ParentID: agent.BranchID(match.Path)}, Path: newPath, Preview: preview, Turns: turns}
		commitTransition, err := c.prepareSessionTransition(newPath, "migrate-legacy-head", loaded)
		if err != nil {
			return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("bind migrated head: %w", err))
		}
		c.snapshotMu.Lock()
		commitTransition.publish()
		c.bindExecutorProjection(newPath, true)
		c.ResetPlannerSession()
		c.rebindCheckpoints(newPath)
		c.loadGuardianSession()
		c.loadRecoveryState(newPath)
		c.rotateSessionTemp()
		c.snapshotMu.Unlock()
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: "continued legacy version as an independent session"})
		return match, nil
	}
	loaded, err := agent.LoadSession(match.Path)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	commitTransition, err := c.prepareSessionTransition(match.Path, "switch", loaded)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("bind switched session: %w", err))
	}
	// See snapshotMu: the swap must not interleave with an in-flight save.
	c.snapshotMu.Lock()
	commitTransition.publish()
	c.bindExecutorProjection(match.Path, true)
	c.ResetPlannerSession()
	c.rebindCheckpoints(match.Path)
	c.loadGuardianSession()
	c.loadRecoveryState(match.Path)
	c.rotateSessionTemp()
	c.snapshotMu.Unlock()
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("switched to branch %s", branchDisplayName(match))})
	return match, nil
}

// ResolveBranchRef resolves a /switch-style branch reference (id, unique
// prefix, name, or path) against a branch listing, using the same matching
// rules as SwitchBranch. Frontends use it to learn the target session path
// before switching — e.g. to move their session lease first.
func ResolveBranchRef(branches []agent.BranchInfo, ref string) (agent.BranchInfo, error) {
	return resolveBranch(branches, strings.TrimSpace(ref))
}

func resolveBranch(branches []agent.BranchInfo, ref string) (agent.BranchInfo, error) {
	refLower := strings.ToLower(ref)
	var matches []agent.BranchInfo
	for _, b := range branches {
		nameLower := strings.ToLower(strings.TrimSpace(b.Name))
		switch {
		case b.ID == ref || strings.EqualFold(b.ID, ref):
			return b, nil
		case b.HeadID != "" && b.HeadID == ref:
			return b, nil
		case b.Name != "" && nameLower == refLower:
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(b.ID), refLower):
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(shortBranchID(b.ID)), refLower):
			matches = append(matches, b)
		case b.Path == ref:
			return b, nil
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return agent.BranchInfo{}, fmt.Errorf("branch %q is ambiguous", ref)
	}
	return agent.BranchInfo{}, fmt.Errorf("branch %q not found", ref)
}

func branchDisplayName(b agent.BranchInfo) string {
	if strings.TrimSpace(b.Name) != "" {
		return fmt.Sprintf("%s (%s)", b.Name, b.ID)
	}
	return b.ID
}

// afterHeadSwitch re-derives the per-transcript runtime state after the
// session moved to another head of the same log. Callers hold snapshotMu.
func (c *Controller) afterHeadSwitch(path string) {
	c.bindExecutorProjection(path, true)
	c.ResetPlannerSession()
	if c.guardianSess != nil {
		c.guardianSess.Reset()
	}
	c.rotateSessionTemp()
	c.emitHeadEvents()
	// Same path, different transcript: serve and remote clients rebind on this
	// barrier exactly as they do for a resume; local desktop tabs learn the
	// head in the desktop PR.
	c.sink.Emit(event.Event{Kind: event.SessionChanged, SessionPath: path, SessionReset: true})
}

// withHeadBranches lists the heads of the current schema-2 log as branches:
// the main head keeps the file's identity so the tree stays rooted at the
// log, and every other head hangs under its parent head.
func (c *Controller) withHeadBranches(branches []agent.BranchInfo) []agent.BranchInfo {
	// Existing heads are exposed for read/navigation only. Selecting one
	// materializes an independent session before execution.
	sess := c.loggedTurnSession()
	if sess == nil {
		return branches
	}
	path := c.SessionPath()
	heads, err := agent.ListSessionHeads(path)
	if err != nil || len(heads) <= 1 {
		return branches
	}
	fileID := agent.BranchID(path)
	headID := func(id string) string {
		if id == agent.SessionMainHead || id == "" {
			return fileID
		}
		return id
	}
	var file agent.BranchInfo
	out := make([]agent.BranchInfo, 0, len(branches)+len(heads))
	for _, b := range branches {
		if agent.CanonicalSessionPath(b.Path) == agent.CanonicalSessionPath(path) {
			file = b
			continue
		}
		out = append(out, b)
	}
	for _, h := range heads {
		if h.Retired {
			continue
		}
		info := file
		info.Path = path
		info.HeadID, info.HeadKind = h.ID, h.Kind
		info.ID = headID(h.ID)
		info.Turns, info.Preview = h.Turns, h.Preview
		if h.ID != agent.SessionMainHead {
			info.Name = h.Name
			info.ParentID = headID(h.ParentHead)
			info.ForkTurn, info.ForkMessageIndex = -1, 0
			info.CreatedAt = h.CreatedAt
		}
		out = append(out, info)
	}
	return out
}

// sessionHeadPolicy groups the frontend's choice between in-log heads and
// separate session files for branch operations.
type sessionHeadPolicy struct {
	fileBranchesOnly bool
}

// headBranchSession returns the session when branch operations may create
// heads inside its schema-2 log, nil when the frontend asked for files.
func (c *Controller) headBranchSession() *agent.Session {
	// New writes always materialize an independent child session. Existing
	// schema-2 heads remain discoverable through the legacy read adapter, but
	// they are never extended or used as a second writable head.
	return nil
}

// publishSessionChild creates a self-contained child before any UI/session switch.
// When the selected message prefix is an exact completed-turn boundary it
// copies the parent's immutable event batches. Legacy or pre-first-turn cuts
// are imported as history only and carry no activity or authorization state.
func (c *Controller) publishSessionChild(newPath string, messages []provider.Message) error {
	childDir := sessionDirectory(newPath)
	childID := agent.BranchID(newPath)
	if childDir == "" || childID == "" {
		return fmt.Errorf("invalid child identity")
	}
	if parent := c.sessionEventStore(); parent != nil {
		if _, err := parent.Flush(context.Background()); err != nil {
			return err
		}
		commits, err := session.Replay(sessionDirectory(c.SessionPath()), nil)
		if err != nil {
			return err
		}
		for i, v := range slices.Backward(commits) {
			commit := v
			if len(commit.Events) == 0 || commit.Events[len(commit.Events)-1].Kind != "turn/end" {
				continue
			}
			projection, projectErr := session.Project(commits[:i+1])
			if projectErr != nil {
				return projectErr
			}
			if reflect.DeepEqual(projection.Messages, messages) {
				_, forkErr := parent.Fork(context.Background(), childDir, childID, commit.LastSequence())
				return forkErr
			}
		}
		projected, projectErr := session.Project(commits)
		if projectErr != nil {
			return projectErr
		}
		if len(projected.Turns) > 0 {
			return fmt.Errorf("selected history is not an exact completed v3 turn boundary")
		}
	}
	if err := os.MkdirAll(filepath.Dir(childDir), 0o700); err != nil {
		return err
	}
	child, err := session.CreateStore(childDir, childID)
	if err != nil {
		return err
	}
	payload, marshalErr := json.Marshal(map[string]any{"messages": messages})
	if marshalErr == nil {
		_, marshalErr = child.Append(context.Background(), session.Batch{OperationID: "history-import", Events: []session.Event{{Kind: "legacy/import", Payload: payload}}})
	}
	if marshalErr == nil {
		_, marshalErr = child.Flush(context.Background())
	}
	return errors.Join(marshalErr, child.Close(context.Background()))
}

// SessionHead reports the schema-2 head the live session is on; ok is false
// for schema-1 sessions, whose branches are still separate files.
func (c *Controller) SessionHead() (agent.HeadRef, bool) {
	if c == nil || c.executor == nil || c.executor.Session() == nil {
		return agent.HeadRef{}, false
	}
	return c.executor.Session().Head()
}
