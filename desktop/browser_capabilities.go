package main

import (
	"context"
	"encoding/json"
	"fmt"
)

func (e *hostBrowserExecutor) rememberFilePreview(ctx context.Context, tabID string, request FileBrowserPreviewRequest) {
	// Optional metadata, never an authorization or a reusable preview token.
	_ = e.call(ctx, "host/browser.preview.remember", map[string]any{"tabId": tabID, "source": request.Source, "path": request.Path, "toolCallId": request.ToolCallID}, nil)
}

func (e *tabBrowserExecutor) BrowserCapability(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
	current, err := e.current()
	if err != nil {
		return nil, err
	}
	return current.BrowserCapability(ctx, capability, args)
}

func (e *hostBrowserExecutor) BrowserCapability(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
	var supported struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := e.call(ctx, "host/browser.capabilities", nil, &supported); err != nil {
		return nil, fmt.Errorf("capability_unsupported: %s: %w", capability, err)
	}
	if !supported.Capabilities[capability] {
		return nil, fmt.Errorf("capability_unsupported: %s", capability)
	}
	var params map[string]any
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, err
	}
	var out json.RawMessage
	var err error
	action, _ := params["action"].(string)
	if capability == "record" && action == "start" {
		directory, err := e.captureDir()
		if err != nil {
			return nil, err
		}
		params["directory"] = directory
	}
	switch capability {
	case "query", "wait", "diagnostics":
		err = e.call(ctx, "host/browser."+capability, params, &out)
	case "viewport", "pointer", "record":
		if action == "get" || action == "status" {
			err = e.call(ctx, "host/browser."+capability, params, &out)
		} else {
			id, _ := params["operationId"].(string)
			tab, _ := params["tabId"].(string)
			err = e.write(ctx, id, capability+":"+action, tab, args, "host/browser."+capability, params, &out)
		}
	default:
		return nil, fmt.Errorf("capability_unsupported: %s", capability)
	}
	return out, err
}
