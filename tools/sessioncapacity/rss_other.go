//go:build !unix

package main

func processPeakRSSBytes() uint64 { return 0 }
