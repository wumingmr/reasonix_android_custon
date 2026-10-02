// Package instanceidentity owns the Desktop data-home identity across processes.
package instanceidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"reasonix/internal/pathidentity"
)

const Prefix = "com.reasonix.desktop"
const UpdateEnvironmentKey = "REASONIX_UPDATE_INSTANCE_ID"

var trayNamespace = uuid.MustParse("af8b2b6e-cf17-43b9-afb9-b0bf2695d8ac")

func CanonicalHome(home string) string {
	identity, err := ResolveHome(home)
	if err != nil {
		return ""
	}
	return identity.Key
}

func ResolveHome(home string) (pathidentity.Identity, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return pathidentity.Identity{}, errors.New("Reasonix data home is empty")
	}
	baseDir := ""
	if !filepath.IsAbs(home) {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return pathidentity.Identity{}, err
		}
	}
	return pathidentity.Resolve(home, pathidentity.Options{BaseDir: baseDir, FollowLeaf: true})
}

func AccessHome(home string) string {
	identity, err := ResolveHome(home)
	if err != nil {
		return ""
	}
	return identity.AccessPath
}

func Digest(home string) string {
	identity, err := ResolveHome(home)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(identity.Key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ForHome(home string) string {
	home = CanonicalHome(home)
	if home == "" {
		return Prefix
	}
	sum := sha256.Sum256([]byte(home))
	return Prefix + "." + hex.EncodeToString(sum[:8])
}

func Valid(id string) bool {
	suffix, ok := strings.CutPrefix(id, Prefix+".")
	if !ok || len(suffix) != 16 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil && suffix == strings.ToLower(suffix)
}

func TrayGUID(id string) string { return "{" + uuid.NewSHA1(trayNamespace, []byte(id)).String() + "}" }

// UpdateEnvironment freezes relative data homes before the helper changes cwd.
func UpdateEnvironment(base []string, home string) []string {
	home = AccessHome(home)
	env := make([]string, 0, len(base)+2)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "REASONIX_HOME") && !strings.EqualFold(key, UpdateEnvironmentKey) {
			env = append(env, entry)
		}
	}
	return append(env, "REASONIX_HOME="+home, UpdateEnvironmentKey+"="+ForHome(home))
}

func UpdateID() string { return os.Getenv(UpdateEnvironmentKey) }
