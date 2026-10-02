//go:build unix

package main

import (
	"runtime"
	"syscall"
)

func processPeakRSSBytes() uint64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil || usage.Maxrss < 0 {
		return 0
	}
	// Darwin reports bytes; Linux and the other supported Unix targets report
	// KiB. This is the process high-water mark, not a sampled Go heap value.
	if runtime.GOOS == "darwin" {
		return uint64(usage.Maxrss)
	}
	return uint64(usage.Maxrss) * 1024
}
