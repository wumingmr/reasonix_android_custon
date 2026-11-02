package cli

// mcp_manager_view.go renders the /mcp manager overlay and its display strings.

import (
	"fmt"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/mcpdiag"
)

func (m chatTUI) renderMCPManagerPanel() panelContent {
	if m.mcp == nil {
		return panelContent{}
	}
	return m.mcp.renderPanel(m.width)
}

func (p *mcpManager) renderPanel(width int) panelContent {
	w := max(viewWidth(width), 40)
	switch p.stage {
	case mcpStageDetail:
		return p.renderDetailBody(w).render(managerContentPanelStyle(w), width)
	case mcpStageTools:
		return p.renderToolsBody(w).render(managerContentPanelStyle(w), width)
	case mcpStageLogs:
		return p.renderLogsBody(w).render(managerContentPanelStyle(w), width)
	case mcpStageConfirmRemove:
		return p.renderConfirmRemoveBody(w).render(managerContentPanelStyle(w), width)
	case mcpStageConfirmClearAuth:
		return p.renderConfirmClearAuthBody(w).render(managerContentPanelStyle(w), width)
	default:
		return p.renderListBody(w).render(managerContentPanelStyle(w), width)
	}
}

func (p *mcpManager) footerHint() string {
	switch p.stage {
	case mcpStageDetail:
		return "↑/↓ navigate · r refresh · Enter to select · Esc to back"
	case mcpStageTools, mcpStageLogs:
		return "Esc to back"
	case mcpStageConfirmRemove, mcpStageConfirmClearAuth:
		return "Enter to select · y confirm · n cancel · Esc to back"
	default:
		if len(p.snapshot.servers) == 0 {
			return "↑/↓ navigate · Enter to confirm · Esc to cancel"
		}
		return "↑/↓ navigate · r refresh · Enter for details · Esc to close"
	}
}

func (p *mcpManager) renderList(width int) string {
	return p.renderListBody(width).text
}

func (p *mcpManager) renderListBody(width int) panelBody {
	var b panelBuilder
	fmt.Fprintf(&b, "%s\n", viewHeader("Manage MCP servers"))
	fmt.Fprintf(&b, "%s\n\n", viewMeta(fmt.Sprintf("%d servers", len(p.snapshot.servers))))
	if p.snapshot.err != "" {
		fmt.Fprintf(&b, "%s\n\n", yellow("config: "+p.snapshot.err))
	}
	if len(p.snapshot.servers) == 0 {
		b.WriteString(viewMeta("No MCP servers configured. Use /mcp add <name> ... to add one.") + "\n\n")
		return b.body()
	}
	start, end := visibleRange(len(p.snapshot.servers), p.sel, mcpListMaxRows)
	if start > 0 {
		fmt.Fprintf(&b, "%s\n", viewMeta(fmt.Sprintf("↑ %d more above", start)))
	}
	lastGroup := ""
	for i := start; i < end; i++ {
		s := p.snapshot.servers[i]
		group := mcpServerGroupLabel(s)
		if group != lastGroup {
			if lastGroup != "" {
				b.WriteByte('\n')
			}
			header := group
			if group == "Global MCPs" && p.snapshot.configPath != "" {
				header += " (" + p.snapshot.configPath + ")"
			}
			fmt.Fprintf(&b, "  %s\n", bold(header))
			lastGroup = group
		}
		b.MarkFocus(i == p.sel, i)
		b.WriteString(p.renderListRow(i, s, width) + "\n")
	}
	if end < len(p.snapshot.servers) {
		fmt.Fprintf(&b, "%s\n", viewMeta(fmt.Sprintf("↓ %d more below", len(p.snapshot.servers)-end)))
	}
	return b.body().trimTrailingNewlines()
}

