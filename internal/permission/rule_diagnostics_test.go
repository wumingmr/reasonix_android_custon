package permission

import (
	"strings"
	"testing"
)

// TestUnmatchableRulesReportsBareShellCommands covers the misconfiguration in
// #6692: the rules were typed as bare commands, so every one of them named a
// tool that does not exist and the deny list never fired.
func TestUnmatchableRulesReportsBareShellCommands(t *testing.T) {
	got := UnmatchableRules(
		[]string{"git status", "Bash(git diff:*)"},
		[]string{"git push --force"},
		[]string{"git checkout HEAD --"},
	)
	if len(got) != 3 {
		t.Fatalf("got %d findings, want 3: %+v", len(got), got)
	}
	want := []UnmatchableRule{
		{List: "allow", Rule: "git status", Defect: DefectNoSuchTool, Suggestion: "Bash(git status)"},
		{List: "ask", Rule: "git push --force", Defect: DefectNoSuchTool, Suggestion: "Bash(git push --force:*)"},
		{List: "deny", Rule: "git checkout HEAD --", Defect: DefectNoSuchTool, Suggestion: "Bash(git checkout HEAD --:*)"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("finding %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestUnmatchableRulesLeavesWorkingRulesAlone is the guardrail that keeps the
// warning trustworthy: a rule that does match must never be reported, or the
// notice trains its reader to ignore it.
func TestUnmatchableRulesLeavesWorkingRulesAlone(t *testing.T) {
	ok := []string{
		"Bash(git status:*)",
		"Bash",
		"edit_file",
		"edit_file(src/**)",
		"mcp__github__create_issue",
		"Bash(git commit -m *)",
		"read_file=exact/path.go",
		"",
	}
	if got := UnmatchableRules(ok, nil, nil); len(got) != 0 {
		t.Fatalf("working rules were reported as unmatchable: %+v", got)
	}
}

// TestUnmatchableRulesKeepsAnExistingSubject checks the suggestion does not
// guess twice: a rule that already carries a subject keeps it.
func TestUnmatchableRulesKeepsAnExistingSubject(t *testing.T) {
	got := UnmatchableRules(nil, nil, []string{"git push(--force)"})
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if want := "Bash(git push --force)"; got[0].Suggestion != want {
		t.Fatalf("suggestion = %q, want %q", got[0].Suggestion, want)
	}
}

// TestUnmatchableRuleSuggestionsParseBack closes the loop: every suggestion
// the warning prints must itself be a rule that parses and targets bash.
func TestUnmatchableRuleSuggestionsParseBack(t *testing.T) {
	for _, r := range UnmatchableRules([]string{"git status"}, []string{"rm -rf /"}, []string{"git stash drop"}) {
		rule, ok := ParseRule(r.Suggestion)
		if !ok {
			t.Errorf("suggestion %q does not parse", r.Suggestion)
			continue
		}
		if canonicalRuleTool(rule.Tool) != "bash" {
			t.Errorf("suggestion %q targets %q, want the bash tool", r.Suggestion, rule.Tool)
		}
		if !strings.Contains(r.Suggestion, "Bash(") {
			t.Errorf("suggestion %q is not in Bash(...) form", r.Suggestion)
		}
	}
}

// TestUnmatchableRulesCatchesEveryWhitespace keeps the check and the invariant
// it rests on asking the same question: a tool name broken across lines is as
// unmatchable as one with a plain space.
func TestUnmatchableRulesCatchesEveryWhitespace(t *testing.T) {
	for _, raw := range []string{"git\tstatus", "git\nstatus", "git status"} {
		if got := UnmatchableRules(nil, nil, []string{raw}); len(got) != 1 {
			t.Errorf("UnmatchableRules(%q) returned %d findings, want 1", raw, len(got))
		}
	}
}

// TestUnmatchableRulesReportsUnclosedSubject covers the entry that is nearly
// right: a rule missing its closing parenthesis parses as one long tool name,
// so it must be named as the typo it is instead of being wrapped in a second
// Bash(...) that would parse but still match nothing.
func TestUnmatchableRulesReportsUnclosedSubject(t *testing.T) {
	tests := []struct {
		name string
		list string
		rule string
		want UnmatchableRule
	}{
		{
			name: "deny keeps the subject the author typed",
			list: "deny",
			rule: "Bash(git status:*",
			want: UnmatchableRule{List: "deny", Rule: "Bash(git status:*", Defect: DefectUnclosedSubject, Suggestion: "Bash(git status:*)"},
		},
		{
			// Wrong in both ways at once: closing the parenthesis alone still
			// leaves a tool named "git push".
			name: "a command in the tool position is repaired too",
			list: "deny",
			rule: "git push(--force",
			want: UnmatchableRule{List: "deny", Rule: "git push(--force", Defect: DefectUnclosedSubject, Suggestion: "Bash(git push --force)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnmatchableRules(nil, nil, []string{tt.rule})
			if len(got) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(got), got)
			}
			if got[0] != tt.want {
				t.Fatalf("finding = %+v, want %+v", got[0], tt.want)
			}
		})
	}
}

// TestUnmatchableRuleSuggestionsAreThemselvesMatchable closes the trap this
// change is about: a suggestion must not be another rule that parses and then
// matches nothing, or the warning would send its reader in a circle.
func TestUnmatchableRuleSuggestionsAreThemselvesMatchable(t *testing.T) {
	bad := []string{"git status", "Bash(git status:*", "git push(--force", "rm -rf /"}
	for _, r := range UnmatchableRules(bad, bad, bad) {
		if again := UnmatchableRules([]string{r.Suggestion}, nil, nil); len(again) != 0 {
			t.Errorf("suggestion %q for %q is itself unmatchable: %+v", r.Suggestion, r.Rule, again)
		}
	}
}

// TestUnmatchableAllowSuggestionDoesNotWidenTheGrant pins the asymmetry: the
// prefix form is right for deny and ask, where covering every argument is the
// safe direction, but on the allow list it would auto-run arguments the author
// never granted.
func TestUnmatchableAllowSuggestionDoesNotWidenTheGrant(t *testing.T) {
	got := UnmatchableRules([]string{"git branch"}, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if want := "Bash(git branch)"; got[0].Suggestion != want {
		t.Fatalf("suggestion = %q, want %q", got[0].Suggestion, want)
	}

	// The claim is about what the suggested rule does, so check the policy it
	// builds rather than only its text.
	p := New("ask", []string{got[0].Suggestion}, nil, nil)
	if d := p.DecideSubject("bash", false, "git branch"); d != Allow {
		t.Errorf("the command the author named decides %v, want allow", d)
	}
	if d := p.DecideSubject("bash", false, "git branch -D main"); d == Allow {
		t.Error("the suggested allow rule also grants \"git branch -D main\"")
	}

	// The same command on deny keeps the prefix form, which must cover the
	// arguments the allow form deliberately leaves out.
	denySuggestion := UnmatchableRules(nil, nil, []string{"git branch"})[0].Suggestion
	if want := "Bash(git branch:*)"; denySuggestion != want {
		t.Fatalf("deny suggestion = %q, want %q", denySuggestion, want)
	}
	if d := New("ask", nil, nil, []string{denySuggestion}).DecideSubject("bash", false, "git branch -D main"); d != Deny {
		t.Errorf("the suggested deny rule decides %v for \"git branch -D main\", want deny", d)
	}
}
