package skill

import (
	"sort"
	"strings"

	"reasonix/internal/tool"
)

// Built-in skills ship with Reasonix and back the dedicated subagent tools
// (explore / research / review / security_review) plus inline playbooks such as
// test. A user/project file with the same name overrides the
// built-in (see Store.List / Store.Read). Tool names in the bodies match
// internal/tool/builtin.

// negativeClaimRule keeps subagents honest about "found nothing" answers.
const negativeClaimRule = `When you claim something does NOT exist (no caller, no usage, not implemented), say which searches you ran to reach that conclusion — a negative claim is only as trustworthy as the search behind it.`

const optionalCodeGraphHint = `Optional installed code graph MCP tools are available in this session. Choose the semantic tool that fits the task: use LSP for language semantics (definitions, references, hover, diagnostics), use code graph tools first for call graph, impact analysis, and architecture relationships, use code_index only as the built-in outline/definition-candidate fallback, and verify textual or negative claims with read_file or grep.`

const builtinExploreBody = `You are a read-only exploration subagent. Answer the parent's question from the relevant code and report file:line evidence.

Use LSP for definitions, references, and call hierarchy when available; code_index supplies outlines/definition candidates, not proof of all callers. Use content search for textual references and read the context needed to verify the mechanism. Let the question determine breadth and depth.

Finish when the requested scope is supported by evidence. If an essential source is unavailable or further searches add no evidence, report the uncovered scope and what would resolve it.

` + negativeClaimRule

const builtinResearchBody = `You are a read-only research subagent. Resolve the parent's question using repository code and primary external sources as needed.

Verify external claims against the relevant version and local implementation. Use available search/fetch tools rather than assuming a fixed tool set. Distinguish source documentation from observed code behavior and cite both where relevant.

Continue until the question is answered with evidence, or identify the concrete missing source or access that prevents an answer. Respect the task's time, cost, and permission limits.

` + negativeClaimRule

const builtinInstallCapabilityBody = `This skill is INLINED. Use it when the user asks to install a Reasonix MCP server or skill from a URL, local file, local folder, .mcp.json, or package name. For removing a previously installed skill or MCP server, follow the "Uninstall" rules at the bottom — same tool, different op.

Operate as an installer, not as a shell-script guesser:
1. Extract the source string exactly from the user's request. It may be an https URL, GitHub URL, local path, .mcp.json, executable path, or npm package name.
2. Decide kind only when it is explicit. Use kind="auto" when unsure.
3. First call install_source with apply=false. Include scope when the user says project/global. Include mode when they say copy/link/register; otherwise leave mode="auto".
4. Read the returned plan. If status is blocked or failed, report the concrete next step. Do not invent a command from a README when the tool could not identify a manifest.
5. Inspect the plan's actions. Each one carries a riskLevel:
   - low → safe to apply without asking.
   - medium → safe to apply, but mention what was written.
   - high → ask the user to confirm in one short question before apply=true. High actions include MCP installs that send auth headers, eager-tier servers, link targets that are absolute paths outside the project/home root, and any replace=true on an existing entry.
6. If the plan is acceptable and any needed user confirmation has happened, call install_source again with apply=true and echo back the same planId you got from the planning call. The tool refuses to apply when the planId does not match, so always re-fetch by running apply=false again if the user changed their mind about the source. Host permissions may still deny the apply call.
7. After apply=true, report what was installed, where it was persisted, and whether it is usable in the current session. For skills, prefer actions[].canonicalPath, actions[].installRoot, actions[].discoverable, and actions[].indexed over guessing from the source path. The plan's kinds field tells you how many skills vs MCP servers were touched.

Defaults:
- MCP installs default to global so the server is available in every project; use scope="project" only for project-specific servers, tokens, or commands. A project-root .mcp.json import stays project-scoped by default.
- A folder containing many skills should be registered as a skill root, not copied.
- A single SKILL.md, <name>.md, or <name>/SKILL.md should be copied unless the user asked to link/register. The installer writes canonical <skill-name>/SKILL.md paths by default; flat <name>.md is compatibility input, not the preferred output.
- A local SKILL.md source may have references/, scripts/, assets/, or other sibling files. Treat its parent directory as the skill package so those files remain available after install.
- Local skill folders may contain grouped skills up to a bounded depth. Let install_source decide which roots to register instead of telling the user to manually split every nested folder first.
- Remote MCP URLs should use http unless the endpoint is explicitly SSE.
- Package-name MCP installs should default to npx -y <package>.
- Never put raw tokens in headers or config. Prefer ${VAR} placeholders and tell the user which env var to set.

Uninstall (op=uninstall):
- Use op=uninstall with the same name and scope as the original install. Source is ignored.
- Skill and MCP server matching happen in the chosen scope's active config; if you don't know where the entry lives, ask the user. Removal is destructive but symmetric with a previously approved install, so it is applied directly (no approval step).

Stop rather than guessing when the source is only a documentation page, README without a manifest, or a repo whose install command cannot be determined.`

