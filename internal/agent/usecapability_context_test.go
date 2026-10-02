package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/capability"
	"reasonix/internal/tool"
)

type capabilityContextKey struct{}

type contextualDiscoveryTool struct{ fakeTool }

func (contextualDiscoveryTool) ProviderVisible(ctx context.Context) bool {
	return ctx.Value(capabilityContextKey{}) == true
}

func (contextualDiscoveryTool) UnavailableReason(context.Context) string {
	return "the host has disabled this capability"
}

func TestCapabilityDiscoveryUsesCurrentExecutionContext(t *testing.T) {
	reg := tool.NewRegistry()
	target := contextualDiscoveryTool{fakeTool{name: "contextual_reader", readOnly: true}}
	reg.Add(target)
	reg.SetProviderVisibleTools([]string{"use_capability"})
	cat := capability.BuildCatalog(capability.CatalogOptions{Tools: reg.CapabilityContractEntries()})
	proxy := NewUseCapabilityTool(context.Background(), nil, nil, reg, nil, nil, func() capability.Catalog { return cat })
	before := string(proxy.Schema())
	for _, available := range []bool{false, true, false} {
		ctx := context.WithValue(context.Background(), capabilityContextKey{}, available)
		for _, action := range []string{"search", "list", "inspect"} {
			args, _ := json.Marshal(map[string]any{"action": action, "query": "contextual_reader", "capability_id": "tool:contextual_reader"})
			out, err := proxy.Execute(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			want := `"status": "disabled"`
			if available {
				want = `"status": "ready"`
			}
			if !strings.Contains(out, want) || (!available && !strings.Contains(out, "the host has disabled this capability")) {
				t.Errorf("%s available=%v: %s", action, available, out)
			}
			if action == "inspect" && !strings.Contains(out, `"input_schema"`) {
				t.Errorf("inspect must disclose the registered tool contract: %s", out)
			}
		}
		outcome, blocked := contextualToolGateOutcome(ctx, target, target.Name())
		if blocked == available || (blocked && !strings.Contains(outcome.output, "the host has disabled this capability")) {
			t.Errorf("execution gate disagrees with discovery: %+v, blocked=%v", outcome, blocked)
		}
	}
	if string(proxy.Schema()) != before || cat.Entries[0].Status != capability.StatusReady {
		t.Fatal("context checks mutated the stable tool schema or catalog")
	}
}
