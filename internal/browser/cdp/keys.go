package cdp

import (
	"fmt"
	"strings"
	"unicode"
)

// Modifier bits as the DevTools input domain defines them.
const (
	modAlt   = 1
	modCtrl  = 2
	modMeta  = 4
	modShift = 8
)

// namedKey is one key the model can name in a chord.
type namedKey struct {
	key  string
	code string
	vk   int
	text string
}

var namedKeys = map[string]namedKey{
	"enter":      {"Enter", "Enter", 13, "\r"},
	"return":     {"Enter", "Enter", 13, "\r"},
	"tab":        {"Tab", "Tab", 9, "\t"},
	"escape":     {"Escape", "Escape", 27, ""},
	"esc":        {"Escape", "Escape", 27, ""},
	"space":      {" ", "Space", 32, " "},
	"backspace":  {"Backspace", "Backspace", 8, ""},
	"delete":     {"Delete", "Delete", 46, ""},
	"insert":     {"Insert", "Insert", 45, ""},
	"home":       {"Home", "Home", 36, ""},
	"end":        {"End", "End", 35, ""},
	"pageup":     {"PageUp", "PageUp", 33, ""},
	"pagedown":   {"PageDown", "PageDown", 34, ""},
	"arrowup":    {"ArrowUp", "ArrowUp", 38, ""},
	"arrowdown":  {"ArrowDown", "ArrowDown", 40, ""},
	"arrowleft":  {"ArrowLeft", "ArrowLeft", 37, ""},
	"arrowright": {"ArrowRight", "ArrowRight", 39, ""},
	"up":         {"ArrowUp", "ArrowUp", 38, ""},
	"down":       {"ArrowDown", "ArrowDown", 40, ""},
	"left":       {"ArrowLeft", "ArrowLeft", 37, ""},
	"right":      {"ArrowRight", "ArrowRight", 39, ""},
}

var modifierNames = map[string]int{
	"alt": modAlt, "option": modAlt,
	"ctrl": modCtrl, "control": modCtrl,
	"meta": modMeta, "cmd": modMeta, "command": modMeta, "super": modMeta,
	"shift": modShift,
}

// charEvents types one rune as a key press. Chrome turns a keyDown carrying
// text into keydown, keypress, and input, which is what a controlled React or
// Vue field listens for.
func charEvents(r rune) []map[string]any {
	if r == '\n' || r == '\r' {
		down, up, err := chordEvents("Enter")
		if err != nil {
			return nil
		}
		return []map[string]any{down, up}
	}
	text := string(r)
	vk := 0
	if r < unicode.MaxASCII {
		vk = int(unicode.ToUpper(r))
	}
	return []map[string]any{
		{"type": "keyDown", "text": text, "unmodifiedText": text, "key": text, "windowsVirtualKeyCode": vk, "nativeVirtualKeyCode": vk},
		{"type": "keyUp", "key": text, "windowsVirtualKeyCode": vk, "nativeVirtualKeyCode": vk},
	}
}

// chordEvents parses a key name or '+'-joined chord such as Control+a into the
// key-down and key-up events that reproduce it.
func chordEvents(spec string) (map[string]any, map[string]any, error) {
	parts := strings.Split(spec, "+")
	modifiers := 0
	for i := range len(parts) - 1 {
		name := strings.ToLower(strings.TrimSpace(parts[i]))
		bit, ok := modifierNames[name]
		if !ok {
			return nil, nil, fmt.Errorf("unknown modifier %q in %q; use Alt, Control, Meta, or Shift", parts[i], spec)
		}
		modifiers |= bit
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if last == "" {
		return nil, nil, fmt.Errorf("keys %q names no key", spec)
	}
	key, err := resolveKey(last)
	if err != nil {
		return nil, nil, err
	}
	// A character is produced only when neither Control nor Meta is held; with
	// them the event is a shortcut and must carry no text.
	text := key.text
	if modifiers&(modCtrl|modMeta) != 0 {
		text = ""
	}
	if modifiers&modShift != 0 && len([]rune(key.key)) == 1 {
		upper := strings.ToUpper(key.key)
		key.key = upper
		if text != "" {
			text = upper
		}
	}
	down := map[string]any{
		"type": "keyDown", "key": key.key, "code": key.code, "modifiers": modifiers,
		"windowsVirtualKeyCode": key.vk, "nativeVirtualKeyCode": key.vk,
	}
	if text != "" {
		down["text"] = text
		down["unmodifiedText"] = text
	} else {
		down["type"] = "rawKeyDown"
	}
	up := map[string]any{
		"type": "keyUp", "key": key.key, "code": key.code, "modifiers": modifiers,
		"windowsVirtualKeyCode": key.vk, "nativeVirtualKeyCode": key.vk,
	}
	return down, up, nil
}

// resolveKey maps a key name onto its event fields; a single character is its
// own key, and F1 to F24 follow their virtual key codes.
func resolveKey(name string) (namedKey, error) {
	if k, ok := namedKeys[strings.ToLower(name)]; ok {
		return k, nil
	}
	if n, ok := functionKey(name); ok {
		return n, nil
	}
	runes := []rune(name)
	if len(runes) != 1 {
		return namedKey{}, fmt.Errorf("unknown key %q; use a single character, Enter, Tab, Escape, an arrow, or a function key", name)
	}
	r := runes[0]
	return namedKey{key: name, code: charCode(r), vk: int(unicode.ToUpper(r)), text: name}, nil
}

func functionKey(name string) (namedKey, bool) {
	upper := strings.ToUpper(name)
	if len(upper) < 2 || upper[0] != 'F' {
		return namedKey{}, false
	}
	n := 0
	for _, r := range upper[1:] {
		if r < '0' || r > '9' {
			return namedKey{}, false
		}
		n = n*10 + int(r-'0')
	}
	if n < 1 || n > 24 {
		return namedKey{}, false
	}
	return namedKey{key: upper, code: upper, vk: 111 + n}, true
}

// charCode names the physical key a character sits on, which controlled inputs
// read from KeyboardEvent.code.
func charCode(r rune) string {
	switch {
	case unicode.IsLetter(r) && r < unicode.MaxASCII:
		return "Key" + strings.ToUpper(string(r))
	case r >= '0' && r <= '9':
		return "Digit" + string(r)
	case r == ' ':
		return "Space"
	}
	return ""
}