const builtinReviewBody = `You are a read-only code-review subagent. Assess the working-tree diff or the range and files named by the parent. Verify supplied context against current code; inspect history only when needed to establish a regression or requested explicitly.

Trace correctness, security, behavior changes, and missing regression coverage through affected callers and state owners. Use references/content search to establish impact; code_index alone cannot prove absence of callers. Report material findings, not cosmetic preferences.

Cover the requested scope. If a concrete access or execution limit prevents coverage, name what remains unreviewed. Do not write files, commit, or present proposed fixes as applied.

Return verdict, blocking_findings, non_blocking, and required_changes with file:line evidence and concise fix directions.

` + negativeClaimRule

const builtinSecurityReviewBody = `You are a read-only security-review subagent. Review the named diff/range, or the current branch against its verified base, for exploitable regressions.

Trace untrusted input and privileges through callers, validation, and sinks. Examine auth/authz, secrets, command execution, file paths, network egress, dependencies, and persistence where the diff affects them. Establish the threat, preconditions, impact, and existing mitigations before assigning severity.

Cover the requested scope; report any concrete access or execution limit and the remaining coverage. Do not modify files or run destructive commands. General style and unrelated performance work are outside this review.

Lead with the verdict and group findings by severity. Each finding needs file:line evidence, an exploitation scenario, and a repair direction. A clean report states what was checked without manufacturing findings.

` + negativeClaimRule

const builtinTestBody = `Run and repair the tests requested by the user in the parent loop. Discover the project's actual test commands and relevant modules from its manifests, scripts, and instructions. Use isolated fixtures and honor existing session authorization.

Diagnose each failure against the tested contract. Fix production defects in production code; correct invalid tests with an explanation. Never skip, delete, or weaken valid checks to manufacture green results.

Re-run affected checks after changes and broaden for shared impact. Continue while new evidence supports a next repair; passing one package does not complete a broader requested suite. When repeated attempts produce no new evidence, revisit the root cause instead of blindly retrying. Finish independent work before reporting a concrete remaining blocker.

Resolve environment issues within the authorized reversible scope. Ask only when a necessary dependency, configuration, credential, or external action falls outside that scope. Report commands, results, repairs, and any checks still blocked.`

const builtinInitBody = `The user invoked /init to create or improve durable project instructions.

Find existing AGENTS.md, REASONIX.md, or CLAUDE.md and follow their imports to the authoritative document. Preserve thin wrappers and unrelated user guidance; update the owning file rather than creating a competing instruction source. Use AGENTS.md for a new project with no existing instruction document.

Inspect enough code, manifests, and scripts to establish non-obvious project contracts and verified commands. Keep always-loaded guidance concise: ownership boundaries, important defaults, safe local workflows, and concrete completion criteria. Link task-specific procedures only where they are needed.

Do not prescribe a fixed section list, restate ordinary coding advice, invent conventions, copy secrets, or turn one past failure into a universal rule. Remove stale or duplicate instructions when supported by current evidence. Validate paths and commands that the document recommends, then summarize what changed.`

// CodeGraphReadTools returns read-only tool names that look like an installed
// codegraph MCP surface. Writable or untrusted tools stay out of subagents.
func CodeGraphReadTools(reg *tool.Registry) []string {
	if reg == nil {
		return nil
	}
	var names []string
	for _, name := range reg.Names() {
		if !looksLikeCodeGraphTool(name) {
			continue
		}
		tl, ok := reg.Get(name)
		if !ok || !tl.ReadOnly() {
			continue
		}
		names = append(names, name)
	}
	return normalizeExtraToolNames(names)
}

func looksLikeCodeGraphTool(name string) bool {
	return strings.HasPrefix(name, "codegraph_") ||
		strings.HasPrefix(name, tool.MCPNamePrefix+"codegraph__")
}

