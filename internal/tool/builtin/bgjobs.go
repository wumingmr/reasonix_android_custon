package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"reasonix/internal/evidence"
	"reasonix/internal/jobs"
	"reasonix/internal/planmode"
	"reasonix/internal/tool"
)

// job_output / job_kill operate background jobs registered by shell and task
// run_in_background calls; legacy aliases remain for replay. They reach the session's
// job manager through the call context (jobs.FromContext) — the agent stamps it
// onto every tool call — and degrade to a clear error when it isn't available
// (a headless context with no manager). Together they poll a job's new output,
// terminate a job, and block until jobs finish.

func init() {
	tool.RegisterBuiltin(jobOutput{})
	tool.RegisterBuiltin(jobKill{})
	tool.RegisterBuiltin(bashOutput{})
	tool.RegisterBuiltin(killShell{})
	tool.RegisterBuiltin(waitJob{})
}

const (
	jobOutputDefaultWait = 30 * time.Second
	jobOutputMaxWait     = 10 * time.Minute
)

// job_output is the provider-facing Harness-compatible job reader. Legacy
// bash_output and wait remain registered for replaying older sessions.
type jobOutput struct{}

func (jobOutput) Name() string { return "job_output" }

func (jobOutput) Description() string {
	return "Read output from a background job. Reads are non-blocking unless wait=true; every response includes the current status. Do not busy-poll a running job."
}

func (jobOutput) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"Job id returned by the tool that started the background work."},"wait":{"type":"boolean","description":"Wait until the job finishes or timeout_ms elapses. A timeout leaves the job running."},"timeout_ms":{"type":"integer","minimum":1,"maximum":600000,"description":"Maximum wait in milliseconds. Defaults to 30000 and is capped at 600000."},"filter":{"type":"string","description":"Optional regular expression; only matching lines of new output are returned."}},"required":["job_id"]}`)
}

func (jobOutput) ReadOnly() bool { return true }

func (jobOutput) ProviderVisible(ctx context.Context) bool {
	_, ok := jobs.FromContext(ctx)
	return ok
}

func (jobOutput) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	result, err := (jobOutput{}).ExecuteDetailed(ctx, args)
	return result.Output, err
}

func (jobOutput) ExecutionDescriptor(json.RawMessage) *tool.ShellExecution { return nil }

func (jobOutput) ExecuteDetailed(ctx context.Context, args json.RawMessage) (tool.DetailedResult, error) {
	var p struct {
		JobID     string `json:"job_id"`
		Wait      bool   `json:"wait"`
		TimeoutMS int    `json:"timeout_ms"`
		Filter    string `json:"filter"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.DetailedResult{}, fmt.Errorf("invalid args: %w", err)
	}
	p.JobID = strings.TrimSpace(p.JobID)
	if p.JobID == "" {
		return tool.DetailedResult{}, fmt.Errorf("job_id is required")
	}
	if p.TimeoutMS < 0 {
		return tool.DetailedResult{}, fmt.Errorf("timeout_ms must be positive")
	}
	// Validate before waiting or consuming the manager's incremental cursor.
	if _, err := regexp.Compile(p.Filter); err != nil {
		return tool.DetailedResult{}, fmt.Errorf("invalid filter: %w", err)
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return tool.DetailedResult{}, fmt.Errorf("background jobs are not available in this context")
	}
	session := jobs.SessionFromContext(ctx)
	if p.Wait {
		waitFor := jobOutputDefaultWait
		if p.TimeoutMS > 0 {
			waitFor = cappedMilliseconds(p.TimeoutMS, jobOutputMaxWait)
		}
		if waitFor > jobOutputMaxWait {
			waitFor = jobOutputMaxWait
		}
		waitCtx, cancel := context.WithTimeout(ctx, waitFor)
		_ = jm.WaitForSession(waitCtx, session, []string{p.JobID}, 0)
		cancel()
	}
	text, status, found := jm.OutputForSession(session, p.JobID)
	if !found {
		return tool.DetailedResult{}, fmt.Errorf("no background job %q", p.JobID)
	}
	if status != jobs.Running {
		collectBackgroundEvidence(ctx, jm, p.JobID)
	}
	if p.Filter != "" && text != "" {
		filtered, err := filterLines(text, p.Filter)
		if err != nil {
			return tool.DetailedResult{}, err
		}
		text = filtered
	}
	body := strings.TrimRight(text, "\n")
	if strings.TrimSpace(body) == "" {
		body = "(no new output)"
	}
	return tool.DetailedResult{
		Output:    fmt.Sprintf("%s\n[status: %s]", body, status),
		Execution: jm.ExecutionForSession(session, p.JobID),
	}, nil
}

