package pathidentity

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestDarwinIdentityKeyToleratesVolumesThatDoNotAnswer(t *testing.T) {
	const path = "/Volumes/remote/Proj/Café.go"
	fixed := func(value string, err error) func(string) (string, error) {
		return func(string) (string, error) { return value, err }
	}
	pathconf := func(value int, err error) func(string) (int, error) {
		return func(string) (int, error) { return value, err }
	}
	cases := []struct {
		name   string
		volume darwinVolume
		want   string
	}{
		{"fuse pathconf EINVAL", darwinVolume{fixed("macfuse", nil), pathconf(-1, syscall.EINVAL)}, path},
		{"fuse pathconf ENOTSUP", darwinVolume{fixed("macfuse", nil), pathconf(-1, fmt.Errorf("pathconf: %w", syscall.ENOTSUP))}, path},
		{"statfs failure", darwinVolume{fixed("", syscall.EIO), pathconf(1, nil)}, path},
		{"case-insensitive apfs", darwinVolume{fixed("apfs", nil), pathconf(0, nil)}, "/volumes/remote/proj/café.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := darwinIdentityKeyBy(path, "/Volumes/remote", tc.volume)
			if err != nil {
				t.Fatalf("identity key failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("identity key = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDarwinIdentityKeyKeepsOtherPathconfFailures(t *testing.T) {
	volume := darwinVolume{
		fsType:        func(string) (string, error) { return "apfs", nil },
		caseSensitive: func(string) (int, error) { return -1, syscall.EACCES },
	}
	if _, err := darwinIdentityKeyBy("/p", "/", volume); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("error = %v, want EACCES", err)
	}
}
