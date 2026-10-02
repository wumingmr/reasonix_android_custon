package permission

import (
	"strings"
	"unicode"
)

// RuleDefect says why a configured rule can never match.
type RuleDefect int

const (
	// DefectNoSuchTool marks a rule whose tool name contains whitespace, which
	// no tool answers to: the author wrote a shell command where a tool name
	// belongs.
	DefectNoSuchTool RuleDefect = iota
	// DefectUnclosedSubject marks a rule that opens a subject parenthesis and
	// never closes it. ParseRule then reads the whole entry as one tool name,
	// so the rule is unmatchable for a reason the author can fix by typing one
	// character rather than by rewriting the entry.
	DefectUnclosedSubject
)

// UnmatchableRule is a configured rule that no tool call can satisfy, paired
// with the rewrite that expresses what the author meant.
type UnmatchableRule struct {
	// List is "allow", "ask" or "deny" — which setting carried the rule.
	List string
	// Rule is the entry exactly as configured.
	Rule string
	// Defect says what is wrong with Rule, so a caller can name the cause
	// instead of only offering a replacement.
	Defect RuleDefect
	// Suggestion is a rule that parses and matches what the entry named, never
	// a wider grant than it asked for: on the allow list a bare command becomes
	// an exact rule, not a prefix that would also admit unlisted arguments.
	Suggestion string
}

// UnmatchableRules reports configured rules that cannot match any tool call.
// ParseRule treats only an empty tool name as malformed, so an entry like
// "git push --force" parses and installs a rule keyed on a tool nothing
// answers to: a deny the author believes is enforced never fires. Whitespace
// in a tool name is an exact test, not a guess — a tool name is an identifier
// and never contains any, which TestNoBuiltinToolNameHasWhitespace pins — so
// this cannot accuse a plugin or MCP tool that merely is not registered yet.
// Both sides ask unicode.IsSpace so the check and its invariant cannot drift
// apart.
func UnmatchableRules(allow, ask, deny []string) []UnmatchableRule {
	var out []UnmatchableRule
	for _, group := range []struct {
		list  string
		rules []string
	}{{"allow", allow}, {"ask", ask}, {"deny", deny}} {
		for _, raw := range group.rules {
			if finding, ok := unmatchableRule(group.list, raw); ok {
				out = append(out, finding)
			}
		}
	}
	return out
}

func unmatchableRule(list, raw string) (UnmatchableRule, bool) {
	rule, ok := ParseRule(raw)
	if !ok {
		return UnmatchableRule{}, false
	}
	trimmed := strings.TrimSpace(raw)
	// A "(" in a parsed tool name can only come from ParseRule's fallback
	// branch, reached when the entry opens a subject it never closes; the
	// literal "tool=subject" form parses earlier, so it is not caught here.
	if !rule.Literal && strings.Contains(rule.Tool, "(") {
		return UnmatchableRule{
			List:       list,
			Rule:       trimmed,
			Defect:     DefectUnclosedSubject,
			Suggestion: closedSubjectSuggestion(list, trimmed),
		}, true
	}
	if strings.IndexFunc(rule.Tool, unicode.IsSpace) < 0 {
		return UnmatchableRule{}, false
	}
	return UnmatchableRule{
		List:       list,
		Rule:       trimmed,
		Defect:     DefectNoSuchTool,
		Suggestion: bashRuleSuggestion(list, rule),
	}, true
}

// closedSubjectSuggestion repairs the missing ")" and then re-judges the
// result, so an entry that is wrong in both ways ("git push(--force") is not
// handed back a rule that still matches nothing.
func closedSubjectSuggestion(list, trimmed string) string {
	repaired := trimmed + ")"
	rule, ok := ParseRule(repaired)
	if !ok {
		return repaired
	}
	if strings.IndexFunc(rule.Tool, unicode.IsSpace) >= 0 {
		return bashRuleSuggestion(list, rule)
	}
	return repaired
}

// bashRuleSuggestion renders the rule the author probably wanted. One that
// already carries a subject keeps it verbatim rather than guessing a second
// time. For a bare command the form depends on the list: deny and ask get the
// prefix rule, so every invocation of the command is covered, while allow gets
// the exact command, because a prefix there would silently grant arguments the
// author never wrote — "git branch" would also run "git branch -D".
func bashRuleSuggestion(list string, rule Rule) string {
	command := strings.TrimSpace(rule.Tool)
	if subject := strings.TrimSpace(rule.Subject); subject != "" {
		return "Bash(" + command + " " + subject + ")"
	}
	if list == "allow" {
		return "Bash(" + command + ")"
	}
	return "Bash(" + command + ":*)"
}