// job_kill is the provider-facing Harness-compatible cancellation tool.
type jobKill struct{}

func (jobKill) Name() string { return "job_kill" }

func (jobKill) Description() string {
	return "Request cancellation of a running background job by job id. Returns immediately; the process tree settles as killed once shutdown completes."
}

func (jobKill) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"Job id returned by the tool that started the background work."},"reason":{"type":"string","description":"Optional short reason for stopping the job."}},"required":["job_id"]}`)
}

func (jobKill) ReadOnly() bool { return false }

func (jobKill) ProviderVisible(ctx context.Context) bool {
	_, ok := jobs.FromContext(ctx)
	return ok
}

func (jobKill) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobID  string `json:"job_id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	p.JobID = strings.TrimSpace(p.JobID)
	if p.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	if jm.KillForSession(jobs.SessionFromContext(ctx), p.JobID) {
		return fmt.Sprintf("Requested cancellation of job %q.\n[status: killed]", p.JobID), nil
	}
	return fmt.Sprintf("Job %q had already finished or is unknown.", p.JobID), nil
}

// bash_output: poll a background job's new output (non-blocking)

type bashOutput struct{}

func (bashOutput) Name() string { return "bash_output" }

func (bashOutput) Description() string {
	return "Legacy alias: read new output from a background job without blocking. New calls should use job_output."
}

func (bashOutput) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"The background job id (e.g. \"bash-1\") returned when it was started."},"filter":{"type":"string","description":"Optional regular expression; only matching lines of the new output are returned."}},"required":["job_id"]}`)
}

func (bashOutput) ReadOnly() bool { return true }

func (bashOutput) HiddenFromCapabilityCatalog() bool { return true }

func (bashOutput) ProviderVisible(ctx context.Context) bool {
	_, ok := jobs.FromContext(ctx)
	return ok
}

func (bashOutput) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobID  string `json:"job_id"`
		Filter string `json:"filter"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	if _, err := regexp.Compile(p.Filter); err != nil {
		return "", fmt.Errorf("invalid filter regexp: %w", err)
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	text, status, found := jm.OutputForSession(jobs.SessionFromContext(ctx), p.JobID)
	if !found {
		return "", fmt.Errorf("no background job %q", p.JobID)
	}
	if status != jobs.Running {
		collectBackgroundEvidence(ctx, jm, p.JobID)
	}
	if p.Filter != "" && text != "" {
		filtered, err := filterLines(text, p.Filter)
		if err != nil {
			return "", err
		}
		text = filtered
	}
	header := fmt.Sprintf("[%s] %s", p.JobID, status)
	if strings.TrimSpace(text) == "" {
		return header + "\n(no new output)", nil
	}
	return header + "\n" + text, nil
}

