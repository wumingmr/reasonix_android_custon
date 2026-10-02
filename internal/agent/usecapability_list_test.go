package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"reasonix/internal/capability"
	"reasonix/internal/config"
	"reasonix/internal/plugin"
	"reasonix/internal/skill"
	"reasonix/internal/tool"
)

func TestUseCapabilityListSummarizesMCPWithoutExpandingCachedDirectories(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	specs := []plugin.Spec{
		{Name: "disabled", Type: "stdio", Command: "disabled-mcp", Authorized: true},
		{Name: "enabled", Type: "stdio", Command: "enabled-mcp", Authorized: true},
	}
	entries := []config.PluginEntry{
		{Name: "disabled", Type: "stdio", Command: "disabled-mcp", Source: config.MCPSourceUserConfig},
		{Name: "enabled", Type: "stdio", Command: "enabled-mcp", Source: config.MCPSourceUserConfig},
	}
	for _, spec := range specs {
		cached := make([]plugin.CachedTool, 64)
		for i := range cached {
			cached[i] = plugin.CachedTool{
				Name:        fmt.Sprintf("tool_%03d", i),
				Description: fmt.Sprintf("catalog-bloat-sentinel-%s-%03d-%s", spec.Name, i, strings.Repeat("x", 256)),
				ReadOnly:    true,
			}
		}
		if err := plugin.SaveCachedSchema(spec.Name, plugin.CachedSchema{
			CacheKey: plugin.SchemaCacheKey(spec),
			Tools:    cached,
		}); err != nil {
			t.Fatal(err)
		}
	}

	host := plugin.NewHost()
	defer host.Close()
	reg := tool.NewRegistry()
	var runtime *MCPCapabilityRuntime
	catalogFn := func() capability.Catalog {
		plugins, cached, keyOK, disabled, proxyTools := runtime.CapabilityCatalogState()
		catalog := capability.BuildCatalog(capability.CatalogOptions{
			Tools:       reg.AllContractEntries(),
			Plugins:     plugins,
			Disabled:    disabled,
			CachedTools: cached,
			CacheKeyOK:  keyOK,
			ProxyTools:  proxyTools,
		})
		catalog.Entries = append(catalog.Entries, capability.Entry{
			ID: "skill:review", Kind: capability.KindSkill, Name: "review", Status: capability.StatusReady,
		})
		return catalog
	}
	runtime = NewMCPCapabilityRuntime(context.Background(), host, specs, reg, catalogFn)
	runtime.ConfigureServers(entries, specs, map[string]bool{"enabled": true})
	frontend := runtime.NewFrontend(nil, nil)

	out, err := frontend.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Capabilities []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"capabilities"`
		Servers []listServerInfo `json:"servers"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode list result: %v\n%s", err, out)
	}
	if len(payload.Capabilities) != 1 || payload.Capabilities[0].ID != "skill:review" {
		t.Fatalf("list expanded MCP entries instead of keeping only non-MCP capabilities: %+v", payload.Capabilities)
	}
	if len(payload.Servers) != 2 || payload.Servers[0].Name != "disabled" || payload.Servers[0].Status != "disabled" || payload.Servers[1].Name != "enabled" || payload.Servers[1].Status != "configured" {
		t.Fatalf("server summaries = %+v, want disabled/configured in stable name order", payload.Servers)
	}
	if strings.Contains(out, "catalog-bloat-sentinel") {
		t.Fatalf("list leaked concrete MCP directory entries:\n%s", out)
	}
	if len(out) >= 4096 {
		t.Fatalf("summary list grew with cached tool descriptions: bytes=%d", len(out))
	}
	t.Logf("compact list bytes=%d for %d cached MCP tools", len(out), len(specs)*64)

	inspected, err := frontend.Execute(context.Background(), json.RawMessage(`{"action":"inspect","capability_id":"mcp-server:enabled"}`))
	if err != nil || !strings.Contains(inspected, "catalog-bloat-sentinel-enabled-000") {
		t.Fatalf("inspect did not preserve the selected server's cached directory: %v\n%s", err, inspected)
	}
	if host.HasClient("enabled") {
		t.Fatal("inspect started the selected MCP server")
	}
	disabledInspect, err := frontend.Execute(context.Background(), json.RawMessage(`{"action":"inspect","capability_id":"mcp-server:disabled"}`))
	if err != nil || !strings.Contains(disabledInspect, "disabled") || strings.Contains(disabledInspect, "catalog-bloat-sentinel") {
		t.Fatalf("disabled inspect exposed a non-actionable cached directory: %v\n%s", err, disabledInspect)
	}
}

