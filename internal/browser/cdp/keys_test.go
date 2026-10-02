package cdp

import (
	"strings"
	"testing"
)

func TestChordEventsCarryModifiersAndSuppressTextForShortcuts(t *testing.T) {
	tests := []struct {
		spec      string
		key       string
		code      string
		vk        int
		modifiers int
		downType  string
		text      string
	}{
		{"Enter", "Enter", "Enter", 13, 0, "keyDown", "\r"},
		{"Escape", "Escape", "Escape", 27, 0, "rawKeyDown", ""},
		{"Control+a", "a", "KeyA", 65, modCtrl, "rawKeyDown", ""},
		{"Shift+a", "A", "KeyA", 65, modShift, "keyDown", "A"},
		{"Meta+Shift+p", "P", "KeyP", 80, modMeta | modShift, "rawKeyDown", ""},
		{"ArrowDown", "ArrowDown", "ArrowDown", 40, 0, "rawKeyDown", ""},
		{"F5", "F5", "F5", 116, 0, "rawKeyDown", ""},
	}
	for _, tc := range tests {
		down, up, err := chordEvents(tc.spec)
		if err != nil {
			t.Fatalf("chordEvents(%q): %v", tc.spec, err)
		}
		if down["key"] != tc.key || down["code"] != tc.code {
			t.Fatalf("chordEvents(%q) key/code = %v/%v, want %v/%v", tc.spec, down["key"], down["code"], tc.key, tc.code)
		}
		if down["windowsVirtualKeyCode"] != tc.vk || down["modifiers"] != tc.modifiers {
			t.Fatalf("chordEvents(%q) vk/modifiers = %v/%v, want %d/%d", tc.spec, down["windowsVirtualKeyCode"], down["modifiers"], tc.vk, tc.modifiers)
		}
		if down["type"] != tc.downType {
			t.Fatalf("chordEvents(%q) type = %v, want %v", tc.spec, down["type"], tc.downType)
		}
		if text, _ := down["text"].(string); text != tc.text {
			t.Fatalf("chordEvents(%q) text = %q, want %q", tc.spec, text, tc.text)
		}
		if up["type"] != "keyUp" || up["key"] != tc.key {
			t.Fatalf("chordEvents(%q) key-up = %v", tc.spec, up)
		}
	}
}

func TestChordEventsRejectUnknownNames(t *testing.T) {
	for _, spec := range []string{"Hyper+a", "NoSuchKey", "Control+", ""} {
		if _, _, err := chordEvents(spec); err == nil {
			t.Fatalf("chordEvents(%q) accepted an unusable chord", spec)
		}
	}
}

func TestCharEventsTypeOneRune(t *testing.T) {
	events := charEvents('x')
	if len(events) != 2 {
		t.Fatalf("charEvents emitted %d events, want a down and an up", len(events))
	}
	if events[0]["text"] != "x" || events[0]["type"] != "keyDown" {
		t.Fatalf("key-down = %v, want a keyDown carrying the character", events[0])
	}
	if events[0]["windowsVirtualKeyCode"] != int('X') {
		t.Fatalf("virtual key code = %v, want the upper-case code", events[0]["windowsVirtualKeyCode"])
	}
	if newline := charEvents('\n'); len(newline) != 2 || newline[0]["key"] != "Enter" {
		t.Fatalf("a newline must type Enter, got %v", newline)
	}
}

func TestSafeFilenameKeepsDownloadsInTheirDirectory(t *testing.T) {
	tests := map[string]string{
		"report.csv":        "report.csv",
		"../../etc/passwd":  "passwd",
		`..\..\windows.ini`: "windows.ini",
		"a/b/c.txt":         "c.txt",
		"weird:name?.txt":   "weird_name_.txt",
		"..":                "",
		"   ":               "",
	}
	for input, want := range tests {
		if got := safeFilename(input); got != want {
			t.Fatalf("safeFilename(%q) = %q, want %q", input, got, want)
		}
	}
	if got := safeFilename(strings.Repeat("n", 400) + ".txt"); len(got) != 180 {
		t.Fatalf("safeFilename truncated to %d characters, want 180", len(got))
	}
}