// filterLines keeps only the lines of s matching the regular expression re.
func filterLines(s, re string) (string, error) {
	rx, err := regexp.Compile(re)
	if err != nil {
		return "", fmt.Errorf("invalid filter regexp: %w", err)
	}
	var keep []string
	for line := range strings.SplitSeq(s, "\n") {
		if rx.MatchString(line) {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n"), nil
}

// kill_shell: terminate a running background job

type killShell struct{}

func (killShell) Name() string { return "kill_shell" }

func (killShell) Description() string {
	return "Legacy alias: terminate a running background job. New calls should use job_kill."
}

func (killShell) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"The background job id to terminate (e.g. \"bash-1\")."}},"required":["job_id"]}`)
}

func (killShell) ReadOnly() bool { return false }

func (killShell) HiddenFromCapabilityCatalog() bool { return true }

func (killShell) ProviderVisible(ctx context.Context) bool {
	_, ok := jobs.FromContext(ctx)
	return ok
}

func (killShell) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	if jm.KillForSession(jobs.SessionFromContext(ctx), p.JobID) {
		return fmt.Sprintf("Killed background job %q.", p.JobID), nil
	}
	return fmt.Sprintf("Background job %q was not running (already finished or unknown).", p.JobID), nil
}

// wait: block until background jobs finish, then return their results

type waitJob struct{}

func (waitJob) Name() string { return "wait" }

func (waitJob) Description() string {
	return "Legacy multi-job wait retained for old sessions. New calls should use job_output with wait=true."
}

func (waitJob) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_ids":{"type":"array","items":{"type":"string"},"description":"Background job ids to wait for. Omit to wait for every currently-running job."},"timeout_seconds":{"type":"integer","description":"Optional maximum seconds to block before returning current progress. Omit to wait until the jobs finish.","minimum":1}}}`)
}

func (waitJob) ReadOnly() bool { return true }

func (waitJob) HiddenFromCapabilityCatalog() bool { return true }

func (waitJob) ProviderVisible(ctx context.Context) bool {
	_, ok := jobs.FromContext(ctx)
	return ok
}

func (waitJob) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobIDs         []string `json:"job_ids"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	jm, ok := jobs.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("background jobs are not available in this context")
	}
	results := jm.WaitForSession(ctx, jobs.SessionFromContext(ctx), p.JobIDs, p.TimeoutSeconds)
	if len(results) == 0 {
		return "No background jobs to wait for.", nil
	}
	var b strings.Builder
	for i, r := range results {
		if r.Status != jobs.Running {
			collectBackgroundEvidence(ctx, jm, r.ID)
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		label := r.ID
		if r.Label != "" {
			label = fmt.Sprintf("%s (%s)", r.ID, r.Label)
		}
		fmt.Fprintf(&b, "[%s] %s", label, r.Status)
		if strings.TrimSpace(r.Output) != "" {
			b.WriteString("\n" + r.Output)
		}
	}
	return b.String(), nil
}

func collectBackgroundEvidence(ctx context.Context, jm *jobs.Manager, jobID string) {
	// A Plan turn should not consume a finished background writer's mutation
	// receipts before the workflow reaches execution. Writers may still run after
	// Permissions approval; leave their evidence on the job so the first
	// post-approval collection can merge and audit it.
	if planmode.Active(ctx) {
		return
	}
	ledger, ok := evidence.FromContext(ctx)
	if !ok || ledger == nil || jm == nil {
		return
	}
	session := jobs.SessionFromContext(ctx)
	// A non-Running status from bash_output/wait does not guarantee the job's
	// run goroutine has actually flushed PublishEvidence and closed done: kill_shell
	// flips status to Killed synchronously, well before its cancelled goroutine
	// unwinds. Check readiness before noting the lease — noting it on an empty,
	// not-yet-ready read would dedupe away every later retry in this turn (the
	// lease is idempotent per turn) while the job later publishes real mutation
	// evidence nobody ever merges or reviews.
	summary, ready := jm.TryLeaseEvidenceForSession(session, jobID)
	if !ready {
		return
	}
	// Note the lease before merging so a second wait/bash_output in the same
	// turn does not double-count. The merge is provisional: the lease does not
	// consume, so if this turn fails the agent never commits and the next turn
	// re-collects. The agent commits leased jobs only after the turn passes its
	// delivery gates.
	if !ledger.NoteBackgroundLease(session, jobID) {
		return
	}
	ledger.MergeChild(summary)
}