func (p *mcpManager) renderListRow(i int, s mcpServerView, width int) string {
	prefix := "    "
	if i == p.sel {
		prefix = accent("  › ")
	}
	nameWidth := min(28, max(12, width/3))
	name := compactMiddle(s.Name, nameWidth)
	status := mcpStatusLabel(s)
	meta := fmt.Sprintf("%s · %s", status, countText(s.Tools, "tool"))
	if s.Prompts > 0 {
		meta += " · " + countText(s.Prompts, "prompt")
	}
	if s.Resources > 0 {
		meta += " · " + countText(s.Resources, "resource")
	}
	if s.Transport != "" {
		meta = s.Transport + " · " + meta
	}
	used := visibleWidth(prefix) + nameWidth + 3
	meta = viewCompactText(meta, viewBudget(width, used))
	name = padRight(name, nameWidth)
	if i == p.sel {
		name = bold(name)
	}
	return fmt.Sprintf("%s%s · %s", prefix, name, viewMeta(meta))
}

func (p *mcpManager) renderDetail(width int) string {
	return p.renderDetailBody(width).text
}

func (p *mcpManager) renderDetailBody(width int) panelBody {
	v, ok := p.selectedServer()
	if !ok {
		return panelBody{text: "MCP server not found\n\n" + dim("Esc to back")}
	}
	actions := mcpActionsFor(v, p.snapshot.configPath)
	if p.action >= len(actions) {
		p.action = max(0, len(actions)-1)
	}
	var b panelBuilder
	fmt.Fprintf(&b, "%s MCP Server\n\n", bold(titleText(v.Name)))
	writeMCPDetailField(&b.Builder, "Status", mcpStatusLabel(v))
	if auth := mcpAuthLabel(v); auth != "" {
		writeMCPDetailField(&b.Builder, "Auth", auth)
	}
	writeMCPDetailField(&b.Builder, "Transport", fallbackText(v.Transport, "unknown"))
	if v.BuiltIn {
		writeMCPDetailField(&b.Builder, "Config location", "built-in")
	} else if v.Source == config.MCPSourceProjectMCPJSON {
		writeMCPDetailField(&b.Builder, "Config location", "current project .mcp.json")
	} else if v.Source == config.MCPSourceProjectConfig {
		writeMCPDetailField(&b.Builder, "Config location", "current project reasonix.toml")
	} else {
		loc := fallbackText(p.snapshot.configPath, "not saved")
		if loc != "not saved" {
			loc = viewCompactPath(loc, viewBudget(width, 18))
		}
		writeMCPDetailField(&b.Builder, "Config location", loc)
	}
	writeMCPDetailField(&b.Builder, "Capabilities", mcpCapabilitiesText(v))
	writeMCPDetailField(&b.Builder, "Tools", countText(v.Tools, "tool"))
	if line := mcpCommandLine(v); line != "" {
		writeMCPDetailField(&b.Builder, mcpCommandLabel(v), viewCompactText(line, viewBudget(width, 18)))
	}
	if len(v.EnvKeys) > 0 {
		writeMCPDetailField(&b.Builder, "Env", strings.Join(v.EnvKeys, ", "))
	}
	if v.Error != "" {
		writeMCPDetailField(&b.Builder, "Error", viewCompactText(v.Error, viewBudget(width, 18)))
	}
	b.WriteByte('\n')
	if len(actions) == 0 {
		b.WriteString(viewMeta("No actions available.") + "\n")
	} else {
		for i, a := range actions {
			b.MarkFocus(i == p.action, i)
			b.WriteString(rowLine(i == p.action, i+1, "", a.label, false) + "\n")
		}
	}
	return b.body().trimTrailingNewlines()
}

func (p *mcpManager) renderToolsBody(width int) panelBody {
	v, ok := p.selectedServer()
	if !ok {
		return panelBody{text: "MCP server not found\n\n" + dim("Esc to back")}
	}
	var b panelBuilder
	fmt.Fprintf(&b, "%s tools\n\n", bold(v.Name))
	if len(v.ToolList) == 0 {
		b.WriteString(viewMeta("Current connection did not return tool details.") + "\n")
	} else {
		limit := min(len(v.ToolList), mcpToolMaxRows)
		for _, t := range v.ToolList[:limit] {
			desc := t.Description
			if t.SchemaError != "" {
				desc = "unavailable: " + t.SchemaError
			}
			desc = viewCompactText(desc, viewBudget(width, 24))
			fmt.Fprintf(&b, "  %-20s %s\n", t.Name, viewMeta(desc))
		}
		if extra := len(v.ToolList) - limit; extra > 0 {
			fmt.Fprintf(&b, "%s\n", viewMore(extra, "tools"))
		}
	}
	return b.body().trimTrailingNewlines()
}

