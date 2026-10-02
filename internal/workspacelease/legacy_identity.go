package workspacelease

import (
	"fmt"
	"os"
	"path/filepath"
)

// Freeze the pre-native-resolver workspace lock spelling. In particular, older
// Windows builds can retain a junction alias even when the new physical root is
// on another drive. Deriving both keys from the new physical root would silently
// stop taking the lock held by those existing writers.
func legacyWorkspaceIdentity(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = filepath.Clean(resolved)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve legacy workspace identity: %w", err)
	}
	return compatibilityIdentityPath(nearestGitWorktreeRoot(absolute)), nil
}
