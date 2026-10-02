package cli

// Discovery side of CLI takeover: find the resident serve processes recorded
// under <Reasonix home>/remote, ask one of them for the session through POST
// /handoff, and prompt before taking a held session over.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/remote/bootstrap"
	"reasonix/internal/store"
)

// cliTakeoverTimeout bounds the drain window of a wait-mode takeover.
const cliTakeoverTimeout = 2 * time.Minute

type cliServeRecord struct {
	pid   int
	base  string
	token string
}

type cliTakeoverGrant struct {
	SessionPath     string `json:"sessionPath"`
	MirrorID        string `json:"mirrorId"`
	HandoffID       string `json:"handoffId,omitempty"`
	ReturnHandoffID string `json:"returnHandoffId"`
	SourceWriterID  string `json:"sourceWriterId"`
	TargetWriterID  string `json:"targetWriterId"`
}

// cliServeProcessAlive is the PID probe used to prune serve state files
// whose process is gone. Variable so tests can model dead and live records.
var cliServeProcessAlive = webInstanceProcessAlive

// discoverCLIServes enumerates resident serve processes recorded under
// <Reasonix home>/remote. This machine is the SSH target in the takeover
// scenario, so the bootstrap's SFTP-written state files are local files here.
// Records whose PID no longer exists are skipped: a restarted desktop
// respawns the serve on a new port, and dialing the stale address only
// produces connection-refused noise that can shadow a live record's result.
func discoverCLIServes() []cliServeRecord {
	dir := config.RemoteStateDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []cliServeRecord
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "serve-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		state, err := bootstrap.UnmarshalState(data)
		if err != nil || state.PID <= 0 || !cliServeProcessAlive(state.PID) {
			continue
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(name, "serve-"), ".json")
		addr := state.Addr
		if port, err := os.ReadFile(filepath.Join(dir, store.RemoteServePortName(slug))); err == nil {
			if trimmed := strings.TrimSpace(string(port)); trimmed != "" {
				addr = trimmed
			}
		}
		if addr == "" {
			continue
		}
		token := ""
		if data, err := os.ReadFile(filepath.Join(dir, store.RemoteServeTokenName(slug))); err == nil {
			token = strings.TrimSpace(string(data))
		}
		if token == "" {
			continue
		}
		out = append(out, cliServeRecord{pid: state.PID, base: "http://" + addr, token: token})
	}
	return out
}

var discoverCLIServesForTakeover = discoverCLIServes

// cliServeForPID finds the resident serve holding the lease by matching the
// holder PID the lease error reported.
func cliServeForPID(pid int) *cliServeRecord {
	records := discoverCLIServes()
	for i := range records {
		if records[i].pid == pid {
			return &records[i]
		}
	}
	return nil
}

func cliServeClient(ctx context.Context, record cliServeRecord) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Jar: jar}
	auth, _ := json.Marshal(map[string]string{"token": record.token})
	authReq, err := http.NewRequestWithContext(ctx, http.MethodPost, record.base+"/auth/token", bytes.NewReader(auth))
	if err != nil {
		return nil, err
	}
	authReq.Header.Set("Content-Type", "application/json")
	authResp, err := client.Do(authReq)
	if err != nil {
		return nil, &cliServeUnreachableError{err: err}
	}
	_, _ = io.Copy(io.Discard, authResp.Body)
	authResp.Body.Close()
	if authResp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("serve auth: status %d", authResp.StatusCode)
	}
	return client, nil
}

