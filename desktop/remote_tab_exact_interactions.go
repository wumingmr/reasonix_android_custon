package main

import (
	"fmt"
	"strings"
)

func (a *App) ResolveRemoteTabPromptExact(target RemotePromptTarget, answer PromptAnswerView) error {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[target.TabID]
	supported := tab != nil && tab.capabilities[serveCapabilityInteractionTargetV1]
	current := tab != nil && tab.ref.HostID == target.HostID && tab.session.sessionID == target.SessionID && tab.gen == target.SessionGeneration
	a.remoteTabMu.Unlock()
	if !supported {
		return fmt.Errorf("this remote Reasonix Serve does not support %s; upgrade it to answer this card safely", serveCapabilityInteractionTargetV1)
	}
	if !current {
		return fmt.Errorf("remote prompt session binding is stale")
	}
	if strings.TrimSpace(target.PromptID) == "" || strings.TrimSpace(target.TurnID) == "" || strings.TrimSpace(target.Kind) == "" {
		return fmt.Errorf("exact remote prompt identity is required")
	}
	if err := a.remoteTabPost(target.TabID, "/resolve-prompt", map[string]any{
		"sessionId": target.SessionID, "promptId": target.PromptID, "turnId": target.TurnID, "runtimeEpoch": target.RuntimeEpoch,
		"kind": target.Kind, "answer": answer,
	}); err != nil {
		return err
	}
	a.clearRemotePendingPromptExact(target)
	return nil
}

func (a *App) SubmitRemoteTabExtensionFormExact(target ExtensionFormTarget, values map[string]any) error {
	a.remoteTabMu.Lock()
	tab := a.remoteTabs[target.TabID]
	supported := tab != nil && tab.capabilities[serveCapabilityExtensionFormInstanceV1]
	current := tab != nil && tab.ref.HostID == target.HostID && tab.session.sessionID == target.SessionID && tab.gen == target.SessionGeneration
	a.remoteTabMu.Unlock()
	if !supported {
		return fmt.Errorf("this remote Reasonix Serve does not support %s; upgrade it to submit this extension form safely", serveCapabilityExtensionFormInstanceV1)
	}
	if !current {
		return fmt.Errorf("remote extension form binding is stale")
	}
	if err := a.remoteTabPost(target.TabID, "/extension-form", map[string]any{
		"sessionId": target.SessionID, "pluginId": target.PluginID, "surfaceId": target.SurfaceID, "generation": target.PluginGeneration,
		"formInstanceId": target.FormInstanceID, "values": values,
	}); err != nil {
		return err
	}
	a.clearRemotePendingExtensionFormExact(target.TabID, target.PluginID, target.SurfaceID, target.FormInstanceID)
	return nil
}
