package provider

import "syscall"

// Winsock reports a dropped peer with its own codes, which do not match the
// POSIX-style syscall.ECONNRESET under errors.Is.
var closedConnectionErrnos = []error{syscall.WSAECONNRESET, syscall.WSAECONNABORTED, syscall.ECONNRESET, syscall.ECONNABORTED}
