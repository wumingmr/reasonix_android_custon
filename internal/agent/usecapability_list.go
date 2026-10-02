package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"reasonix/internal/capability"
	"reasonix/internal/plugin"
)

// listServerInfo is one configured MCP server entry returned by action=list.
// It never starts a server or opens a network connection.
type listServerInfo struct {
	Name         string `json:"name"`
	CapabilityID string `json:"capability_id"`
	Status       string `json:"status"`
	Authorized   bool   `json:"authorized"`
	Connected    bool   `json:"connected"`
}

func (t *UseCapabilityTool) listCapabilitiesPage(ctx context.Context, limit int, cursor string) (string, error) {
	if limit == 0 {
		limit = 50
	}
	type capInfo struct {
		ID                string `json:"id"`
		Kind              string `json:"kind"`
		Name              string `json:"name"`
		Status            string `json:"status,omitempty"`
		ReadOnly          bool   `json:"read_only,omitempty"`
		Description       string `json:"description,omitempty"`
		UnavailableReason string `json:"unavailable_reason,omitempty"`
	}
	var caps []capInfo
	if t.currentToolResultTarget() != nil {
		caps = append(caps, capInfo{
			ID: sessionToolResultCapabilityID, Kind: "session", Name: "tool_result", Status: "ready", ReadOnly: true,
			Description: "Read one bounded page from a complete tool result retained in this agent's current session.",
		})
	}
	catalog := t.currentCatalog()
	if len(catalog.Entries) > 0 {
		for _, e := range catalog.Entries {
			e = t.contextualEntry(ctx, e)
			// Servers already have a compact representation below. Keep concrete
			// MCP tools in the internal catalog for routing, inspect, and known-ID
			// calls, but do not inject every cached directory into model context.
			if e.Kind == capability.KindMCPServer || e.Kind == capability.KindMCPTool {
				continue
			}
			// Skip provider-visible core tools — they are already top-level.
			if e.Kind == capability.KindTool && t.registry != nil && t.registry.ProviderVisible(e.ToolName) {
				continue
			}
			caps = append(caps, capInfo{
				ID:                e.ID,
				Kind:              string(e.Kind),
				Name:              e.Name,
				Status:            string(e.Status),
				ReadOnly:          e.ReadOnly,
				Description:       e.Description,
				UnavailableReason: e.FailureReason,
			})
		}
	}
	serversJSON, err := t.listServers()
	if err != nil {
		return "", err
	}
	var serversPayload struct {
		Servers []listServerInfo `json:"servers"`
		Note    string           `json:"note"`
	}
	_ = json.Unmarshal([]byte(serversJSON), &serversPayload)
	total := len(caps) + len(serversPayload.Servers)
	offset := 0
	if strings.TrimSpace(cursor) != "" {
		version, rawOffset, ok := strings.Cut(cursor, ":")
		if !ok || version != catalog.Fingerprint {
			return "", fmt.Errorf("list cursor expired because the capability catalog changed; restart without cursor")
		}
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 || parsed > total {
			return "", fmt.Errorf("invalid list cursor; restart without cursor")
		}
		offset = parsed
	}
	end := min(offset+limit, total)
	capStart, capEnd := min(offset, len(caps)), min(end, len(caps))
	page := caps[capStart:capEnd]
	serverStart := max(0, offset-len(caps))
	serverEnd := max(0, end-len(caps))
	serverPage := serversPayload.Servers[serverStart:serverEnd]
	nextCursor := ""
	if end < total {
		nextCursor = catalog.Fingerprint + ":" + strconv.Itoa(end)
	}
	payload := map[string]any{
		"capabilities":    page,
		"servers":         serverPage,
		"catalog_version": catalog.Fingerprint,
		"next_cursor":     nextCursor,
		"truncated":       nextCursor != "",
		"snapshot_stale":  catalog.Stale,
		"incomplete":      catalog.Incomplete,
		"note":            "This page contains at most limit entries across capabilities and MCP server summaries. Call action=inspect with capability_id=mcp-server:<name> to list one enabled server's tools without starting it, or action=call with a concrete capability_id to invoke a non-core tool, skill, MCP tool, or other catalog entry without changing the provider tool schema.",
	}
	if serversPayload.Note != "" {
		payload["note"] = payload["note"].(string) + " " + serversPayload.Note
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// listServers returns sorted configured MCP server names, status, and
// capability IDs without starting servers. Used by Planner discovery when no
// specific capability route was provided.
func (t *UseCapabilityTool) listServers() (string, error) {
	configured := t.configuredServers()
	list := make([]listServerInfo, 0, len(configured))
	for _, server := range configured {
		spec := server.spec
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		// Apply stored project grants without process/network side effects so
		// list status matches resolve/execute authorization.
		resolved := plugin.ResolveStoredAuthorization(context.Background(), spec)
		connected := server.enabled && resolved.ServerAuthorized() && t.host != nil && t.host.HasClientForSpec(resolved)
		status := "configured"
		if !server.enabled {
			status = server.decision.Code()
		} else if connected {
			status = "ready"
		} else if t.host != nil {
			for _, f := range t.host.Failures() {
				if f.Name == name && strings.TrimSpace(f.Error) != "" {
					status = "failed"
					break
				}
			}
		}
		list = append(list, listServerInfo{
			Name:         name,
			CapabilityID: "mcp-server:" + name,
			Status:       status,
			Authorized:   resolved.ServerAuthorized(),
			Connected:    connected,
		})
	}
	b, err := json.MarshalIndent(map[string]any{
		"servers": list,
		"note":    "list does not start MCP servers. Call action=call on mcp-server:<name> to connect after authorization, or mcp-tool:<server>/<tool> for a concrete tool.",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
