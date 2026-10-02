package jobs

import (
	"context"

	"reasonix/internal/tool"
)

// HasTimedOut reports whether teardown returned before every job had unwound.
func (r TeardownResult) HasTimedOut() bool { return len(r.TimedOut) > 0 }

// SetExecution records terminal shell metadata for the background job carried
// by ctx. It is host-only metadata and does not change the persisted job log.
func SetExecution(ctx context.Context, execution *tool.ShellExecution) {
	if execution == nil {
		return
	}
	j, _ := ctx.Value(jobCtxKey{}).(*Job)
	if j == nil {
		return
	}
	j.mu.Lock()
	j.execution = tool.CloneShellExecution(execution)
	j.mu.Unlock()
}

// ExecutionForSession returns terminal shell metadata only for a job owned by
// parentSession. Task jobs and running shell jobs normally return nil.
func (m *Manager) ExecutionForSession(parentSession, id string) *tool.ShellExecution {
	j := m.get(parentSession, id)
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return tool.CloneShellExecution(j.execution)
}