func (p *mcpManager) renderLogsBody(width int) panelBody {
	v, ok := p.selectedServer()
	if !ok {
		return panelBody{text: "MCP server not found\n\n" + dim("Esc to back")}
	}
	var b panelBuilder
	fmt.Fprintf(&b, "%s logs\n\n", bold(v.Name))
	if strings.TrimSpace(v.Error) == "" {
		b.WriteString(viewMeta("No failure log recorded for this MCP.") + "\n")
	} else {
		b.WriteString(viewProtectLines(v.Error, viewBudget(width, 2)) + "\n")
	}
	return b.body().trimTrailingNewlines()
}

func (p *mcpManager) renderConfirmRemoveBody(width int) panelBody {
	v, ok := p.selectedServer()
	if !ok {
		return panelBody{text: "MCP server not found\n\n" + dim("Esc to back")}
	}
	var b panelBuilder
	fmt.Fprintf(&b, "Remove MCP server %q?\n", v.Name)
	b.WriteString(viewMeta("This removes it from Reasonix config. It cannot be undone from this panel.") + "\n\n")
	b.MarkFocus(p.confirm == 0, 0)
	b.WriteString(rowLine(p.confirm == 0, 1, "", "Confirm remove", false) + "\n")
	b.MarkFocus(p.confirm == 1, 1)
	b.WriteString(rowLine(p.confirm == 1, 2, "", "Cancel", false) + "\n")
	return b.body().trimTrailingNewlines()
}

func (p *mcpManager) renderConfirmClearAuth(width int) string {
	return p.renderConfirmClearAuthBody(width).text
}

func (p *mcpManager) renderConfirmClearAuthBody(width int) panelBody {
	v, ok := p.selectedServer()
	if !ok {
		return panelBody{text: "MCP server not found\n\n" + dim("Esc to back")}
	}
	var b panelBuilder
	fmt.Fprintf(&b, "Clear authentication for MCP server %q?\n", v.Name)
	hint := "This removes local auth-like headers, environment values, and URL tokens; the server stays in config."
	b.WriteString(viewMeta(viewCompactText(hint, viewBudget(width, 0))) + "\n\n")
	b.MarkFocus(p.confirm == 0, 0)
	b.WriteString(rowLine(p.confirm == 0, 1, "", "Confirm clear authentication", false) + "\n")
	b.MarkFocus(p.confirm == 1, 1)
	b.WriteString(rowLine(p.confirm == 1, 2, "", "Cancel", false) + "\n")
	return b.body().trimTrailingNewlines()
}

func mcpActionsFor(v mcpServerView, configPath string) []mcpActionItem {
	var out []mcpActionItem
	managed := v.BuiltIn || v.Source == config.MCPSourcePluginPackage
	if v.Tools > 0 || len(v.ToolList) > 0 {
		out = append(out, mcpActionItem{mcpActionViewTools, "View tools"})
	}
	if v.Status == "failed" {
		if mcpAuthStatus(v) == mcpdiag.AuthRequired {
			out = append(out, mcpActionItem{mcpActionAuth, "Authenticate"})
		} else {
			out = append(out, mcpActionItem{mcpActionConnect, "Retry"})
		}
		if mcpCanClearAuth(v) {
			out = append(out, mcpActionItem{mcpActionClearAuth, "Clear authentication"})
		}
		out = appendMCPFailureSecondaryActions(out, v, configPath)
		return out
	}
	switch v.Status {
	case "connected":
		out = append(out, mcpActionItem{mcpActionConnect, "Reconnect"})
	case "deferred":
		out = append(out, mcpActionItem{mcpActionConnect, "Reconnect"})
	case "disabled":
		out = append(out, mcpActionItem{mcpActionConnect, "Enable and connect"})
	}
	out = appendMCPConfigActions(out, v, configPath)
	if mcpCanClearAuth(v) {
		out = append(out, mcpActionItem{mcpActionClearAuth, "Clear authentication"})
	}
	if v.Status != "disabled" {
		out = append(out, mcpActionItem{mcpActionDisable, "Disable for this session"})
	}
	if !managed {
		out = append(out, mcpActionItem{mcpActionRemove, "Remove server"})
	}
	return out
}

