//go:build !windows

package provider

import "syscall"

var closedConnectionErrnos = []error{syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE}
