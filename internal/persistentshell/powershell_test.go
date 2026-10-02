package persistentshell

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/sandbox"
)

func TestPowerShellControlFrames(t *testing.T) {
	var buffer bytes.Buffer
	want := shellFrame{Version: 1, Kind: "run", ID: "id", Command: "中文\n'quote'"}
	if err := writeShellFrame(&buffer, want); err != nil {
		t.Fatal(err)
	}
	got, err := readShellFrame(&buffer)
	if err != nil || got.Command != want.Command || got.ID != want.ID {
		t.Fatalf("%+v %v", got, err)
	}
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(maxControlFrame+1))
	if _, err := readShellFrame(&buffer); err == nil {
		t.Fatal("oversized control frame accepted")
	}
}

func TestPowerShellPersistentLive(t *testing.T) {
	path := os.Getenv("REASONIX_TEST_PWSH")
	if path == "" {
		path, _ = exec.LookPath("pwsh")
	}
	if path == "" {
		t.Skip("native PowerShell runtime unavailable")
	}
	sh := sandbox.Shell{Kind: sandbox.ShellPowerShell, Path: path}
	m := New()
	m.Retain()
	defer m.Release()
	req := Request{Shell: sh, Argv: InteractiveArgv(sh), Dir: longPath(t.TempDir()), Env: os.Environ(), Timeout: 10 * time.Second}
	run := func(command string) Result { req.Command = command; return m.Run(context.Background(), req) }
	first := run("$myValue = '中文 value'; function MyValue { $myValue }; $env:RX_SAMPLE = 'kept'; Write-Output ready")
	if first.Err != nil || !first.ExitCodeKnown {
		t.Fatalf("first: %+v", first)
	}
	second := run("MyValue; Write-Output $env:RX_SAMPLE; [Console]::Write('no newline')")
	if second.Err != nil || !strings.Contains(second.Output, "中文 value") || !strings.Contains(second.Output, "kept") || !strings.HasSuffix(second.Output, "no newline") {
		t.Fatalf("second: %+v", second)
	}
	failed := run("throw 'expected error'")
	if failed.Err == nil || !failed.ExitCodeKnown || failed.ExitCode == 0 {
		t.Fatalf("failure: %+v", failed)
	}
	if good := run("Write-Output recovered"); good.Err != nil || good.ExitCode != 0 {
		t.Fatalf("recovery: %+v", good)
	}
	location := filepath.Join(req.Dir, "中文 space")
	if err := os.Mkdir(location, 0700); err != nil {
		t.Fatal(err)
	}
	if changed := run("Set-Location '" + strings.ReplaceAll(location, "'", "''") + "'"); changed.Err != nil {
		t.Fatal(changed.Err)
	}
	if got := run("[Console]::Write((Get-Location).Path)"); got.Err != nil || !strings.Contains(got.Output, "中文 space") {
		t.Fatalf("cwd: %+v", got)
	}
	for _, command := range []string{"Write-Error 'cmdlet failure'", "if (", "$LASTEXITCODE = -7"} {
		if got := run(command); got.Err == nil || !got.ExitCodeKnown || got.ExitCode == 0 {
			t.Fatalf("%s: %+v", command, got)
		}
	}
	large := strings.Repeat("长", 10000)
	if got := run("[Console]::Write('" + large + "')"); got.Err != nil || got.Output != large {
		t.Fatalf("long output: bytes=%d err=%v", len(got.Output), got.Err)
	}
	if got := run("[Console]::Write('REASONIX_END_fake:0'); [Console]::Error.Write('stderr')"); got.Err != nil || !strings.Contains(got.Output, "REASONIX_END_fake:0") || !strings.Contains(got.Output, "stderr") {
		t.Fatalf("raw output: %+v", got)
	}
	if got := run("exit 3"); !got.Reset || got.ExitCodeKnown || got.Err == nil {
		t.Fatalf("host exit: %+v", got)
	}
	if got := run("[Console]::Write($env:RX_SAMPLE)"); got.Err != nil || strings.Contains(got.Output, "kept") {
		t.Fatalf("reset environment: %+v", got)
	}
	req.Timeout = 50 * time.Millisecond
	timed := run("Start-Sleep -Seconds 10")
	if !timed.TimedOut || timed.ExitCodeKnown || !timed.Reset {
		t.Fatalf("timeout: %+v", timed)
	}
}