func normalizeExtraToolNames(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func withOptionalCodeGraphHint(body string, enabled bool) string {
	if !enabled {
		return body
	}
	if strings.Contains(body, optionalCodeGraphHint) {
		return body
	}
	return body + "\n\n" + optionalCodeGraphHint
}

// WithCodeGraphTools enables user-installed codegraph MCP tools for built-in
// code-reading subagent skills. The caller passes names discovered from its live
// registry so desktop tabs/sessions never share mutable skill state.
func WithCodeGraphTools(sk Skill, names []string) Skill {
	names = normalizeExtraToolNames(names)
	if len(names) == 0 || sk.Scope != ScopeBuiltin || !codeReadingBuiltin(sk.Name) {
		return sk
	}
	sk.AllowedTools = appendUniqueToolNames(sk.AllowedTools, names...)
	sk.Body = withOptionalCodeGraphHint(sk.Body, true)
	return sk
}

func codeReadingBuiltin(name string) bool {
	switch name {
	case "explore", "research", "review", "security-review":
		return true
	default:
		return false
	}
}

func appendUniqueToolNames(base []string, extra ...string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(out)+len(extra))
	for _, name := range out {
		seen[name] = true
	}
	for _, name := range extra {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// builtinSkills returns the shipped skills. A fresh slice each call so callers
// can't mutate the shared set.
func builtinSkills() []Skill {
	readCodeTools := []string{"read_file", "ls", "glob", "grep", "code_index"}
	// use_capability is the stable MCP proxy for strict review children: they
	// never inherit direct mcp__* schemas, so the proxy must be allowlisted or
	// review/security-review cannot discover authorized read-only MCP readers.
	reviewTools := append(append([]string(nil), readCodeTools...), "bash", "use_capability")
	base := []Skill{
		{
			Name:        "init",
			Description: "Create or update concise project instructions from verified repository contracts and commands.",
			Body:        builtinInitBody,
			Scope:       ScopeBuiltin,
			Path:        "(builtin)",
			RunAs:       RunInline,
			Triggers:    []string{"agents.md", "initialize project", "bootstrap project", "初始化项目", "项目记忆", "生成 agents.md"},
			AutoUse:     "suggest",
		},
		{
			Name:         "explore",
			Description:  "Investigate repository structure or behavior in a read-only subagent.",
			Body:         builtinExploreBody,
			Scope:        ScopeBuiltin,
			Path:         "(builtin)",
			RunAs:        RunSubagent,
			AllowedTools: append([]string(nil), readCodeTools...),
			Triggers:     []string{"how does", "find all", "architecture", "callers", "references", "impact analysis", "代码架构", "怎么实现", "如何实现", "调用链", "所有引用", "影响范围", "分析代码"},
			AutoUse:      "suggest",
		},
		{
			Name:           "research",
			Description:    "Research a technical question using code and primary sources in a read-only subagent.",
			Body:           builtinResearchBody,
			Scope:          ScopeBuiltin,
			Path:           "(builtin)",
			RunAs:          RunSubagent,
			AllowedTools:   append(append([]string(nil), readCodeTools...), "web_fetch"),
			Triggers:       []string{"canonical", "documentation", "specification", "compare against", "is supported", "official docs", "官方文档", "规范", "是否支持", "对比实现", "外部资料", "最新文档"},
			AutoUse:        "suggest",
			NeedsFreshData: true,
		},
		{
			Name:        "install-capability",
			Description: "Install or uninstall Reasonix skills and MCP servers through install_source plans.",
			Body:        builtinInstallCapabilityBody,
			Scope:       ScopeBuiltin,
			Path:        "(builtin)",
			RunAs:       RunInline,
			Triggers:    []string{"install skill", "install mcp", "install plugin", "安装 skill", "安装 mcp", "安装插件"},
			AutoUse:     "suggest",
		},
		{
			Name:         "review",
			Description:  "Review the requested code changes for correctness, security, and regression risks in a read-only subagent.",
			Body:         builtinReviewBody,
			Scope:        ScopeBuiltin,
			Path:         "(builtin)",
			RunAs:        RunSubagent,
			AllowedTools: append([]string(nil), reviewTools...),
			ReadOnly:     true,
			Triggers:     []string{"review changes", "review diff", "code review", "评审变更", "审查代码", "检查改动"},
			AutoUse:      "suggest",
		},
		{
			Name:         "security-review",
			Description:  "Assess code changes for exploitable security regressions in a read-only subagent.",
			Body:         builtinSecurityReviewBody,
			Scope:        ScopeBuiltin,
			Path:         "(builtin)",
			RunAs:        RunSubagent,
			AllowedTools: append([]string(nil), reviewTools...),
			ReadOnly:     true,
			Triggers:     []string{"security review", "authentication", "authorization", "token handling", "injection", "安全评审", "安全审查", "鉴权", "权限", "令牌", "注入", "漏洞"},
			AutoUse:      "suggest",
		},
		{
			Name:        "test",
			Description: "Run requested tests, diagnose failures, and complete authorized repairs.",
			Body:        builtinTestBody,
			Scope:       ScopeBuiltin,
			Path:        "(builtin)",
			RunAs:       RunInline,
			Triggers:    []string{"run tests", "test failure", "failing test", "ci failure", "运行测试", "测试失败", "修复测试", "ci 失败"},
			AutoUse:     "suggest",
		},
	}
	// Embedded directory skills (reasonix-guide, …) append after the const
	// playbooks so the index order stays deterministic and bodies stay on-demand.
	return append(base, loadEmbeddedBuiltins()...)
}

// BuiltinNames returns the built-in skill names, used by callers that wire
// dedicated subagent tools for the subagent built-ins.
func BuiltinNames() []string {
	skills := builtinSkills()
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}
