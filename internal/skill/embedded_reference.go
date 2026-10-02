package skill

import (
	"fmt"
	"strings"

	"reasonix/internal/skill/builtincontent"
)

func renderEmbeddedReference(sk Skill, reference string) (string, error) {
	if sk.Scope != ScopeBuiltin || !strings.HasPrefix(sk.Path, "(builtin:") ||
		!strings.HasSuffix(sk.Path, ")") {
		return "", fmt.Errorf("skill %q has no embedded references; use its source files for file-backed references", sk.Name)
	}
	source := strings.TrimSuffix(strings.TrimPrefix(sk.Path, "(builtin:"), ")")
	body, err := builtincontent.ReadReference(source, reference)
	if err != nil {
		return "", fmt.Errorf("read_skill reference %q: %w", reference, err)
	}
	return fmt.Sprintf("# Skill reference: %s/%s\n\n%s", sk.Name, reference, body), nil
}
