// Command desktopinventory enumerates every desktop shell entry point the
// Electron migration must account for and assigns each one a migration class.
// It reads the desktop Go package, the frontend sources, the packaging script
// and the CI workflows, then writes docs/desktop-migration/{INVENTORY.md,
// inventory.json}. -check fails when migration entries drift or remain
// unclassified; navigation-only source line changes do not block qualification.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "docs/desktop-migration", "output directory (relative to root)")
	check := flag.Bool("check", false, "verify the checked-in inventory is current instead of writing it")
	flag.Parse()

	inv, err := build(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "desktopinventory:", err)
		os.Exit(2)
	}
	if unclassified := inv.unclassified(); len(unclassified) > 0 {
		for _, u := range unclassified {
			fmt.Fprintf(os.Stderr, "unclassified: %s\n", u)
		}
		os.Exit(2)
	}
	md, js, err := render(inv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "desktopinventory:", err)
		os.Exit(2)
	}
	dir := filepath.Join(*root, *out)
	mdPath := filepath.Join(dir, "INVENTORY.md")
	jsPath := filepath.Join(dir, "inventory.json")
	if *check {
		stale := false
		for path, want := range map[string][]byte{mdPath: md, jsPath: js} {
			have, err := os.ReadFile(path)
			if err != nil || !sameInventory(have, want) {
				fmt.Fprintf(os.Stderr, "stale: %s (run: go run ./tools/desktopinventory)\n", path)
				stale = true
			}
		}
		if stale {
			os.Exit(1)
		}
		fmt.Printf("desktop inventory current: %d entries\n", inv.count())
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "desktopinventory:", err)
		os.Exit(2)
	}
	for path, data := range map[string][]byte{mdPath: md, jsPath: js} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "desktopinventory:", err)
			os.Exit(2)
		}
	}
	fmt.Printf("wrote %s and %s (%d entries)\n", mdPath, jsPath, inv.count())
}

// Source line numbers are navigation hints, not migration contracts. Moving
// unrelated code must not fail qualification; paths, entries, classifications,
// ownership, signatures and all other generated content remain exact.
var sourceLineHint = regexp.MustCompile(`((?:desktop|scripts|\.github)/[^\s"` + "`" + `|<>]+\.(?:go|[cm]?[jt]sx?|sh|ya?ml)):[0-9]+\b`)

func sameInventory(have, want []byte) bool {
	return bytes.Equal(sourceLineHint.ReplaceAll(have, []byte("${1}")), sourceLineHint.ReplaceAll(want, []byte("${1}")))
}
