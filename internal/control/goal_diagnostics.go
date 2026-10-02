package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"runtime/debug"
	"strings"
	"time"

	goaldomain "reasonix/internal/goal"
	"reasonix/internal/secrets"
	"reasonix/internal/session"
)

// GoalDiagnosticMetadata is supplied by the host so a user-exported artifact
// identifies the exact build and negotiated feature surface that produced it.
type GoalDiagnosticMetadata struct {
	ApplicationVersion string   `json:"applicationVersion,omitempty"`
	BuildCommit        string   `json:"buildCommit,omitempty"`
	ProtocolVersion    int      `json:"protocolVersion,omitempty"`
	Capabilities       []string `json:"capabilities"`
}

type goalDiagnosticTransition struct {
	Sequence      uint64                `json:"sequence"`
	OperationID   string                `json:"operationId"`
	GoalID        string                `json:"goalId,omitempty"`
	Revision      uint64                `json:"revision,omitempty"`
	Phase         goaldomain.Phase      `json:"phase,omitempty"`
	Activation    goaldomain.Activation `json:"activation"`
	RoundsStarted uint64                `json:"roundsStarted,omitempty"`
	Inferred      bool                  `json:"inferred"`
}

// ExportGoalDiagnostics is the compatibility in-memory form. Production hosts
// use WriteGoalDiagnostics so diagnostic size is not a memory or RPC limit.
func (c *Controller) ExportGoalDiagnostics(ctx context.Context, metadata GoalDiagnosticMetadata) ([]byte, error) {
	var output bytes.Buffer
	if err := c.WriteGoalDiagnostics(ctx, &output, metadata); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// WriteGoalDiagnostics streams the authoritative accepted event prefix after
// attempting a Flush checkpoint. A failed Flush is exported as evidence, and
// credential-like material is redacted one commit at a time.
func (c *Controller) WriteGoalDiagnostics(ctx context.Context, dst io.Writer, metadata GoalDiagnosticMetadata) error {
	return c.WriteSessionDiagnostics(ctx, dst, metadata, nil)
}

// WriteSessionDiagnostics preserves the goal schema while adding host observations.
func (c *Controller) WriteSessionDiagnostics(ctx context.Context, dst io.Writer, metadata GoalDiagnosticMetadata, extra map[string]any) error {
	if c == nil {
		return session.ErrSessionNotRunning
	}
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil {
		return errors.New("goal diagnostics require a canonical session")
	}
	cut := runtime.Session().EventSequence()
	var exportSnapshot session.ExportSnapshot
	if value, ok := extra["exportSnapshot"]; ok {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &exportSnapshot); err != nil {
			return err
		}
		if exportSnapshot.Ref != runtime.Ref() || exportSnapshot.SnapshotSequence > cut {
			return errors.New("diagnostic export source changed")
		}
		cut = exportSnapshot.SnapshotSequence
	}
	_, flushErr := runtime.Session().FlushThrough(ctx, cut)
	if metadata.Capabilities == nil {
		metadata.Capabilities = []string{}
	}
	fillGoalDiagnosticBuildMetadata(&metadata)
	state := runtime.StateSnapshot()
	state.Session.PersistenceError = secrets.RedactCredentials(state.Session.PersistenceError)
	observation := c.RuntimeStateSnapshot()
	observation.PersistenceErr = secrets.RedactCredentials(observation.PersistenceErr)
	unavailable := []string{
		"activation transitions are inferred from recorded Goal events; process-local activation history before export is unavailable",
	}
	if flushErr != nil {
		unavailable = append(unavailable, "durability checkpoint failed: "+secrets.RedactError(flushErr))
	}
	if _, err := io.WriteString(dst, "{\n"); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value any
	}{
		{"schemaVersion", 1},
		{"exportedAt", time.Now().UTC()},
		{"metadata", metadata},
		{"runtime", state},
		{"observation", observation},
		{"submissionDiagnostics", map[string]uint64{"reused": c.submissions.reused.Load(), "conflicts": c.submissions.conflicts.Load(), "unknown": c.submissions.unknown.Load()}},
		{"shellDiagnostics", c.persistentShell.Diagnostics()},
		{"lifecycleDiagnostics", c.lifecycleDiagnosticSnapshot()},
		{"providerDiagnostics", c.providerDiagnosticSnapshot()},
		{"acceptedThrough", cut},
		{"runtimeObservedAt", time.Now().UTC()},
		{"runtimeObservedThrough", state.Session.EventSequence},
		{"durableThrough", state.Session.DurableSequence},
		{"persistenceStatus", state.Session.PersistenceStatus},
		{"persistenceError", state.Session.PersistenceError},
	}
	if exportSnapshot.Ref.SessionID != "" {
		exportSnapshot.DurableThrough = state.Session.DurableSequence
		extra = cloneDiagnosticExtras(extra)
		extra["exportSnapshot"] = exportSnapshot
	}
	for name, value := range extra {
		fields = append(fields, struct {
			name  string
			value any
		}{name, value})
	}
	for _, field := range fields {
		if err := writeGoalDiagnosticField(dst, field.name, field.value, true); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(dst, "  \"commits\": ["); err != nil {
		return err
	}
	return writeSessionDiagnosticCommits(ctx, dst, runtime.Session(), cut, unavailable)
}

