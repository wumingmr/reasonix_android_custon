package instruction

import (
	"strings"
	"testing"
)

func TestProjectCheckSectionsRemainModelContext(t *testing.T) {
	for _, heading := range []string{"Reasonix host checks", "reasonix HOST checks", "Project verification"} {
		body := "## " + heading + "\n- verify: go test ./...\nAlways inspect actual failures."
		text := Block([]Document{{Path: "AGENTS.md", Scope: ScopeProject, Body: body}})
		if !strings.Contains(text, body) {
			t.Fatalf("project instruction text was dropped or rewritten: %s", text)
		}
	}
}
