package cli

import (
	"fmt"
	"os"
	"strings"

	"reasonix/internal/boot"
	"reasonix/internal/config"
)

func mcpList() int {
	workspace := mcpCLIWorkspaceRoot()
	cfg, err := config.LoadForRoot(workspace)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	listed, pending := 0, false
	for _, p := range cfg.Plugins {
		typ := p.Type
		if typ == "" {
			typ = "stdio"
		}
		decision := config.MCPServerDecision(p, workspace)
		pending = pending || decision.AwaitsUser()
		auto := " [" + decision.Code() + "]"
		fmt.Printf("%-16s (%s)%s  %s\n", p.Name, typ, auto, config.MCPLaunchLine(p))
		listed++
	}
	if listed == 0 {
		fmt.Println("no MCP servers configured")
	}
	if pending {
		fmt.Println("project-declared servers stay off until you review them (`reasonix mcp get <name>`) and run `reasonix mcp enable <name>`")
	}
	return 0
}

// mcpCLIWorkspaceRoot is the workspace a session started here would use, so a
// decision recorded from the command line reaches that session.
func mcpCLIWorkspaceRoot() string {
	if root := boot.ResolveWorkspaceRoot(""); strings.TrimSpace(root) != "" {
		return root
	}
	return "."
}
