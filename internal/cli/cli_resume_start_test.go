package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
)

func ambiguousResumeWorkspace(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	dir := resolveCLISessionDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	alpha := saveQueryTestSession(t, dir, "alpha-branch.jsonl", "demo-alpha rollout")
	beta := saveQueryTestSession(t, dir, "beta-branch.jsonl", "demo-beta rollout")
	return alpha, beta
}

func TestHeadlessResumeAmbiguousQueryListsCandidates(t *testing.T) {
	alpha, beta := ambiguousResumeWorkspace(t)

	var rc int
	out := captureStderr(t, func() {
		_, rc = headlessResumeTarget("demo", false, false)
	})
	if rc != 1 {
		t.Fatalf("exit code = %d, want 1", rc)
	}
	for _, want := range []string{
		agent.BranchID(alpha), "demo-alpha rollout",
		agent.BranchID(beta), "demo-beta rollout",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stderr does not name candidate %q:\n%s", want, out)
		}
	}
}

func TestInteractiveResumeAmbiguousQueryOffersOnlyMatches(t *testing.T) {
	alpha, beta := ambiguousResumeWorkspace(t)
	_ = saveQueryTestSession(t, resolveCLISessionDir(), "gamma-branch.jsonl", "unrelated work")

	var offered []string
	choose := func(entries []resumeEntry) (cliResumeTarget, int) {
		for _, entry := range entries {
			offered = append(offered, entry.target.path)
		}
		return entries[len(entries)-1].target, 0
	}
	target, rc := resumeQueryTarget(resolveCLISessionDir(), "demo", os.Stderr, choose)
	if rc != 0 {
		t.Fatalf("exit code = %d, want the chooser's pick", rc)
	}
	if len(offered) != 2 || !containsPath(offered, alpha) || !containsPath(offered, beta) {
		t.Fatalf("chooser offered %v, want exactly %s and %s", offered, alpha, beta)
	}
	if target.path != offered[1] {
		t.Fatalf("target = %+v, want the chosen %s", target, offered[1])
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if filepath.Clean(path) == filepath.Clean(want) {
			return true
		}
	}
	return false
}
