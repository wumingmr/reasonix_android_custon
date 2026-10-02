package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/packagegrant"
)

func TestStripPackageGrantsOnlyAnswersForAnApplication(t *testing.T) {
	dir := t.TempDir()
	for name, app := range map[string]string{
		"no application":         "",
		"a path that is nothing": filepath.Join(dir, "missing.exe"),
		"a directory":            dir,
	} {
		var out, logs bytes.Buffer
		if code := stripPackageGrants(&out, &logs, app); code != 2 || out.Len() != 0 {
			t.Errorf("%s: code=%d out=%q", name, code, out.String())
		}
	}
}

// The shell reads this line as structure, so both lists are present even when
// empty: a null would be a third answer it has to guess about.
func TestStripPackageGrantsReportsOneLineOfJSON(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Reasonix.exe")
	if err := os.WriteFile(app, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if code := stripPackageGrants(&out, &logs, app); code != 0 {
		t.Fatalf("code=%d logs=%s", code, logs.String())
	}
	var report packagegrant.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("not JSON: %q", out.String())
	}
	if len(report.Stripped)+len(report.Refused)+len(report.Unread) != 0 {
		t.Fatalf("a clean tree reported %+v from %q", report, out.String())
	}
	for _, list := range []string{`"stripped":[]`, `"refused":[]`, `"unread":[]`} {
		if !bytes.Contains(out.Bytes(), []byte(list)) {
			t.Fatalf("%s was not written as an empty list: %q", list, out.String())
		}
	}
}

func TestStripPackageGrantsRequestReadsBothSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--strip-package-grants", "--app", `C:\R\app\Reasonix.exe`},
		{"-strip-package-grants", "-app=C:\\R\\app\\Reasonix.exe"},
	} {
		app, ok := stripPackageGrantsRequest(args)
		if !ok || app != `C:\R\app\Reasonix.exe` {
			t.Fatalf("%q: app=%q ok=%v", args, app, ok)
		}
	}
	if _, ok := stripPackageGrantsRequest([]string{"--host-rpc", "--app", "x"}); ok {
		t.Fatal("a launch without the flag was read as a grant pass")
	}
}
