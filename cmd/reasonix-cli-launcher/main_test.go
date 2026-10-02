package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("REASONIX_CLI_LAUNCHER_HELPER") != "1" {
		return
	}
	raw, _ := os.ReadFile("/dev/stdin")
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	fmt.Printf("args=%s input=%s", strings.Join(args, "|"), raw)
	os.Exit(23)
}

func TestRunCLIPreservesArgumentsStreamsAndExitCode(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture uses a shell executable path")
	}
	root := t.TempDir()
	version := "v1.2.3"
	dir := filepath.Join(root, "versions", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "reasonix-cli.exe")
	script := "#!/bin/sh\nREASONIX_CLI_LAUNCHER_HELPER=1 exec \"" + os.Args[0] + "\" -test.run=TestCLIHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(target, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pointer := `{"schemaVersion":1,"activeVersion":"` + version + `","activeDir":"versions/` + version + `"}`
	if err := os.WriteFile(filepath.Join(root, "current.json"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "reasonix-cli.exe")
	if err := os.WriteFile(launcher, []byte("launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runCLI(launcher, []string{"two words", "中文"}, strings.NewReader("pipe"), &stdout, &stderr)
	if code != 23 || stdout.String() != "args=two words|中文 input=pipe" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunCLIFailsClosedOnInvalidPointer(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, "reasonix-cli.exe")
	if err := os.WriteFile(launcher, []byte("launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "current.json"), []byte(`{"activeDir":"../../escape"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := runCLI(launcher, nil, nil, &bytes.Buffer{}, &stderr); code == 0 || !strings.Contains(stderr.String(), "resolve active CLI") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