func writeSessionDiagnosticCommits(ctx context.Context, dst io.Writer, store *session.Session, cut uint64, unavailable []string) error {
	first := true
	var destinationError error
	changes := []goalDiagnosticTransition{}
	activation := goaldomain.ActivationDisarmed
	err := visitAcceptedGoalDiagnosticCommits(ctx, store, cut, func(commit session.Commit) error {
		encoded, err := json.MarshalIndent(commit, "    ", "  ")
		if err != nil {
			return err
		}
		encoded, err = secrets.RedactJSON(encoded)
		if err != nil {
			return err
		}
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, encoded, "    ", "  "); err != nil {
			return err
		}
		encoded = formatted.Bytes()
		separator := "\n    "
		if !first {
			separator = ",\n    "
		}
		if _, err := io.WriteString(dst, separator); err != nil {
			destinationError = err
			return err
		}
		if _, err := dst.Write(encoded); err != nil {
			destinationError = err
			return err
		}
		first = false
		changes = append(changes, goalActivationChangesForCommit(commit, &activation)...)
		return nil
	})
	if destinationError != nil {
		return destinationError
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		unavailable = append(unavailable, "accepted event traversal failed: "+secrets.RedactError(err))
	}
	if !first {
		if _, err := io.WriteString(dst, "\n  "); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(dst, "],\n"); err != nil {
		return err
	}
	if err := writeGoalDiagnosticField(dst, "activationChanges", changes, true); err != nil {
		return err
	}
	if err := writeGoalDiagnosticField(dst, "unavailable", unavailable, false); err != nil {
		return err
	}
	_, err = io.WriteString(dst, "}\n")
	return err
}

func visitAcceptedGoalDiagnosticCommits(ctx context.Context, store *session.Session, through uint64, visit func(session.Commit) error) error {
	offset := uint64(0)
	for {
		page, err := store.AcceptedPage(ctx, offset, 1000)
		if err != nil {
			return err
		}
		for _, commit := range page.Commits {
			if commit.LastSequence() > through {
				return nil
			}
			if err := visit(commit); err != nil {
				return err
			}
		}
		if !page.Truncated {
			return nil
		}
		if page.Next <= offset {
			return errors.New("goal diagnostics accepted-page cursor did not advance")
		}
		offset = page.Next
	}
}

func writeGoalDiagnosticField(dst io.Writer, name string, value any, comma bool) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded, err = secrets.RedactJSON(encoded)
	if err != nil {
		return err
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, encoded, "  ", "  "); err != nil {
		return err
	}
	encoded = formatted.Bytes()
	if _, err := fmt.Fprintf(dst, "  %q: ", name); err != nil {
		return err
	}
	if _, err := dst.Write(encoded); err != nil {
		return err
	}
	if comma {
		_, err = io.WriteString(dst, ",")
		if err != nil {
			return err
		}
	}
	_, err = io.WriteString(dst, "\n")
	return err
}

func fillGoalDiagnosticBuildMetadata(metadata *GoalDiagnosticMetadata) {
	if metadata == nil {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if metadata.ApplicationVersion == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		metadata.ApplicationVersion = info.Main.Version
	}
	if metadata.BuildCommit != "" {
		return
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			metadata.BuildCommit = setting.Value
			return
		}
	}
}

func goalActivationChangesForCommit(commit session.Commit, activation *goaldomain.Activation) []goalDiagnosticTransition {
	changes := []goalDiagnosticTransition{}
	hasTurnStart := false
	for _, item := range commit.Events {
		hasTurnStart = hasTurnStart || item.Kind == "turn/start"
	}
	for _, item := range commit.Events {
		if item.Kind != "goal/state" {
			continue
		}
		var document struct {
			Current *goaldomain.Snapshot `json:"current"`
		}
		if json.Unmarshal(item.Payload, &document) != nil {
			continue
		}
		transition := goalDiagnosticTransition{Sequence: item.Sequence, OperationID: commit.OperationID, Activation: goaldomain.ActivationDisarmed, Inferred: true}
		if document.Current != nil {
			transition.GoalID = document.Current.ID
			transition.Revision = document.Current.Revision
			transition.Phase = document.Current.Phase
			transition.RoundsStarted = document.Current.RoundsStarted
			if document.Current.Phase == goaldomain.PhaseActive {
				op := strings.ToLower(commit.OperationID)
				if hasTurnStart || strings.Contains(op, ":create") || strings.Contains(op, ":resume") || strings.Contains(op, "goal-control:set") {
					*activation = goaldomain.ActivationArmed
				}
			} else {
				*activation = goaldomain.ActivationDisarmed
			}
			transition.Activation = *activation
		} else {
			*activation = goaldomain.ActivationDisarmed
		}
		changes = append(changes, transition)
	}
	return changes
}

func cloneDiagnosticExtras(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	maps.Copy(out, input)
	return out
}
