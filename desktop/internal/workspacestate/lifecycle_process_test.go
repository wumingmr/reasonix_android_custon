package workspacestate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type lifecycleProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	scan   *bufio.Scanner
	stderr *strings.Builder
}

func startLifecycleProcess(t *testing.T, path, action string, expected uint64) *lifecycleProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceLifecycleProcessHelper$")
	cmd.Env = append(os.Environ(),
		"REASONIX_WORKSPACE_PROCESS_HELPER=1",
		"REASONIX_WORKSPACE_PROCESS_PATH="+path,
		"REASONIX_WORKSPACE_PROCESS_ACTION="+action,
		"REASONIX_WORKSPACE_PROCESS_EXPECTED="+strconv.FormatUint(expected, 10),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &lifecycleProcess{cmd: cmd, stdin: stdin, scan: bufio.NewScanner(stdout), stderr: &stderr}
	if !child.scan.Scan() || child.scan.Text() != "ready" {
		_ = cmd.Process.Kill()
		t.Fatalf("%s child did not become ready: line=%q err=%v stderr=%s", action, child.scan.Text(), child.scan.Err(), stderr.String())
	}
	return child
}

func (p *lifecycleProcess) run(t *testing.T) string {
	t.Helper()
	if _, err := io.WriteString(p.stdin, "go\n"); err != nil {
		t.Fatal(err)
	}
	if err := p.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if !p.scan.Scan() {
		_ = p.cmd.Process.Kill()
		t.Fatalf("child result missing: err=%v stderr=%s", p.scan.Err(), p.stderr.String())
	}
	result := p.scan.Text()
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("child failed: %v stderr=%s", err, p.stderr.String())
	}
	return result
}

func seedArchivedProcessState(t *testing.T) (*Store, uint64) {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "state.json"))
	ctx := t.Context()
	if err := store.EnsureWorkspace(ctx, Workspace{ID: GlobalWorkspaceID, Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachSession(ctx, "", GlobalWorkspaceID, "victim", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(ctx, "victim"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return store, state.Generation
}

func TestWorkspaceLifecycleCommitOrderAcrossProcesses(t *testing.T) {
	t.Run("restore wins", func(t *testing.T) {
		store, expected := seedArchivedProcessState(t)
		restore := startLifecycleProcess(t, store.Path(), "restore", expected)
		purge := startLifecycleProcess(t, store.Path(), "purge", expected)
		if result := restore.run(t); result != "ok" {
			t.Fatalf("restore result = %q", result)
		}
		if result := purge.run(t); result != "conflict" {
			t.Fatalf("purge result = %q", result)
		}
		state, err := store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.SessionStates["victim"].Lifecycle != Active || ClassifyPurge(state, "victim") != PurgeAbsent {
			t.Fatalf("restore-first state = lifecycle=%+v purge=%v", state.SessionStates["victim"], ClassifyPurge(state, "victim"))
		}
	})

	t.Run("purge wins", func(t *testing.T) {
		store, expected := seedArchivedProcessState(t)
		restore := startLifecycleProcess(t, store.Path(), "restore", expected)
		purge := startLifecycleProcess(t, store.Path(), "purge", expected)
		if result := purge.run(t); result != "ok" {
			t.Fatalf("purge result = %q", result)
		}
		if result := restore.run(t); result != "conflict" {
			t.Fatalf("restore result = %q", result)
		}
		state, err := store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.SessionStates["victim"].Lifecycle != Deleted || ClassifyPurge(state, "victim") != PurgeTombstoned {
			t.Fatalf("purge-first state = lifecycle=%+v purge=%v", state.SessionStates["victim"], ClassifyPurge(state, "victim"))
		}
	})
}

func TestWorkspaceLifecycleProcessHelper(t *testing.T) {
	if os.Getenv("REASONIX_WORKSPACE_PROCESS_HELPER") != "1" {
		return
	}
	path := os.Getenv("REASONIX_WORKSPACE_PROCESS_PATH")
	action := os.Getenv("REASONIX_WORKSPACE_PROCESS_ACTION")
	expected, err := strconv.ParseUint(os.Getenv("REASONIX_WORKSPACE_PROCESS_EXPECTED"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	var observed Operation
	if action == "resume" {
		state, loadErr := store.Load(t.Context())
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		observed = state.PendingOperations["purge-victim"]
	}
	fmt.Println("ready")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	switch action {
	case "restore":
		err = store.RestoreSession(t.Context(), "victim")
	case "purge":
		err = store.BeginPurge(t.Context(), "victim", expected)
	case "resume":
		err = store.ResumePurge(t.Context(), "victim", observed)
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	switch {
	case err == nil:
		fmt.Println("ok")
	case errors.Is(err, ErrMutationConflict):
		fmt.Println("conflict")
	default:
		fmt.Printf("error:%v\n", err)
	}
}

func TestWorkspaceOldPurgeProcessCannotReplaceNewOperation(t *testing.T) {
	store, expected := seedArchivedProcessState(t)
	if err := store.mutate(t.Context(), func(s *State) error {
		s.PendingOperations["purge-victim"] = Operation{ID: "purge-victim", Kind: "purge", Phase: "prepared", Lifecycle: Deleted, SessionIDs: []string{"victim"}, ExpectedGeneration: expected}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := startLifecycleProcess(t, store.Path(), "resume", expected)
	if err := store.RestoreSession(t.Context(), "victim"); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveSession(t.Context(), "victim"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginPurge(t.Context(), "victim", state.Generation); err != nil {
		t.Fatal(err)
	}
	if got := old.run(t); got != "conflict" {
		t.Fatalf("old replay=%s", got)
	}
	state, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ClassifyPurge(state, "victim") != PurgeTombstoned {
		t.Fatal("old process changed new deletion")
	}
}
