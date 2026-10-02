//go:build windows

package config

import (
	"crypto/sha1"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCredentialAccessRepairsLegacyCredentialDeny(t *testing.T) {
	for _, operation := range []string{"save", "load", "revision"} {
		t.Run(operation, func(t *testing.T) {
			testCredentialAccessRepairsLegacyDeny(t, operation)
		})
	}
}

func TestCredentialAccessRepairsLegacyDenyThroughLinkedHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	realHome := t.TempDir()
	linkedHome := filepath.Join(t.TempDir(), "reasonix-home")
	if err := os.Symlink(realHome, linkedHome); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	t.Setenv("REASONIX_HOME", linkedHome)
	path := UserCredentialsPath()
	if err := os.WriteFile(path, []byte("EXISTING_KEY=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if user == nil || user.User.Sid == nil {
		t.Fatal("current process token has no user SID")
	}
	trustee := "*" + user.User.Sid.String()
	if output, err := exec.Command("icacls", realPath, "/deny", trustee+":(RX)").CombinedOutput(); err != nil {
		t.Fatalf("install legacy credential deny ACL: %v: %s", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() {
		_ = exec.Command("icacls", realPath, "/remove:d", trustee, "/C").Run()
	})
	markerDir := filepath.Join(os.TempDir(), "windows-sandbox-denylocks")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(markerDir, strconv.Itoa(os.Getpid())+"-credential-linked-home-test.txt")
	if err := os.WriteFile(marker, []byte("deny\t"+realPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	file, ok := readDotEnvFile(path)
	if !ok || file.Values["EXISTING_KEY"] != "old" {
		t.Fatal("credential load through linked home failed to repair legacy deny")
	}
}

func testCredentialAccessRepairsLegacyDeny(t *testing.T, operation string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("ACL_REPAIR_INTEGRATION_KEY", "")
	path := UserCredentialsPath()
	if err := os.WriteFile(path, []byte("EXISTING_KEY=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if user == nil || user.User.Sid == nil {
		t.Fatal("current process token has no user SID")
	}
	trustee := "*" + user.User.Sid.String()
	if output, err := exec.Command("icacls", path, "/deny", trustee+":(RX)").CombinedOutput(); err != nil {
		t.Fatalf("install legacy credential deny ACL: %v: %s", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() {
		_ = exec.Command("icacls", path, "/remove:d", trustee, "/C").Run()
	})
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("legacy deny ACL did not block credential reads")
	}
	// Lock identity must be available before ACL repair, without reading the
	// protected contents or silently lifting the deny as part of resolution.
	if _, err := resolveConfigAccessPathUnpinned(path, true); err != nil {
		t.Fatalf("resolve identity before credential ACL repair: %v", err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("identity resolution removed the credential read deny")
	}
	markerDir := filepath.Join(os.TempDir(), "windows-sandbox-denylocks")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(markerDir, strconv.Itoa(os.Getpid())+"-credential-test.txt")
	if err := os.WriteFile(marker, []byte("deny\t"+path+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	switch operation {
	case "load":
		if file, ok := readDotEnvFile(path); !ok || file.Values["EXISTING_KEY"] != "old" {
			t.Fatal("credential load failed to repair legacy deny")
		}
	case "revision":
		if revision := CredentialStoreRevision(); !strings.HasPrefix(revision, "sha256:") {
			t.Fatalf("credential revision after legacy deny = %s", revision)
		}
	}

	if _, err := SetCredential("ACL_REPAIR_INTEGRATION_KEY", "new"); err != nil {
		t.Fatalf("SetCredential after legacy deny ACL: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repaired credential file: %v", err)
	}
	contents := string(data)
	if !strings.Contains(contents, "EXISTING_KEY=old") || !strings.Contains(contents, "ACL_REPAIR_INTEGRATION_KEY=new") {
		t.Fatalf("credential contents after repair = %q", contents)
	}
}

func TestReadableCredentialsDoNotWaitForSandboxLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	path := UserCredentialsPath()
	if err := os.WriteFile(path, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Hold one of the sandbox's ancestor mutexes on this thread while another
	// goroutine exercises each credential reader. The lock remains held until
	// all readers finish, so completion cannot depend on a scheduling race.
	digest := sha1.Sum([]byte(strings.ToLower(filepath.Clean(home))))
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Local\windows-sandbox.%x`, digest[:16]))
	if err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mutex, err := windows.CreateMutex(nil, true, name)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(mutex)
	defer windows.ReleaseMutex(mutex)
	done := make(chan error, 1)
	go func() {
		if revision := CredentialStoreRevision(); !strings.HasPrefix(revision, "sha256:") {
			done <- fmt.Errorf("readable store revision = %s", revision)
			return
		}
		if _, ok := readDotEnvFile(path); !ok {
			done <- fmt.Errorf("readDotEnvFile failed while unrelated sandbox lock was held")
			return
		}
		_, err := readCredentialFileLines(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readable credentials waited for an unrelated sandbox lock")
	}
}

// A deny with no sandbox record cannot be attributed to Reasonix, so the
// provenance-checked repair refuses; an explicit save must still succeed by
// resetting the ACL without reading it and keeping the stored values.
func TestCredentialSaveResetsDenyWithoutSandboxRecord(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("REASONIX_HOME", t.TempDir())
	t.Setenv("ACL_RESET_INTEGRATION_KEY", "")
	path := UserCredentialsPath()
	if err := os.WriteFile(path, []byte("EXISTING_KEY=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	trustee := "*" + user.User.Sid.String()
	if output, err := exec.Command("icacls", path, "/deny", trustee+":(RX)").CombinedOutput(); err != nil {
		t.Fatalf("install deny ACL: %v: %s", err, strings.TrimSpace(string(output)))
	}
	t.Cleanup(func() { _ = exec.Command("icacls", path, "/remove:d", trustee, "/C").Run() })
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("deny ACL did not block credential reads")
	}
	if _, err := SetCredential("ACL_RESET_INTEGRATION_KEY", "new"); err != nil {
		t.Fatalf("SetCredential with an unattributed deny: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reset credential file: %v", err)
	}
	if got := string(data); !strings.Contains(got, "EXISTING_KEY=old") || !strings.Contains(got, "ACL_RESET_INTEGRATION_KEY=new") {
		t.Fatalf("credential contents after reset = %q", got)
	}
	if quarantined, _ := filepath.Glob(path + ".locked-*"); len(quarantined) != 0 {
		t.Fatalf("reset path must not quarantine the store: %v", quarantined)
	}
}
