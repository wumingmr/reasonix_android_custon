package main

import (
	"encoding/json"
	"testing"
)

func TestClearRemotePendingPromptExactPreservesReplacement(t *testing.T) {
	tab := &remoteTab{
		id: "remote-1", ref: RemoteTabRef{HostID: "host-a"}, gen: 7,
		session: remoteTabSessionState{sessionID: "session-a"},
		pendingEvents: map[string]json.RawMessage{
			"ask_request:1": json.RawMessage(`{"kind":"ask_request","runtimeEpoch":"runtime-new","ask":{"id":"1","turnId":"turn-new","questions":[]}}`),
		},
		runtime: remoteTabRuntimeState{pendingPrompt: true},
	}
	a := &App{remoteTabs: map[string]*remoteTab{tab.id: tab}, remoteEventHook: func(string, any) {}}
	old := RemotePromptTarget{TabID: tab.id, HostID: "host-a", SessionID: "session-a", SessionGeneration: 7,
		PromptID: "1", TurnID: "turn-old", RuntimeEpoch: "runtime-old", Kind: "ask"}
	a.clearRemotePendingPromptExact(old)
	if len(tab.pendingEvents) != 1 || !tab.runtime.pendingPrompt {
		t.Fatalf("old completion removed replacement: pending=%d runtime=%+v", len(tab.pendingEvents), tab.runtime)
	}
	current := old
	current.TurnID, current.RuntimeEpoch = "turn-new", "runtime-new"
	a.clearRemotePendingPromptExact(current)
	if len(tab.pendingEvents) != 0 || tab.runtime.pendingPrompt {
		t.Fatalf("current completion did not clear its request: pending=%d runtime=%+v", len(tab.pendingEvents), tab.runtime)
	}
}

func TestRemotePendingEventKeyIncludesMCPIdentity(t *testing.T) {
	frame := json.RawMessage(`{"kind":"mcp_interaction","mcpInteraction":{"id":"elicitation-1","turnId":"turn-1"}}`)
	if got := remotePendingEventKey("mcp_interaction", frame); got != "mcp_interaction:elicitation-1" {
		t.Fatalf("pending MCP key = %q", got)
	}
}
