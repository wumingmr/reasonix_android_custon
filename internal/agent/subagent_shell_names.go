package agent

import "reasonix/internal/tool"

// normalizeSubagentShellNames keeps legacy skill/profile allowlists working
// when Windows exposes only the canonical pwsh tool. The parent registry is
// authoritative: aliases select its platform shell and never add a second
// executor or bypass its sandbox wrapper.
func normalizeSubagentShellNames(parent *tool.Registry, names []string) []string {
	if parent == nil || len(names) == 0 {
		return names
	}
	shellName := ""
	if _, ok := parent.Get("pwsh"); ok {
		shellName = "pwsh"
	} else if _, ok := parent.Get("bash"); ok {
		shellName = "bash"
	}
	if shellName == "" {
		return names
	}
	out := append([]string(nil), names...)
	for i, name := range out {
		if tool.IsShellToolName(name) {
			out[i] = shellName
		}
	}
	return out
}
