package skill

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCatalogSharesDescriptionBudgetBeforeOmittingSkills(t *testing.T) {
	var skills []Skill
	for i := range 60 {
		skills = append(skills, Skill{
			Name: fmt.Sprintf("skill-%03d", i), Description: strings.Repeat("description ", 30),
		})
	}
	skills = append(skills, Skill{Name: "private", Invocation: "manual"})
	out := CatalogBlock(skills)
	if utf8.RuneCountInString(out) > IndexMaxChars {
		t.Fatal("catalog exceeded its character budget")
	}
	for _, sk := range skills[:60] {
		if !strings.Contains(out, "- "+sk.Name+" — ") {
			t.Errorf("missing skill or description: %s", sk.Name)
		}
	}
	if strings.Contains(out, "private") || strings.Contains(out, "more skills") {
		t.Fatalf("unexpected visibility or overflow: %s", out)
	}
	if out != CatalogBlock(skills) || out != ReadOnlyCatalogBlock(skills) {
		t.Fatal("catalog differs across identical calls or read-only rendering")
	}
}

func TestCatalogOverflowPreservesWholeEntriesAndReportsOmissions(t *testing.T) {
	var skills []Skill
	names := map[string]bool{}
	for i := range 200 {
		name := fmt.Sprintf("skill-%03d-%s", i, strings.Repeat("x", 40))
		names[name] = true
		skills = append(skills, Skill{Name: name, RunAs: RunSubagent, Description: "A useful operation"})
	}
	out := CatalogBlock(skills)
	if utf8.RuneCountInString(out) > IndexMaxChars || !strings.HasSuffix(out, "\n```") {
		t.Fatal("overflow broke the catalog budget or envelope")
	}
	shown := 0
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(line, "- "), " ")
		if !names[name] || !strings.Contains(line, "[🧬 subagent]") {
			t.Fatalf("partial skill identity or tag: %q", line)
		}
		shown++
	}
	if shown == 0 || !strings.Contains(out, fmt.Sprintf("%d more skills", len(skills)-shown)) ||
		!strings.Contains(out, "use_capability action=search") {
		t.Fatalf("overflow lacks accurate count/discovery route: %s", out)
	}
}

func TestCatalogKeepsUnicodeDescriptionsWithinBudget(t *testing.T) {
	var skills []Skill
	for i := range 60 {
		skills = append(skills, Skill{Name: fmt.Sprintf("unicode-%d", i),
			Description: strings.Repeat("👨‍👩‍👧‍👦中文é ", 80)})
	}
	out := CatalogBlock(skills)
	if !utf8.ValidString(out) || utf8.RuneCountInString(out) > IndexMaxChars {
		t.Fatal("unicode catalog is invalid or over budget")
	}
	for _, sk := range skills {
		if !strings.Contains(out, "- "+sk.Name+" ") {
			t.Errorf("lost identity: %s", sk.Name)
		}
	}
}

func TestCatalogPolicyDoesNotDependOnDynamicSkills(t *testing.T) {
	policy := InvocationPolicyBlock()
	for _, sk := range []Skill{{Name: "alpha", Description: "first"}, {Name: "beta", Description: "second"}} {
		if got := IndexBlock([]Skill{sk}); got != policy+"\n\n"+CatalogBlock([]Skill{sk}) {
			t.Fatal("dynamic catalog changed the stable policy boundary")
		}
	}
}
