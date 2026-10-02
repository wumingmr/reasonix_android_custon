//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Bash's Readline must accept eight-bit terminal input even when the user
// deliberately selects LC_ALL=C. Preserve their inputrc via include, then set
// only the byte-transport options. Unlike machine commands, raw keystrokes
// cannot be encoded as shell literals.
func terminalInputEnvironment(spec terminalStartSpec) ([]string, func(), error) {
	noop := func() {}
	if filepath.Base(spec.command.path) != "bash" {
		return spec.env, noop, nil
	}
	values := make(map[string]string)
	for _, item := range spec.env {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	source := values["INPUTRC"]
	if source == "" {
		source = filepath.Join(values["HOME"], ".inputrc")
		if _, err := os.Stat(source); err != nil {
			source = "/etc/inputrc"
		}
	}
	// Readline expands tilde-prefixed include paths itself, just as it does
	// for INPUTRC. Resolve ordinary relative paths against the terminal cwd.
	if !filepath.IsAbs(source) && !strings.HasPrefix(source, "~") {
		source = filepath.Join(spec.dir, source)
	}
	if strings.ContainsAny(source, "\r\n") {
		return nil, noop, fmt.Errorf("terminal inputrc path contains a line break")
	}
	file, err := os.CreateTemp("", "reasonix-terminal-inputrc-*")
	if err != nil {
		return nil, noop, err
	}
	var once sync.Once
	cleanup := func() { once.Do(func() { _ = os.Remove(file.Name()) }) }
	_, err = fmt.Fprintf(file, "$include %s\nset convert-meta off\nset input-meta on\nset output-meta on\n", source)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	env := make([]string, 0, len(spec.env)+1)
	for _, item := range spec.env {
		if !strings.HasPrefix(item, "INPUTRC=") {
			env = append(env, item)
		}
	}
	return append(env, "INPUTRC="+file.Name()), cleanup, nil
}
