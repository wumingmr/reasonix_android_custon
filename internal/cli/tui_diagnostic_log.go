package cli

import (
	"os"
	"strings"
	"sync"
)

// rotatingDiagnosticLog keeps the newest diagnostics of a long session within
// limit bytes on disk: the live file holds at most half, and when it fills it
// replaces a single ".prev.log" sibling. What precedes a crash is what matters.
type rotatingDiagnosticLog struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	segment int64
	written int64
}

func newRotatingDiagnosticLog(file *os.File, limit int64) *rotatingDiagnosticLog {
	return &rotatingDiagnosticLog{file: file, path: file.Name(), segment: max(limit/2, 1)}
}

func (l *rotatingDiagnosticLog) previousPath() string {
	return strings.TrimSuffix(l.path, ".log") + ".prev.log"
}

func (l *rotatingDiagnosticLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := len(p)
	if l.file == nil || total == 0 {
		return total, nil
	}
	if int64(len(p)) > l.segment {
		p = p[int64(len(p))-l.segment:]
	}
	if l.written > 0 && l.written+int64(len(p)) > l.segment && !l.rotateLocked() {
		return total, nil
	}
	n, err := l.file.Write(p)
	l.written += int64(n)
	if err != nil {
		l.closeLocked()
	}
	return total, nil
}

// rotateLocked closes before renaming because Windows refuses to rename an
// open file.
func (l *rotatingDiagnosticLog) rotateLocked() bool {
	_ = l.file.Sync()
	_ = l.file.Close()
	l.file = nil
	if err := os.Rename(l.path, l.previousPath()); err != nil {
		return false
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	l.file = file
	l.written = 0
	return true
}

func (l *rotatingDiagnosticLog) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	return l.file.Sync()
}

func (l *rotatingDiagnosticLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
	return nil
}

func (l *rotatingDiagnosticLog) closeLocked() {
	if l.file == nil {
		return
	}
	_ = l.file.Sync()
	_ = l.file.Close()
	l.file = nil
}
