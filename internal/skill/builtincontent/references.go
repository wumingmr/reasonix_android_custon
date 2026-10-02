package builtincontent

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// ReadReference reads a Markdown reference inside one embedded skill package.
func ReadReference(skillPath, reference string) (string, error) {
	if !fs.ValidPath(skillPath) || path.Base(skillPath) != "SKILL.md" {
		return "", fmt.Errorf("invalid embedded skill path")
	}
	if !fs.ValidPath(reference) || !strings.HasPrefix(reference, "references/") ||
		!strings.HasSuffix(reference, ".md") || strings.Contains(reference, "\\") {
		return "", fmt.Errorf("reference must name a relative references/*.md file")
	}
	if _, err := files.ReadFile(skillPath); err != nil {
		return "", err
	}
	content, err := files.ReadFile(path.Join(path.Dir(skillPath), reference))
	if err != nil {
		return "", err
	}
	return string(content), nil
}
