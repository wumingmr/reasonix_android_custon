//go:build windows

package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/jobs"
	"reasonix/internal/sandbox"
)

func TestWindowsWorkspaceWriteBackgroundHTTPJobLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	powerShell := ""
	for _, name := range []string{"pwsh", "powershell"} {
		if found, lookupErr := exec.LookPath(name); lookupErr == nil {
			powerShell = found
			break
		}
	}
	if powerShell == "" {
		t.Skip("PowerShell unavailable")
	}

	workspace := t.TempDir()
	sh := sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: powerShell}
	b := bash{
		name:    "pwsh",
		shell:   sh,
		workDir: workspace,
		sb: sandbox.Spec{
			Mode:       "enforce",
			WriteRoots: []string{workspace},
			Network:    true,
			Shell:      sh,
		},
	}
	manager := jobs.NewManager(event.Discard)
	defer manager.Close()
	ctx := jobs.WithSession(jobs.WithManager(context.Background(), manager), "windows-http")
	ctx = sandbox.WithPermissionPreset(ctx, "workspace-write")

	js := `const http=require("http");const s=http.createServer((q,r)=>{r.end("openmaic-ok")});s.listen(0,"127.0.0.1",()=>console.log("READY "+s.address().port));`
	encoded := base64.StdEncoding.EncodeToString([]byte(js))
	command := fmt.Sprintf("$code=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s')); & '%s' -e $code", encoded, strings.ReplaceAll(node, "'", "''"))
	args, _ := json.Marshal(bashParams{Command: command, Description: "Start mock OpenMAIC service", RunInBackground: true})
	result, err := b.ExecuteDetailed(ctx, args)
	if err != nil {
		t.Fatalf("start background PowerShell: %v", err)
	}
	jobID := regexp.MustCompile(`pwsh-\d+`).FindString(result.Output)
	if jobID == "" {
		t.Fatalf("background result did not return pwsh job id: %q", result.Output)
	}

	ready := regexp.MustCompile(`READY\s+(\d+)`)
	var output strings.Builder
	deadline := time.Now().Add(15 * time.Second)
	port := 0
	for time.Now().Before(deadline) {
		chunk, _, found := manager.OutputForSession("windows-http", jobID)
		if !found {
			t.Fatalf("job %q disappeared", jobID)
		}
		output.WriteString(chunk)
		if match := ready.FindStringSubmatch(output.String()); len(match) == 2 {
			port, _ = strconv.Atoi(match[1])
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if port == 0 {
		t.Fatalf("background service never became ready: %q", output.String())
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("health check %s: %v", url, err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %s", response.Status)
	}
	if _, err := (jobKill{}).Execute(ctx, json.RawMessage(`{"job_id":"`+jobID+`","reason":"test complete"}`)); err != nil {
		t.Fatalf("job_kill: %v", err)
	}
	if results := manager.WaitForSession(ctx, "windows-http", []string{jobID}, 10); len(results) != 1 || results[0].Status != jobs.Killed {
		t.Fatalf("job did not settle killed: %+v", results)
	}
	for stopDeadline := time.Now().Add(5 * time.Second); ; {
		if response, getErr := client.Get(url); getErr != nil {
			break
		} else {
			_ = response.Body.Close()
		}
		if time.Now().After(stopDeadline) {
			t.Fatal("HTTP child process survived job_kill")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
