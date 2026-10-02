package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type shellAliasFixture struct{ name string }

func (f shellAliasFixture) Name() string                                           { return f.name }
func (shellAliasFixture) Description() string                                      { return "shell" }
func (shellAliasFixture) Schema() json.RawMessage                                  { return json.RawMessage(`{"type":"object"}`) }
func (shellAliasFixture) Execute(context.Context, json.RawMessage) (string, error) { return "ok", nil }
func (shellAliasFixture) ReadOnly() bool                                           { return false }

func TestResolveCallRoutesHiddenShellAliasesToPwsh(t *testing.T) {
	r := NewRegistry()
	r.Add(shellAliasFixture{name: "pwsh"})
	for _, legacy := range []string{"bash", "Bash", "PowerShell", "powershell", "Pwsh"} {
		resolved, canonical, ambiguous := r.ResolveCall(legacy)
		if resolved == nil || canonical != "pwsh" || len(ambiguous) != 0 {
			t.Fatalf("ResolveCall(%q) = %v, %q, %v", legacy, resolved, canonical, ambiguous)
		}
	}
	if got := r.AllNames(); len(got) != 1 || got[0] != "pwsh" {
		t.Fatalf("hidden aliases leaked into registry catalog: %v", got)
	}
}
