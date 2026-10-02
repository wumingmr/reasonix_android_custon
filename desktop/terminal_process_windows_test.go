//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// A loaded Windows runner can take far longer than the shell's own work to echo
// a marker or reap a ConPTY child. Keep the outer responsiveness bound generous;
// the test still fails if the process never responds.
const conptySmokeWait = 30 * time.Second

func TestWindowsTerminalProcessConPTYSmoke(t *testing.T) {
	available, reason := terminalPlatformAvailable()
	if !available {
		t.Skip(reason)
	}
	commandPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatal(err)
	}
	proc, err := startTerminalProcess(terminalStartSpec{
		command: commandForShellPath(commandPath, "Command Prompt"),
		dir:     t.TempDir(),
		env:     os.Environ(),
		cols:    defaultTerminalColumns,
		rows:    defaultTerminalRows,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()

	if err := proc.Resize(100, 30); err != nil {
		t.Fatalf("resize ConPTY: %v", err)
	}

	const marker = "reasonix-conpty-smoke"
	readResult := make(chan error, 1)
	markerSeen := make(chan struct{})
	go func() {
		var output bytes.Buffer
		buf := make([]byte, 4096)
		seen := false
		for {
			n, readErr := proc.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if !seen && bytes.Contains(output.Bytes(), []byte(marker)) {
					seen = true
					close(markerSeen)
				}
			}
			if readErr != nil {
				if !seen {
					readResult <- fmt.Errorf("read ConPTY output: %w", readErr)
				} else {
					readResult <- nil
				}
				return
			}
		}
	}()

	// Queue exit with the marker command so seeing terminal input echo cannot
	// race a second write. Continue draining output until the process is closed.
	if _, err := proc.Write([]byte("echo " + marker + "\r\nexit\r\n")); err != nil {
		t.Fatalf("write ConPTY command: %v", err)
	}
	select {
	case <-markerSeen:
	case <-time.After(conptySmokeWait):
		t.Fatal("timed out waiting for ConPTY output")
	}

	waitResult := make(chan error, 1)
	go func() {
		_, waitErr := proc.Wait()
		waitResult <- waitErr
	}()
	select {
	case err := <-waitResult:
		if err != nil {
			t.Fatalf("wait for ConPTY exit: %v", err)
		}
	case <-time.After(conptySmokeWait):
		t.Fatal("timed out waiting for ConPTY process exit")
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("close ConPTY process: %v", err)
	}
	select {
	case err := <-readResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(conptySmokeWait):
		t.Fatal("timed out waiting for ConPTY reader to finish")
	}
}