func appendMCPFailureSecondaryActions(out []mcpActionItem, v mcpServerView, configPath string) []mcpActionItem {
	managed := v.BuiltIn || v.Source == config.MCPSourcePluginPackage
	if strings.TrimSpace(v.Error) != "" {
		out = append(out, mcpActionItem{mcpActionLogs, "View logs"})
	}
	out = appendMCPConfigActions(out, v, configPath)
	if v.Status != "disabled" {
		out = append(out, mcpActionItem{mcpActionDisable, "Disable for this session"})
	}
	if !managed {
		out = append(out, mcpActionItem{mcpActionRemove, "Remove server"})
	}
	return out
}

func appendMCPConfigActions(out []mcpActionItem, v mcpServerView, configPath string) []mcpActionItem {
	if v.Configured {
		path := mcpConfigPathForView(v, configPath)
		if !v.BuiltIn && v.Source != config.MCPSourcePluginPackage && path != "" {
			out = append(out, mcpActionItem{mcpActionEdit, "Edit config"})
		}
	}
	return out
}

func mcpConfigPathForView(v mcpServerView, fallback string) string {
	if path := strings.TrimSpace(v.ConfigPath); path != "" {
		return path
	}
	return strings.TrimSpace(fallback)
}

func writeMCPDetailField(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(b, "%-16s %s\n", label+":", value)
}

func mcpStatusLabel(v mcpServerView) string {
	switch {
	case v.Status == "connected":
		return green("✓ connected")
	case v.Status == "failed" && mcpAuthStatus(v) == mcpdiag.AuthRequired:
		return yellow("⚠ needs authentication")
	case v.Status == "failed":
		return red("✕ failed")
	case v.Status == "deferred":
		return "○ preparing in background"
	case v.Status == "initializing":
		return "◌ connecting..."
	case v.Status == "disabled":
		return "○ disabled"
	default:
		return viewMeta("unknown")
	}
}

func mcpServerGroupLabel(v mcpServerView) string {
	switch mcpServerGroupRank(v) {
	case 0:
		return "Managed MCPs"
	case 1:
		return "Project MCPs"
	default:
		return "Global MCPs"
	}
}

func mcpAuthLabel(v mcpServerView) string {
	switch {
	case v.Status == "connected":
		return green("✓ authenticated")
	case mcpAuthStatus(v) == mcpdiag.AuthRequired:
		return red("✕ not authenticated")
	case mcpAuthStatus(v) == mcpdiag.AuthPossible:
		return yellow("may need authorization")
	default:
		return ""
	}
}

func mcpCapabilitiesText(v mcpServerView) string {
	var caps []string
	if v.HasTools || v.Tools > 0 {
		caps = append(caps, "tools")
	}
	if v.Prompts > 0 {
		caps = append(caps, "prompts")
	}
	if v.Resources > 0 {
		caps = append(caps, "resources")
	}
	if len(caps) == 0 {
		return "none"
	}
	return strings.Join(caps, ", ")
}

func mcpCommandLabel(v mcpServerView) string {
	if v.Transport == "http" || v.Transport == "sse" {
		return "URL"
	}
	return "Command"
}

func mcpCommandLine(v mcpServerView) string {
	if v.Transport == "http" || v.Transport == "sse" {
		return strings.TrimSpace(v.URL)
	}
	return strings.TrimSpace(v.Command + " " + strings.Join(v.Args, " "))
}
