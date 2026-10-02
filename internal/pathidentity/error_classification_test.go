package pathidentity

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestClassifyPreservesWrappedFilesystemErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind ErrorKind
	}{
		{"missing", os.ErrNotExist, ErrorUnavailable},
		{"missing-syscall", syscall.ENOENT, ErrorUnavailable},
		{"denied", os.ErrPermission, ErrorPermission},
		{"loop", syscall.ELOOP, ErrorLinkLoop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classify("physical", "fixture", fmt.Errorf("open physical path: %w", tc.err))
			var failure *Error
			if !errors.As(err, &failure) || failure.Kind != tc.kind || !errors.Is(err, tc.err) {
				t.Fatalf("lost error kind or cause: %v", err)
			}
		})
	}
}
