package evidence

import (
	pathpkg "path"
	"strings"
)

// normalizeTargetPath provides stable cross-platform receipt target identities.
func normalizeTargetPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	if value == "" {
		return ""
	}
	cleaned := pathpkg.Clean(value)
	// path.Clean treats a Windows drive prefix as an ordinary path component
	// and removes the root slash from "C:/". Restore it so drive roots remain
	// absolute and can safely relativize paths in cross-platform evidence.
	if len(value) == 3 && value[1] == ':' && value[2] == '/' && len(cleaned) == 2 && cleaned[1] == ':' {
		return cleaned + "/"
	}
	return cleaned
}
