package installsource

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func testPlanID(t *testing.T, req request, actions []action) string {
	t.Helper()
	id, err := computePlanID(req, actions)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestImportedExecutionInputsInvalidateApproval(t *testing.T) {
	for _, field := range []string{"env", "headers"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("REASONIX_HOME", t.TempDir())
			root := t.TempDir()
			src := filepath.Join(root, ".mcp.json")
			tl := NewTool(Options{ProjectRoot: root, HomeDir: t.TempDir()})
			write := func(value string) {
				writeFile(t, src, `{"mcpServers":{"demo":{"command":"node","args":["server.js"],"`+field+`":{"REVIEW_VALUE":"`+value+`"}}}}`)
			}
			write("approved")
			args := map[string]any{"source": src, "kind": "mcp", "scope": "project"}
			before := execInstall(t, tl, args)
			write("changed")
			after := execInstall(t, tl, args)
			if before.PlanID == after.PlanID {
				t.Fatal("changed execution inputs retained approval identity")
			}
			args["apply"], args["planId"] = true, before.PlanID
			raw, _ := json.Marshal(args)
			if _, err := tl.Execute(context.Background(), raw); err == nil {
				t.Fatal("stale approval accepted")
			}
		})
	}
}