func TestUseCapabilityListPagesOneImmutableCatalogVersion(t *testing.T) {
	skills := make([]skill.Skill, 123)
	for i := range skills {
		skills[i] = skill.Skill{Name: fmt.Sprintf("skill-%03d", i), Description: "candidate", Scope: skill.ScopeProject}
	}
	revision := 0
	catalogFn := func() capability.Catalog {
		current := append([]skill.Skill(nil), skills...)
		if revision > 0 {
			current = append(current, skill.Skill{Name: "new", Description: "candidate", Scope: skill.ScopeProject})
		}
		return capability.BuildCatalog(capability.CatalogOptions{Skills: current})
	}
	runtime := NewMCPCapabilityRuntime(context.Background(), nil, nil, tool.NewRegistry(), catalogFn)
	frontend := runtime.NewFrontend(nil, nil)

	type page struct {
		Capabilities []struct {
			ID string `json:"id"`
		} `json:"capabilities"`
		CatalogVersion string `json:"catalog_version"`
		NextCursor     string `json:"next_cursor"`
		Truncated      bool   `json:"truncated"`
	}
	read := func(raw string) page {
		t.Helper()
		out, err := frontend.Execute(context.Background(), json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		var got page
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := read(`{"action":"list"}`)
	if len(first.Capabilities) != 50 || !first.Truncated || first.NextCursor == "" || first.CatalogVersion == "" {
		t.Fatalf("first page = %+v", first)
	}
	second := read(fmt.Sprintf(`{"action":"list","cursor":%q,"limit":50}`, first.NextCursor))
	if len(second.Capabilities) != 50 || second.CatalogVersion != first.CatalogVersion || second.NextCursor == "" {
		t.Fatalf("second page = %+v", second)
	}
	third := read(fmt.Sprintf(`{"action":"list","cursor":%q,"limit":50}`, second.NextCursor))
	if len(third.Capabilities) != 23 || third.Truncated || third.NextCursor != "" || third.CatalogVersion != first.CatalogVersion {
		t.Fatalf("third page = %+v", third)
	}

	revision++
	if _, err := frontend.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"action":"list","cursor":%q}`, first.NextCursor))); err == nil || !strings.Contains(err.Error(), "cursor expired") {
		t.Fatalf("old cursor survived catalog replacement: %v", err)
	}
}

func TestUseCapabilityListLimitAlsoBoundsServerSummaries(t *testing.T) {
	specs := make([]plugin.Spec, 120)
	entries := make([]config.PluginEntry, 120)
	for i := range specs {
		name := fmt.Sprintf("server-%03d", i)
		specs[i] = plugin.Spec{Name: name, Type: "stdio", Command: "unused", Authorized: true}
		entries[i] = config.PluginEntry{Name: name, Type: "stdio", Command: "unused"}
	}
	runtime := NewMCPCapabilityRuntime(context.Background(), nil, specs, tool.NewRegistry(), func() capability.Catalog {
		return capability.BuildCatalog(capability.CatalogOptions{Plugins: entries})
	})
	runtime.ConfigureServers(entries, specs, nil)
	frontend := runtime.NewFrontend(nil, nil)

	var first struct {
		Servers    []listServerInfo `json:"servers"`
		NextCursor string           `json:"next_cursor"`
		Truncated  bool             `json:"truncated"`
	}
	out, err := frontend.Execute(context.Background(), json.RawMessage(`{"action":"list","limit":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Servers) != 50 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first server page = %+v", first)
	}
	var second struct {
		Servers []listServerInfo `json:"servers"`
	}
	out, err = frontend.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"action":"list","limit":50,"cursor":%q}`, first.NextCursor)))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Servers) != 50 || second.Servers[0].Name != "server-050" {
		t.Fatalf("second server page = %+v", second.Servers)
	}
}

func TestUseCapabilitySearchLargeCatalogHonorsSmallLimit(t *testing.T) {
	skills := make([]skill.Skill, 10_000)
	for i := range skills {
		skills[i] = skill.Skill{Name: fmt.Sprintf("catalog-%05d", i), Description: "large catalog candidate", Scope: skill.ScopeProject}
	}
	catalogFn := func() capability.Catalog {
		return capability.BuildCatalog(capability.CatalogOptions{Skills: skills})
	}
	runtime := NewMCPCapabilityRuntime(context.Background(), nil, nil, tool.NewRegistry(), catalogFn)
	frontend := runtime.NewFrontend(nil, nil)
	out, err := frontend.Execute(context.Background(), json.RawMessage(`{"action":"search","query":"catalog","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil || len(payload.Results) != 1 {
		t.Fatalf("large catalog result count=%d err=%v", len(payload.Results), err)
	}
}
