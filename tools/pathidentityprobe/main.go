// pathidentityprobe compares path metadata without loading Reasonix configuration
// or acquiring locks. Its only write is a new diagnostic report.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"reasonix/internal/pathidentity"
)

var sourceCommit = "development"

type report struct {
	Schema          int           `json:"schema"`
	Started         string        `json:"started_utc"`
	GoVersion       string        `json:"go_version"`
	Platform        string        `json:"platform"`
	SourceCommit    string        `json:"source_commit"`
	IdentityVersion int           `json:"identity_version"`
	UserHome        string        `json:"os_user_home"`
	Notes           []string      `json:"notes"`
	Paths           []observation `json:"paths"`
	TimedOut        bool          `json:"timed_out"`
	Incomplete      string        `json:"incomplete_path,omitempty"`
}

func main() {
	out := flag.String("out", "", "new report file; default: beside this executable")
	timeout := flag.Duration("timeout", 60*time.Second, "total collection deadline")
	flag.Parse()
	if *timeout <= 0 || *timeout > 5*time.Minute {
		fmt.Fprintln(os.Stderr, "timeout must be between 0 and 5m")
		os.Exit(2)
	}
	if err := run(*out, *timeout, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(output string, timeout time.Duration, paths []string) error {
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve OS user: %w", err)
	}
	if output == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		output = filepath.Join(filepath.Dir(exe), "reasonix-path-probe-"+time.Now().UTC().Format("20060102-150405.000000000")+".json")
	}
	// Exclusive creation refuses to overwrite any existing file or final symlink.
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create new report: %w", err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result := collect(ctx, current, paths)
	raw, err := encodeRedacted(result, current)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if result.TimedOut {
		fmt.Println("Collection timed out; the partial report records the unfinished path.")
	}
	fmt.Println("Report:", output)
	fmt.Println("No settings, credentials, lock files or sessions were changed.")
	return nil
}

func collect(ctx context.Context, current *user.User, extra []string) report {
	r := report{Schema: 1, Started: time.Now().UTC().Format(time.RFC3339),
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		SourceCommit: sourceCommit, IdentityVersion: pathidentity.Version, UserHome: current.HomeDir,
		Notes: []string{"Metadata only; no config loading, content reads, lock acquisition or network requests.",
			"Path identities are from this build's source resolver; filepath_eval_symlinks remains the independent Go baseline.",
			"A missing synthetic child is intentional. Native resolution is diagnostic, not an applied fix."},
		Paths: []observation{}}
	plan := make(chan []string, 1)
	go func() { plan <- defaultPaths(current, extra) }()
	var queue []string
	select {
	case queue = <-plan:
	case <-ctx.Done():
		r.TimedOut, r.Incomplete = true, "enumerating configured lock directory"
		return r
	}
	seen := map[string]bool{}
	for len(queue) > 0 && len(r.Paths) < 300 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true
		one := make(chan observation, 1)
		go func() { one <- inspect(path) }()
		select {
		case got := <-one:
			r.Paths = append(r.Paths, got)
			// Independently inspect the native target: alias-only failures must
			// be distinguished from failures on the target itself.
			if got.Native.Path != "" && !seen[got.Native.Path] {
				queue = append(queue, got.Native.Path)
			}
		case <-ctx.Done():
			r.TimedOut, r.Incomplete = true, path
			return r
		}
	}
	if len(queue) > 0 {
		r.Notes = append(r.Notes, "Stopped at the 300-path limit.")
	}
	return r
}

func encodeRedacted(r report, current *user.User) ([]byte, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.MarshalIndent(redactValue(value, current), "", "  ")
}
