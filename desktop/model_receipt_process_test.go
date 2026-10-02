package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"reasonix/internal/config"
)

func TestAuditDurableReceiptAcrossProcessRestart(t *testing.T) {
	isolateDesktopUserDirs(t)
	_, newRef := configureSwitchableDefaultModels(t)
	a := NewApp()
	change := ModelSettingsChange{Kind: "preference", Field: "default", Ref: newRef, RequestID: "real-process-restart", ExpectedFingerprint: a.Settings().ModelSettingsFingerprint}
	first := a.ApplyModelSettings(change)
	if !first.Persisted {
		t.Fatalf("initial save failed: %+v", first)
	}
	raw, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestAuditDurableReceiptChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "REASONIX_AUDIT_REQUEST="+path, "REASONIX_AUDIT_HOME="+config.ReasonixHomeDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("same request after real restart failed: %v\n%s", err, output)
	}
}

func TestAuditDurableReceiptChild(t *testing.T) {
	path := os.Getenv("REASONIX_AUDIT_REQUEST")
	if path == "" {
		t.Skip("child process only")
	}
	t.Setenv("REASONIX_HOME", os.Getenv("REASONIX_AUDIT_HOME"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var change ModelSettingsChange
	if err := json.Unmarshal(raw, &change); err != nil {
		t.Fatal(err)
	}
	if _, ok := config.LookupModelSettingsReceipt(change.RequestID); !ok {
		t.Fatal("fixture receipt unavailable in child")
	}
	result := NewApp().ApplyModelSettings(change)
	if !result.Persisted {
		t.Fatalf("restarted process rejected identical request: %+v", result)
	}
}
