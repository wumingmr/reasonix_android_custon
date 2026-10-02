package skill

import (
	"strings"

	"reasonix/internal/textutil"
)

// IndexMaxChars caps the session-context skills catalog; bodies never enter it.
const IndexMaxChars = 4000

const missingDescPlaceholder = `(no description — frontmatter is missing a "description:" line; tell the user to add one)`

// indexHeader is the cache-stable invocation policy. The dynamic catalog is
// delivered independently in the latest host-generated session-context.
const indexHeader = "# Skills — playbooks you can invoke\n\n" +
	"The latest host-generated `<session-context>` contains the skills catalog. Use a skill when the user names it or its guidance materially helps the task; keyword overlap alone is insufficient. Load only relevant references. Call `run_skill` with the bare name and concrete task in `arguments`, or use the dedicated tool when available. Inline skills return instructions; `[🧬 subagent]` skills execute in isolation and return a final answer. Skill instructions do not expand the user's authorization. The user can also invoke `/<name>`. Discover omitted skills with `use_capability` action=search."

const readOnlyIndexHeader = "# Skills — read-only playbooks you can invoke\n\n" +
	"The latest host-generated `<session-context>` contains the current one-line catalog for this narrow read-only skill surface. Call `read_only_skill({ name: \"<skill-name>\", arguments: \"<task>\" })` — `name` is JUST the identifier, NOT the `[🧬 subagent]` tag. Inline skills are loaded into context. Skills tagged `[🧬 subagent]` run in an isolated ephemeral read-only subagent with only read-only research tools and safe foreground bash; no writes, installers, memory mutation, continuation/fork, background jobs, or writer-capable delegation are available. Read-only nested delegation may be available until max_subagent_depth is reached."

// InvocationPolicyBlock is the stable executor policy without catalog entries.
func InvocationPolicyBlock() string { return indexHeader }

// ReadOnlyInvocationPolicyBlock is the stable planner policy without catalog entries.
func ReadOnlyInvocationPolicyBlock() string { return readOnlyIndexHeader }

// CatalogBlock renders only dynamic names, descriptions, and run tags.
func CatalogBlock(skills []Skill) string { return catalogBlock(skills) }

// ReadOnlyCatalogBlock currently has the same entries as CatalogBlock; the
// planner-specific invocation semantics remain in ReadOnlyInvocationPolicyBlock.
func ReadOnlyCatalogBlock(skills []Skill) string { return catalogBlock(skills) }

// IndexBlock renders the system/tool-result skills listing without attaching it
// to a base prompt. Only names + descriptions (+ a subagent tag) are listed;
// bodies load on demand via run_skill.
func IndexBlock(skills []Skill) string {
	return indexBlockWithHeader(indexHeader, skills)
}

// ReadOnlyIndexBlock renders the same listing with read_only_skill-specific
// invocation guidance for token-economy plan-mode connections.
func ReadOnlyIndexBlock(skills []Skill) string {
	return indexBlockWithHeader(readOnlyIndexHeader, skills)
}

func indexBlockWithHeader(header string, skills []Skill) string {
	catalog := catalogBlock(skills)
	if catalog == "" {
		return ""
	}
	return header + "\n\n" + catalog
}

func catalogBlock(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	visible := make([]Skill, 0, len(skills))
	for _, sk := range skills {
		// Manual-invocation skills (e.g. user-authored subagent profiles) stay
		// invocable by name (/<name>, run_skill) but must never enter the
		// session-context catalog the model scans for candidates on its own
		// initiative.
		if sk.Invocation == "manual" {
			continue
		}
		visible = append(visible, sk)
	}
	if len(visible) == 0 {
		return ""
	}
	return boundedCatalog(visible)
}

// ApplyIndex appends the skills index to basePrompt, or returns it unchanged
// when there are no skills. Only names + descriptions (+ a subagent tag) are
// listed; bodies load on demand via run_skill.
func ApplyIndex(basePrompt string, skills []Skill) string {
	block := IndexBlock(skills)
	if block == "" {
		return basePrompt
	}
	return basePrompt + "\n\n" + block
}

// Keep the full identifier and run tag while sharing the description budget.
func indexLineWithLimit(sk Skill, descriptionLimit int) string {
	desc := strings.TrimSpace(strings.ReplaceAll(sk.Description, "\n", " "))
	if desc == "" {
		desc = missingDescPlaceholder
	}
	tag := ""
	if sk.RunAs == RunSubagent {
		tag = " [🧬 subagent]"
	}
	max := min(descriptionLimit, 130-len([]rune(sk.Name))-len([]rune(tag)))
	clipped := clipRunes(desc, max)
	if clipped == "" {
		return "- " + sk.Name + tag
	}
	return "- " + sk.Name + tag + " — " + clipped
}

// clipRunes preserves the historical name but clips by grapheme clusters so
// combined emoji and other user-visible characters stay intact.
func clipRunes(s string, max int) string {
	return textutil.ClipGraphemes(s, max, "…")
}