// cliTakeoverHeldSession requests a target-writer reservation and consumes it
// through leases. The previous keeper binding is retained if either step
// fails; callers commit their controller only after this returns a binding.
func cliTakeoverHeldSession(sessionPath string, leaseErr error, leases *control.SessionLeaseKeeper, manager *cliTakeoverManager) (*cliTakeoverBinding, error) {
	if manager != nil && manager.Reclaiming() {
		return nil, fmt.Errorf("the remote side is reclaiming the current session")
	}
	pid := 0
	var leaseError *agent.SessionLeaseError
	if errors.As(leaseErr, &leaseError) && leaseError != nil && leaseError.Info != nil {
		pid = leaseError.Info.PID
	}
	if pid <= 0 {
		return nil, fmt.Errorf("%w; no local serve identity to take over from", agent.ErrSessionLeaseHeld)
	}
	record := cliServeForPID(pid)
	if record == nil {
		// The holder PID does not match any discovered serve (stale state
		// file, serve restart). The holder is on this machine, so try every
		// local serve: the one holding the session will accept the handoff.
		records := discoverCLIServes()
		if len(records) == 0 {
			return nil, fmt.Errorf("%w; holder pid %d is not a resident serve on this machine and no local serve is running", agent.ErrSessionLeaseHeld, pid)
		}
		var lastErr error
		for i := range records {
			binding, err := cliTakeoverFromServe(sessionPath, &records[i], leases, manager)
			if err == nil {
				return binding, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return cliTakeoverFromServe(sessionPath, record, leases, manager)
}

// postCLITakeoverHandoff exchanges one serve's /handoff for a grant. Both the
// legacy path-lease flow and the final-format identity flow validate the same
// wire contract; only the target key and post-grant reservation differ.
func postCLITakeoverHandoff(ctx context.Context, client *http.Client, base, sessionPath, errPrefix string) (cliTakeoverGrant, error) {
	body, _ := json.Marshal(map[string]any{
		"sessionPath": sessionPath, "targetWriterId": agent.SessionWriterID(),
		"force": true, "mode": "wait", "timeoutMs": cliTakeoverTimeout.Milliseconds(),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/handoff", bytes.NewReader(body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err != nil {
		return cliTakeoverGrant{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return cliTakeoverGrant{}, fmt.Errorf("%s: %w", errPrefix, &cliServeUnreachableError{err: err})
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return cliTakeoverGrant{}, fmt.Errorf("%s: %s", errPrefix, strings.TrimSpace(string(respBody)))
	}
	var grant cliTakeoverGrant
	if json.Unmarshal(respBody, &grant) != nil || grant.MirrorID == "" || grant.HandoffID == "" ||
		grant.ReturnHandoffID == "" || grant.SourceWriterID == "" || grant.TargetWriterID != agent.SessionWriterID() {
		return cliTakeoverGrant{}, fmt.Errorf("%s: invalid handoff grant", errPrefix)
	}
	return grant, nil
}

// cliTakeoverFromServe executes the handoff against one specific serve.
func cliTakeoverFromServe(sessionPath string, record *cliServeRecord, leases *control.SessionLeaseKeeper, manager *cliTakeoverManager) (*cliTakeoverBinding, error) {
	pid := record.pid
	ctx, cancel := context.WithTimeout(context.Background(), cliTakeoverTimeout+15*time.Second)
	defer cancel()
	client, err := cliServeClient(ctx, *record)
	if err != nil {
		return nil, fmt.Errorf("takeover from local serve (pid %d): %w", pid, err)
	}
	grant, err := postCLITakeoverHandoff(ctx, client, record.base, sessionPath, fmt.Sprintf("takeover from local serve (pid %d)", pid))
	if err != nil {
		return nil, err
	}
	binding := &cliTakeoverBinding{path: sessionPath, record: *record, client: client, grant: grant}
	if manager != nil {
		current, _, _, _ := manager.snapshot()
		if current != nil && !manager.Returned() && agent.CanonicalSessionPath(current.path) != agent.CanonicalSessionPath(sessionPath) {
			binding.priorMirror = current
		}
	}
	previous, err := leases.RebindDetachingWithHandoff(sessionPath, grant.SourceWriterID, grant.HandoffID)
	if err != nil {
		cliEndFailedHandoff(binding)
		return nil, err
	}
	binding.previous = previous
	return binding, nil
}

// cliSessionTakeoverCandidate reports whether leaseErr describes a session
// /takeover can actually take: the lease info must identify a holder, and at
// least one resident serve must exist on this machine to hand the session
// over. The holder does not have to be a discovered serve — a serve's state
// file PID drifts across restarts and desktop reconnects, and the takeover
// execution falls back to trying every local serve — but with no serve at all
// the holder is another CLI or an unrelated runtime that has no handoff
// endpoint, and offering /takeover would only promise a command that must
// fail. The refusal then names the holder and the close hint instead.
func cliSessionTakeoverCandidate(leaseErr error) bool {
	var leaseError *agent.SessionLeaseError
	if !errors.As(leaseErr, &leaseError) || leaseError == nil || leaseError.Info == nil {
		return false
	}
	return len(discoverCLIServesForTakeover()) > 0
}

// promptSessionTakeover asks on the terminal (pre-TUI startup) whether to take
// the held session over. Non-interactive sessions answer no.
func promptSessionTakeover(leaseErr error) bool {
	if !isInteractive() {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s\n", sessionLeaseResumeRefusal(leaseErr))
	fmt.Fprint(os.Stderr, "take over the session from this machine's resident serve? [y/N] ")
	answer, err := readCLITakeoverAnswer()
	if err != nil {
		return false
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func readCLITakeoverAnswer() (string, error) {
	buf := make([]byte, 64)
	n, err := os.Stdin.Read(buf)
	if n > 0 {
		return string(buf[:n]), nil
	}
	return "", err
}
