package secrets

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestProcessEnvMissingLocaleUsesUTF8(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX locale")
	}
	for _, key := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		t.Setenv(key, "")
	}
	env := ProcessEnv()
	if !strings.Contains(strings.Join(env, "\n"), "LANG=") {
		t.Fatal("missing LANG")
	}
	cmd := exec.Command("sed", "s/./X/g")
	cmd.Env = env
	cmd.Stdin = strings.NewReader("中文\n")
	got, err := cmd.Output()
	if err != nil || string(got) != "XX\n" {
		t.Fatalf("UTF-8 character semantics: %q %v", got, err)
	}
	t.Setenv("LC_ALL", "C")
	cmd = exec.Command("sed", "s/./X/g")
	cmd.Env = ProcessEnv()
	cmd.Stdin = strings.NewReader("中文\n")
	got, err = cmd.Output()
	if err != nil || string(got) != "XXXXXX\n" {
		t.Fatalf("explicit byte semantics: %q %v", got, err)
	}
}
