//go:build !windows

package cli

import "os"

func newConsoleGlyphFit(*os.File) *glyphFit { return nil }
