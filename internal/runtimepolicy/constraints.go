package runtimepolicy

import (
	"path/filepath"
	"regexp"
	"strings"

	"reasonix/internal/shellparse"
)

// Constraints are explicit user or host limits. They never encode task
// complexity, security keywords, or file counts.
type Constraints struct {
	ForbidMutation bool // plan mode or an inherited parent only; user prose never sets it
	ForbidTests    bool
	AllowedChecks  []string
	ForbidExternal bool
	// AllowRebuild records that the user explicitly asked to rewrite a file
	// completely. It only ever waives the read-before-overwrite requirement for
	// a file the same instruction names; the model can never set it.
	AllowRebuild bool
	// RebuildPaths are the resolved files an AllowRebuild instruction named.
	// The waiver is a membership test over this host-recorded set, never a
	// re-parse of instruction text at write time.
	RebuildPaths     []string
	PlanModeReadOnly bool
	Notes            []string
}

// ParseConstraints accepts only explicit forbid/limit phrasing.
func ParseConstraints(instruction string) Constraints {
	var c Constraints
	lower := strings.ToLower(instruction)
	if matchesAny(lower, []string{
		"不要测试", "别跑测试", "不用测试", "跳过测试", "不要跑测试",
		"don't run tests", "do not run tests", "no tests", "skip tests",
		"without tests", "don't test", "do not test",
	}) {
		c.ForbidTests = true
		c.Notes = append(c.Notes, "user_forbid_tests")
	}
	if matchesAny(lower, []string{
		"完全重写", "从头重写", "整个重写", "直接重写", "覆盖重写", "整个文件重写",
		"from scratch", "rewrite it completely", "rewrite the file completely",
		"overwrite it completely", "replace it entirely", "rebuild the file",
		"rewrite this file", "rewrite the whole file",
	}) {
		c.AllowRebuild = true
		c.Notes = append(c.Notes, "user_allow_rebuild")
	}
	if cmds := parseAllowedChecks(instruction); len(cmds) > 0 {
		c.AllowedChecks = cmds
		c.Notes = append(c.Notes, "user_allowed_checks")
	}
	if matchesAny(lower, []string{
		"不要 push", "不要push", "别 push", "别push", "不要推送", "不要发布",
		"don't push", "do not push", "no push", "don't publish", "do not publish",
		"no publish", "don't deploy", "do not deploy",
	}) {
		c.ForbidExternal = true
		c.Notes = append(c.Notes, "user_forbid_external")
	}
	return c
}

// StripQuotedConstraints removes fenced and quoted spans so cited phrases
// cannot bind the host.
func StripQuotedConstraints(raw string) string {
	s := stripFences(raw)
	s = stripInlineCode(s)
	s = stripQuoted(s, '"', '"')
	s = stripQuoted(s, '“', '”')
	s = stripQuoted(s, '「', '」')
	return strings.TrimSpace(s)
}

// rebuildPathPattern extracts candidate file tokens from one instruction clause.
var rebuildPathPattern = regexp.MustCompile("`[^`]+`|\"[^\"]+\"|'[^']+'|[A-Za-z0-9_./\\\\:-]+")

// ParseRebuildPaths resolves the files an instruction names in a clause that
// itself grants AllowRebuild. Callers record the result once per turn and
// authorize a rebuild by membership, so model-authored text can never grant the
// waiver at write time.
func ParseRebuildPaths(instruction, baseDir string) []string {
	var paths []string
	for _, clause := range strings.FieldsFunc(instruction, func(r rune) bool {
		return strings.ContainsRune("\n;；。!?！？", r)
	}) {
		if !ParseConstraints(clause).AllowRebuild {
			continue
		}
		lower := strings.ToLower(clause)
		if matchesAny(lower, []string{"不要", "别", "not ", "don't", "禁止"}) {
			continue
		}
		for _, token := range rebuildPathPattern.FindAllString(clause, -1) {
			token = strings.Trim(token, "`\"'")
			if token == "" {
				continue
			}
			if !filepath.IsAbs(token) {
				token = filepath.Join(baseDir, token)
			}
			paths = append(paths, filepath.Clean(token))
		}
	}
	return paths
}

func (c Constraints) AllowsMutation() bool {
	return !c.ForbidMutation && !c.PlanModeReadOnly
}

func (c Constraints) AllowsTests() bool { return !c.ForbidTests }

func (c Constraints) AllowsExternal() bool { return !c.ForbidExternal }

func (c Constraints) AllowsCommand(command string) bool {
	if !c.AllowsTests() {
		return false
	}
	command = strings.TrimSpace(command)
	if command == "" || len(c.AllowedChecks) == 0 {
		return true
	}
	for _, allowed := range c.AllowedChecks {
		if strings.EqualFold(strings.TrimSpace(allowed), command) {
			return true
		}
	}
	commandFields, malformed := shellparse.StaticFields(command)
	if malformed != "" || len(commandFields) == 0 {
		return false
	}
	for _, allowed := range c.AllowedChecks {
		allowedFields, malformed := shellparse.StaticFields(strings.TrimSpace(allowed))
		if malformed == "" && len(allowedFields) > 0 && hasFieldPrefix(commandFields, allowedFields) {
			return true
		}
	}
	return false
}

func parseAllowedChecks(instruction string) []string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)只跑\s+([^\n,，;；]+)`),
		regexp.MustCompile(`(?i)只运行\s+([^\n,，;；]+)`),
		regexp.MustCompile(`(?i)only\s+run\s+([^\n,;]+)`),
		regexp.MustCompile(`(?i)just\s+run\s+([^\n,;]+)`),
	}
	var out []string
	for _, re := range patterns {
		m := re.FindStringSubmatch(instruction)
		if len(m) < 2 {
			continue
		}
		cmd := strings.Trim(strings.TrimSpace(m[1]), "\"'`。.")
		if cmd != "" {
			out = append(out, cmd)
		}
	}
	return out
}

func matchesAny(lower string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(lower, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func hasFieldPrefix(fields, prefix []string) bool {
	if len(prefix) > len(fields) {
		return false
	}
	for i := range prefix {
		if !strings.EqualFold(fields[i], prefix[i]) {
			return false
		}
	}
	return true
}

func stripFences(s string) string {
	var b strings.Builder
	inFence := false
	for line := range strings.SplitSeq(s, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func stripInlineCode(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == '`' {
			in = !in
			continue
		}
		if !in {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stripQuoted(s string, open, close rune) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if !in && r == open {
			in = true
			continue
		}
		if in && r == close {
			in = false
			continue
		}
		if !in {
			b.WriteRune(r)
		}
	}
	return b.String()
}
