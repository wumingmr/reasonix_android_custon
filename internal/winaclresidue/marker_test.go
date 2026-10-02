package winaclresidue

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkerOwnerPIDParsesRetiredBackendNames(t *testing.T) {
	for name, want := range map[string]string{
		"1234.txt":              "1234",
		"1234-deadbeef.txt":     "1234",
		"1234-1700000000-2.txt": "1234",
		"1234":                  "",
		"-abc.txt":              "",
		"notapid-1.txt":         "",
		"99999999999.txt":       "",
	} {
		got, ok := markerOwnerPID(name)
		if (want == "") == ok || got != want {
			t.Fatalf("markerOwnerPID(%q) = %q,%v want %q", name, got, ok, want)
		}
	}
}

func TestReadResidueMarkerSkipsCorruptLines(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "42.txt")
	body := "deny\tC:\\Users\\me\\.env\r\n" +
		"grant\tC:\\Tools\\with space\\bin\n" +
		"deny C:\\no-tab\n" +
		"unknown\tC:\\kind\n" +
		"deny\t\n" +
		"\n"
	if err := os.WriteFile(marker, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readResidueMarker(marker)
	if len(got) != 2 || got[0] != (residueEntry{residueDeny, `C:\Users\me\.env`}) || got[1] != (residueEntry{residueGrant, `C:\Tools\with space\bin`}) {
		t.Fatalf("readResidueMarker = %+v", got)
	}
	if got := readResidueMarker(filepath.Join(t.TempDir(), "missing.txt")); got != nil {
		t.Fatalf("missing marker parsed as %+v", got)
	}
}

func TestIsWindowsSystemRootUsesEnvironmentRoots(t *testing.T) {
	t.Setenv("SystemRoot", `C:\Windows`)
	t.Setenv("ProgramFiles", `C:\Program Files`)
	for path, want := range map[string]bool{
		`C:\Windows\System32`:      true,
		`c:\windows`:               true,
		`C:\Program Files\Git\bin`: true,
		`C:\WindowsOld\System32`:   false,
		`C:\Users\me\project`:      false,
		`C:\Program FilesX\tool`:   false,
	} {
		if got := isWindowsSystemRoot(path); got != want {
			t.Fatalf("isWindowsSystemRoot(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestDedupeSIDStringsDropsEmptyAndRepeats(t *testing.T) {
	got := dedupeSIDStrings([]string{"S-1-15-2-1", "", "S-1-15-2-2", "S-1-15-2-1", "S-1-5-21-1"})
	if len(got) != 3 || got[0] != "S-1-15-2-1" || got[1] != "S-1-15-2-2" || got[2] != "S-1-5-21-1" {
		t.Fatalf("dedupeSIDStrings = %v", got)
	}
}
