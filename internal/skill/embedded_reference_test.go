package skill_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"reasonix/internal/skill"
)

func TestEmbeddedGuideReferencesAreReadableOnDemand(t *testing.T) {
	store := skill.New(skill.Options{HomeDir: t.TempDir()})
	loader := skill.NewReadSkillTool(store)
	root, err := loader.Execute(context.Background(), json.RawMessage(`{"name":"reasonix-guide"}`))
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`\]\((references/[^)]+\.md)\)`).FindAllStringSubmatch(root, -1)
	if len(links) != 6 {
		t.Fatalf("reference routes = %d, want 6", len(links))
	}
	for _, link := range links {
		args, _ := json.Marshal(map[string]string{"name": "reasonix-guide", "reference": link[1]})
		page, err := loader.Execute(context.Background(), args)
		if err != nil || len(page) == 0 {
			t.Fatalf("%s: %v", link[1], err)
		}
		if page == root || strings.Contains(root, strings.SplitN(page, "\n\n", 2)[1]) {
			t.Fatalf("%s was not loaded separately", link[1])
		}
		again, _ := loader.Execute(context.Background(), args)
		if again != page {
			t.Fatalf("%s is not stable", link[1])
		}
	}
	guide, _ := store.Read("reasonix-guide")
	if !strings.Contains(skill.CatalogBlock(store.List()), guide.Description) {
		t.Fatal("built-in guide trigger description was clipped")
	}
}

func TestEmbeddedReferenceRejectsTraversalAndNonReferenceFiles(t *testing.T) {
	loader := skill.NewReadSkillTool(skill.New(skill.Options{HomeDir: t.TempDir()}))
	for _, ref := range []string{
		"../SKILL.md", "references/../SKILL.md", "/etc/passwd",
		"references/../../other/SKILL.md", "references\\skills.md",
		"SKILL.md", "references/missing.md", "references/skills.md/extra",
	} {
		t.Run(ref, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"name": "reasonix-guide", "reference": ref})
			if out, err := loader.Execute(context.Background(), args); err == nil || out != "" {
				t.Fatalf("reference %q allowed: %q, %v", ref, out, err)
			}
		})
	}
	if _, err := loader.Execute(context.Background(), json.RawMessage(`{"name":"reasonix-guide","reference":"references/skills.md","arguments":"task"}`)); err == nil {
		t.Fatal("ambiguous reference/task request accepted")
	}
}

func TestEmbeddedReferenceHonorsOverrideAndDisabledSkill(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "override", true: "disabled"}[disabled], func(t *testing.T) {
			opts := skill.Options{HomeDir: t.TempDir(), ProjectRoot: t.TempDir()}
			if disabled {
				opts.DisabledNames = []string{"reasonix-guide"}
			}
			store := skill.New(opts)
			if !disabled {
				_, err := store.CreateWithContent("reasonix-guide", skill.ScopeProject,
					"---\nname: reasonix-guide\ndescription: Project guide\n---\nLocal guidance")
				if err != nil {
					t.Fatal(err)
				}
			}
			loader := skill.NewReadSkillTool(skill.New(opts))
			out, err := loader.Execute(context.Background(), json.RawMessage(`{"name":"reasonix-guide","reference":"references/skills.md"}`))
			if err == nil || out != "" {
				t.Fatal("embedded page bypassed the winning/disabled skill")
			}
			if !disabled {
				out, err = loader.Execute(context.Background(), json.RawMessage(`{"name":"reasonix-guide"}`))
				if err != nil || !strings.Contains(out, "Local guidance") {
					t.Fatal("legacy body read no longer uses the project override")
				}
			}
		})
	}
}
