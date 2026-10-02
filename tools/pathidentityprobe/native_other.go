//go:build !windows

package main

import "fmt"

func nativePath(string) outcome {
	return failureOutcome(fmt.Errorf("Windows native probe is unavailable on this platform"))
}
