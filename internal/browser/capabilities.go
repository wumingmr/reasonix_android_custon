package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

// CapabilityExecutor is optional. CDP and older remote implementations retain
// the original Executor contract and never silently route enhanced calls elsewhere.
type CapabilityExecutor interface {
	BrowserCapability(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

// CapabilityTools is registered only in the on-demand inventory. Its schemas do
// not replace any existing browser tool or alter the permanent provider prefix.
func CapabilityTools(exec Executor) []tool.Tool {
	query := []property{tabIDProp(), documentTokenProp(), str("role", "Exact ARIA role."), str("name", "Exact accessible name, paired with role."), str("text", "Exact text."), str("testId", "Exact data-testid."), str("containerRef", "Optional existing container ref."), str("frameRef", "Optional existing element ref identifying the frame to search."), enum("state", "Optional element state.", "visible", "hidden", "enabled", "disabled", "checked", "editable")}
	return []tool.Tool{
		newCapability(exec, "query", "Find elements by semantic criteria within the current snapshot. Returns count and refs; multiple results are ambiguous and must be refined before acting.", false, []string{"tabId", "documentToken"}, query...),
		newCapability(exec, "wait", "Wait with a bounded deadline for DOMContentLoaded, an exact URL, or a semantic element state. Continuous network activity does not prevent completion.", false, []string{"tabId"}, append(query, str("url", "Exact target URL."), bounded(integer("timeoutMs", "Maximum wait, default 3000 ms."), 1, 8000))...),
		newCapability(exec, "viewport", "Read, set or reset the CSS viewport. Mutations invalidate previous coordinate observations and are recorded operations.", true, []string{"tabId", "action"}, tabIDProp(), enum("action", "Viewport operation.", "get", "set", "reset"), operationIDProp(), bounded(integer("width", "CSS width."), 320, 3840), bounded(integer("height", "CSS height."), 320, 2160)),
		newCapability(exec, "pointer", "Hover, drag, or click coordinates grounded in a current screenshot observation. Never reuse stale observations or retry unknown outcomes.", true, []string{"tabId", "operationId", "action", "documentToken"}, tabIDProp(), operationIDProp(), documentTokenProp(), enum("action", "Pointer operation.", "hover", "drag", "click"), refProp("Source element reference."), str("targetRef", "Drag destination reference."), str("observationToken", "Screenshot observation token required for coordinates."), integer("x", "CSS x coordinate."), integer("y", "CSS y coordinate.")),
		newCapability(exec, "diagnostics", "Read bounded, redacted page errors and failed requests. Unavailable collection is distinct from zero errors.", false, []string{"tabId"}, tabIDProp(), integer("after", "Return entries after this sequence cursor."), enum("kind", "Optional event type.", "console", "exception", "network", "navigation")),
		newCapability(exec, "record", "Record the existing task tab without audio. Query status, stop and finalize, or cancel. Only completed validated WebM is an artifact.", true, []string{"tabId", "action"}, tabIDProp(), enum("action", "Recording operation.", "start", "status", "stop", "cancel"), operationIDProp(), str("recordingId", "ID from start."), bounded(integer("durationSeconds", "Default 20, maximum 90."), 1, 90)),
	}
}

type capabilityTool struct {
	base
	capability string
	write      bool
	fields     map[string]bool
}

func newCapability(exec Executor, name, description string, write bool, required []string, props ...property) tool.Tool {
	fields := map[string]bool{}
	for _, p := range props {
		fields[p.name] = true
	}
	return capabilityTool{base: base{exec: exec, name: "browser_" + name, description: description, schema: objectSchema(required, props...), snip: listSnip}, capability: name, write: write, fields: fields}
}
func (t capabilityTool) ReadOnly() bool     { return !t.write }
func (t capabilityTool) PlanModeSafe() bool { return !t.write }
func (t capabilityTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := t.ready(ctx); err != nil {
		return "", err
	}
	executor, ok := t.exec.(CapabilityExecutor)
	if !ok {
		return "", fmt.Errorf("capability_unsupported: attached browser does not support %s", t.Name())
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}
	for key := range params {
		if !t.fields[key] {
			return "", fmt.Errorf("unknown field %q", key)
		}
	}
	var tabID, operationID, action string
	_ = json.Unmarshal(params["tabId"], &tabID)
	if err := requireTab(tabID); err != nil {
		return "", err
	}
	_ = json.Unmarshal(params["operationId"], &operationID)
	_ = json.Unmarshal(params["action"], &action)
	if t.write && action != "get" && action != "status" && !operationIDRe.MatchString(operationID) {
		return "", fmt.Errorf("a valid fresh operationId is required")
	}
	result, err := executor.BrowserCapability(ctx, t.capability, args)
	if err != nil {
		return "", translate(err, t.Name())
	}
	if !json.Valid(result) {
		return "", fmt.Errorf("invalid browser capability response")
	}
	return strings.TrimSpace(string(result)), nil
}
