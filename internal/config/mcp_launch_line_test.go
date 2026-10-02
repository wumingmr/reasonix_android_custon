package config

import (
	"strings"
	"testing"
)

// The line a user approves shows everything that changes which code runs.
func TestLaunchLineShowsEnvThatChangesWhatRuns(t *testing.T) {
	line := MCPLaunchLine(PluginEntry{
		Name: "repo", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
		Env: map[string]string{"NODE_OPTIONS": "--require ./x.js", "DYLD_INSERT_LIBRARIES": "./x.dylib", "API_TOKEN": "secret-value"},
	})
	for _, want := range []string{`NODE_OPTIONS="--require ./x.js"`, `DYLD_INSERT_LIBRARIES="./x.dylib"`, "API_TOKEN=", "npx -y @modelcontextprotocol/server-filesystem ."} {
		if !strings.Contains(line, want) {
			t.Fatalf("launch line %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "secret-value") {
		t.Fatalf("launch line %q shows a value that does not change what runs", line)
	}
}

func TestLaunchLineRedactsEndpointCredentials(t *testing.T) {
	line := MCPLaunchLine(PluginEntry{
		Name: "repo", Type: "http", URL: "https://mcp.example/mcp?access_token=SECRET1&workspace=main",
		Headers: map[string]string{"Authorization": "Bearer SECRET2"},
		Env:     map[string]string{"npm_config_registry": "https://registry.example/"},
	})
	if strings.Contains(line, "SECRET") {
		t.Fatalf("launch line leaks a credential: %s", line)
	}
	for _, want := range []string{"workspace=main", "headers:Authorization", `npm_config_registry="https://registry.example/"`} {
		if !strings.Contains(line, want) {
			t.Fatalf("launch line %q lacks %q", line, want)
		}
	}
}
