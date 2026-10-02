package cdp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/browser"
)

func uploadRequest(tab browser.Tab, token, op string, files ...string) browser.ActRequest {
	return browser.ActRequest{
		OperationID: op, TabID: tab.ID, DocumentToken: token,
		Action: browser.ActionUpload, Ref: "e1", Files: files,
	}
}

// A page is untrusted: a file input that accepted any path would hand the
// site every file this process can read.
func TestUploadRefusesFilesOutsideTheTaskDirectories(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	owned := filepath.Join(workspace, "resume.pdf")
	secret := filepath.Join(outside, "id_rsa")
	for _, path := range []string{owned, secret} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	f := newFakeBrowser(t)
	exec := newTestExecutorWithRoots(t, f, workspace)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	res, err := exec.Act(ctx, uploadRequest(tab, snap.DocumentToken, "op-secret", secret))
	if err != nil {
		t.Fatalf("refused upload returned an error: %v", err)
	}
	if res.Executed {
		t.Fatal("a file outside the task's directories was attached to a page")
	}
	if !strings.Contains(res.Reason, "outside this task's directories") {
		t.Fatalf("reason = %q, want the containment refusal", res.Reason)
	}
	if f.countCalls("DOM.setFileInputFiles") != 0 {
		t.Fatal("the refused path still reached the browser")
	}
	if res, err = exec.Act(ctx, uploadRequest(tab, snap.DocumentToken, "op-owned", owned)); err != nil || !res.Executed {
		t.Fatalf("a workspace file was refused: %+v %v", res, err)
	}
}

// EvalSymlinks runs before the containment check, so a link the agent can
// write inside the workspace cannot point a file input at a private key.
func TestUploadRefusesASymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(secret, []byte("key"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(workspace, "innocent.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f := newFakeBrowser(t)
	exec := newTestExecutorWithRoots(t, f, workspace)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	res, err := exec.Act(ctx, uploadRequest(tab, snap.DocumentToken, "op-link", link))
	if err != nil {
		t.Fatalf("refused upload returned an error: %v", err)
	}
	if res.Executed {
		t.Fatal("a symlink out of the workspace was attached to a page")
	}
	if f.countCalls("DOM.setFileInputFiles") != 0 {
		t.Fatal("the symlink still reached the browser")
	}
}

// A download the agent just took is inside the executor's own directory and
// must stay attachable without configuring anything.
func TestUploadAllowsTheExecutorsOwnArtifacts(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutorWithRoots(t, f)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	downloaded := filepath.Join(exec.artifacts, "downloads", "report.csv")
	if err := os.WriteFile(downloaded, []byte("id\n"), 0o600); err != nil {
		t.Fatalf("stage download: %v", err)
	}
	res, err := exec.Act(ctx, uploadRequest(tab, snap.DocumentToken, "op-artifact", downloaded))
	if err != nil || !res.Executed {
		t.Fatalf("a downloaded file was refused: %+v %v", res, err)
	}
}

func TestUploadRefusesDirectoriesAndMissingFiles(t *testing.T) {
	workspace := t.TempDir()
	f := newFakeBrowser(t)
	exec := newTestExecutorWithRoots(t, f, workspace)
	ctx := context.Background()
	tab, snap := openTab(t, exec, ctx)

	for op, path := range map[string]string{
		"op-dir":     workspace,
		"op-missing": filepath.Join(workspace, "absent.txt"),
	} {
		res, err := exec.Act(ctx, uploadRequest(tab, snap.DocumentToken, op, path))
		if err != nil || res.Executed {
			t.Fatalf("%s: %+v %v, want a refusal", op, res, err)
		}
	}
}

func TestArtifactPathRefusesNamesThatAreNotLeaves(t *testing.T) {
	f := newFakeBrowser(t)
	exec := newTestExecutorWithRoots(t, f)
	for _, name := range []string{"../escape.png", "sub/shot.png", `..\escape.png`} {
		if _, err := exec.artifactPath("screenshots", name); err == nil {
			t.Fatalf("artifactPath accepted %q", name)
		}
	}
	path, err := exec.artifactPath("screenshots", "tab-1-1.png")
	if err != nil || filepath.Dir(path) != filepath.Join(exec.artifacts, "screenshots") {
		t.Fatalf("artifactPath(%q) = %q, %v", "tab-1-1.png", path, err)
	}
}
