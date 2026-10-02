package boot

import (
	"fmt"
	"strings"
	"sync"

	"reasonix/internal/config"
	"reasonix/internal/event"
)

// projectMCPNoticesShown keeps the notice to once per process for each
// declaration, so rebuilding a controller does not repeat it.
var projectMCPNoticesShown sync.Map

// emitProjectMCPDecisionNotice tells the user which project-declared servers are
// off pending their decision, with what each would run.
func emitProjectMCPDecisionNotice(sink event.Sink, cfg *config.Config, root string) {
	if sink == nil || cfg == nil {
		return
	}
	var lines []string
	for _, p := range cfg.Plugins {
		d := config.MCPServerDecision(p, root)
		if !d.AwaitsUser() {
			continue
		}
		key := strings.Join([]string{config.ReasonixHomeDir(), root, p.Name, d.Code(), config.MCPLaunchLine(p)}, "\x00")
		if _, seen := projectMCPNoticesShown.LoadOrStore(key, true); seen {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s [%s]: %s", p.Name, d.Code(), config.MCPLaunchLine(p)))
	}
	if len(lines) == 0 {
		return
	}
	sink.Emit(event.Event{
		Kind: event.Notice, Level: event.LevelWarn,
		Text: "This project declares MCP servers that stay off until you enable them.",
		Detail: strings.Join(lines, "\n") +
			"\nReview each one (full declaration: `reasonix mcp get <name>`), then enable it with `reasonix mcp enable <name>`, /mcp connect, or the MCP panel.",
	})
}
