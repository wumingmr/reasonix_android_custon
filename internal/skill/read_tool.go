package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

// readSkillTool loads an inline skill body into context without running anything.
type readSkillTool struct {
	store *Store
}

// NewReadSkillTool builds a read-only inline-skill loader so a plan can consult
// playbooks without starting a subagent.
func NewReadSkillTool(store *Store) tool.Tool { return &readSkillTool{store: store} }

func (*readSkillTool) Name() string { return tool.HostReadSkill }

// ReadOnly is true: read_skill only renders an inline skill body, with no
// subagent or side effects.
func (*readSkillTool) ReadOnly() bool { return true }

func (*readSkillTool) Description() string {
	return "Read an inline skill or one of its embedded references without executing work. Pass the bare skill name. Subagent skills require run_skill or their dedicated tool."
}

func (*readSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "name":{"type":"string","description":"Bare inline skill identifier from the skills catalog."},
  "arguments":{"type":"string","description":"Optional task arguments for reading the skill body."},
  "reference":{"type":"string","description":"Optional references/*.md path from an embedded skill's router. Reads only that page; omit to read the skill body."}
},
"required":["name"]
}`)
}

func (t *readSkillTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	name := cleanSkillName(p.Name)
	if name == "" {
		return "", fmt.Errorf("read_skill requires a 'name' argument (got %q, which is just a marker/tag)", p.Name)
	}
	sk, ok := t.store.Read(name)
	if !ok {
		return "", fmt.Errorf("unknown skill %q — available: %s", name, availableNames(t.store))
	}
	if err := t.store.ValidateInvocation(sk); err != nil {
		return "", fmt.Errorf("read_skill: %w", err)
	}
	sk = t.store.Prepare(sk)
	if sk.RunAs == RunSubagent {
		return "", fmt.Errorf("read_skill: skill %q is a subagent and must be executed, not read — use run_skill (or the dedicated %s tool)", name, name)
	}
	if reference := strings.TrimSpace(p.Reference); reference != "" {
		if strings.TrimSpace(p.Arguments) != "" {
			return "", fmt.Errorf("read_skill: reference and arguments are mutually exclusive")
		}
		return renderEmbeddedReference(sk, reference)
	}
	return RenderInvocation(sk, strings.TrimSpace(p.Arguments)), nil
}
