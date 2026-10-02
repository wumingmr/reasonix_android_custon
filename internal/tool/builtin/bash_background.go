package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"reasonix/internal/jobs"
	"reasonix/internal/proc"
	"reasonix/internal/sandbox"
	"reasonix/internal/sessiontemp"
	"reasonix/internal/shellrun"
	"reasonix/internal/tool"
)

func (b bash) startBackground(ctx context.Context, p bashParams, sh sandbox.Shell, argv []string, wrapped bool, cmdEnv []string, lease *sessiontemp.Lease, releaseLease *bool, start time.Time, ex *tool.ShellExecution) (tool.DetailedResult, error) {
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		ex.State = tool.ShellStateNotRun
		ex.FailurePhase = tool.ShellPhaseDependency
		ex.MutationRisk = tool.ShellMutationNotStarted
		ex.DurationMs = time.Since(start).Milliseconds()
		return tool.DetailedResult{Execution: ex}, fmt.Errorf("background execution is not available in this context")
	}

	// The job closure owns the lease after StartForSession. It releases the
	// generation after either process completion or a start failure.
	jobLease := lease
	*releaseLease = false
	jobLabel := strings.TrimSpace(p.Description)
	if jobLabel == "" {
		jobLabel = commandPreview(p.Command)
	}
	permissionPreset := string(sandbox.PermissionPresetFrom(ctx))
	jobSpec := b.specForCall(ctx)
	job, err := jm.TryStartSessionProcess(jobs.SessionFromContext(ctx), b.Name(), jobLabel, func(jobCtx context.Context, out io.Writer) (string, error) {
		if jobLease != nil {
			defer jobLease.Release()
		}
		cmd := proc.CommandContext(jobCtx, argv[0], argv[1:]...)
		cmd.Dir = b.workDir
		cmd.Env = cmdEnv
		cmd.WaitDelay = bashWaitDelay
		capture := &boundedDiagnosticWriter{limit: 64 << 10}
		combined := io.MultiWriter(out, capture)
		cmd.Stdout = combined
		cmd.Stderr = combined
		started := time.Now()
		tracked, runErr := runShellProcess(jobCtx, cmd, sh, p.Command, shouldTrackShellProcess(wrapped, sh, p.Command, p.PreserveBackgroundProcesses))
		if shouldReapAfterRun(jobCtx, sh, p.Command, p.PreserveBackgroundProcesses) {
			reapShellProcess(cmd, tracked)
		}
		runErr = normalizeBashRunError(jobCtx, runErr, p.PreserveBackgroundProcesses)
		execution, classifiedErr := classifyBackgroundShellExecution(jobCtx, sh, argv, capture.String(), runErr, started)
		if classifiedErr != nil && execution.FailurePhase == tool.ShellPhaseExecution && wrapped {
			writeBackgroundSandboxHint(out, capture.String(), classifiedErr, p, jobSpec, permissionPreset, b.workDir)
		}
		jobs.SetExecution(jobCtx, execution)
		return "", classifiedErr
	})
	if err != nil {
		*releaseLease = true
		ex.State = tool.ShellStateNotRun
		ex.FailurePhase = tool.ShellPhaseDependency
		ex.MutationRisk = tool.ShellMutationNotStarted
		return tool.DetailedResult{Execution: ex}, err
	}

	msg := fmt.Sprintf("Started background job %q. It keeps running across turns; read it with job_output(job_id=%q) or stop it with job_kill(job_id=%q).", job.ID, job.ID, job.ID)
	ex.State = tool.ShellStateBackgroundStarted
	ex.MutationRisk = tool.ShellMutationUnknown
	ex.DurationMs = time.Since(start).Milliseconds()
	return tool.DetailedResult{
		Output:    appendSessionDataHint(msg, b.guard.CommandHint(b.workDir, p.Command)),
		Execution: ex,
	}, nil
}

func writeBackgroundSandboxHint(out io.Writer, captured string, runErr error, p bashParams, spec sandbox.Spec, permissionPreset, workDir string) {
	hinted := appendSandboxWriteHint(captured, runErr, p, spec, permissionPreset, workDir)
	if hint := strings.TrimPrefix(hinted, captured); hint != "" {
		_, _ = io.WriteString(out, hint+"\n")
	}
}

type boundedDiagnosticWriter struct {
	buf   []byte
	limit int
}

func (w *boundedDiagnosticWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.limit <= 0 {
		return n, nil
	}
	if len(p) >= w.limit {
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return n, nil
	}
	w.buf = append(w.buf, p...)
	if overflow := len(w.buf) - w.limit; overflow > 0 {
		copy(w.buf, w.buf[overflow:])
		w.buf = w.buf[:w.limit]
	}
	return n, nil
}

func (w *boundedDiagnosticWriter) String() string { return string(w.buf) }

func classifyBackgroundShellExecution(ctx context.Context, sh sandbox.Shell, argv []string, output string, runErr error, started time.Time) (*tool.ShellExecution, error) {
	ex := shellrun.DescriptorFromShell(sh)
	ex.DurationMs = time.Since(started).Milliseconds()
	switch {
	case ctx.Err() != nil:
		ex.State = tool.ShellStateCancelled
		ex.FailurePhase = tool.ShellPhaseCancellation
		ex.MutationRisk = tool.ShellMutationMayBePartial
		return ex, runErr
	case runErr == nil:
		ex.State = tool.ShellStateCompleted
		ex.ExitCode = tool.IntPtr(0)
		ex.MutationRisk = tool.ShellMutationMayHaveCompleted
		return ex, nil
	}
	if exitErr := (*exec.ExitError)(nil); errors.As(runErr, &exitErr) {
		ex.State = tool.ShellStateFailed
		ex.FailurePhase = tool.ShellPhaseExecution
		ex.ExitCode = tool.IntPtr(exitErr.ExitCode())
		ex.MutationRisk = tool.ShellMutationMayBePartial
		return ex, runErr
	}
	ex.State = tool.ShellStateNotRun
	ex.FailurePhase = tool.ShellPhaseLaunch
	ex.MutationRisk = tool.ShellMutationNotStarted
	return ex, runErr
}
