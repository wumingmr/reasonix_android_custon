//go:build darwin

package filelock

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalLockPathKeepsLegacyDarwinCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MixedCase.lock")
	got, err := canonicalLockPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "MixedCase.lock") {
		t.Fatalf("canonical lock path = %q, want exact legacy case", got)
	}
	alias := filepath.Join(filepath.Dir(got), "mixedcase.lock")
	release, err := TryAcquireModeWithKey(got, strings.ToLower(got), ModeExclusive)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if aliasRelease, err := TryAcquireModeWithKey(alias, strings.ToLower(alias), ModeExclusive); !errors.Is(err, ErrHeld) {
		if aliasRelease != nil {
			aliasRelease()
		}
		t.Fatalf("local registry did not share explicit alias identity: %q / %q: %v", got, alias, err)
	}
}
