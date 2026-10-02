package control

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/permissionpreset"
	"reasonix/internal/sandbox"
	"reasonix/internal/session"
)

// SessionGrantSummary is a transport-safe description of an in-memory grant.
// Grants remain session-local and are never converted to persistent rules.
type SessionGrantSummary struct {
	Scope  string `json:"scope"`
	Target string `json:"target"`
}

type PermissionCapabilities struct {
	Backend           string   `json:"backend"`
	Enforcement       string   `json:"enforcement"`
	SupportedPresets  []string `json:"supportedPresets"`
	UnavailableReason string   `json:"unavailableReason,omitempty"`
	WriteIsolation    string   `json:"writeIsolation,omitempty"`
	ReadIsolation     string   `json:"readIsolation,omitempty"`
	NetworkIsolation  string   `json:"networkIsolation,omitempty"`
}

// PermissionSnapshot is the sole user-facing permission state for a session.
type PermissionSnapshot struct {
	SessionID     string                 `json:"sessionId"`
	Generation    uint64                 `json:"generation"`
	Revision      uint64                 `json:"revision"`
	Preset        string                 `json:"preset"`
	WorkspaceRoot string                 `json:"workspaceRoot"`
	Grants        []SessionGrantSummary  `json:"grants"`
	Capabilities  PermissionCapabilities `json:"capabilities"`
}

const (
	presetSandboxProbe int32 = iota
	presetSandboxPinnedOn
	presetSandboxPinnedOff
)

// presetSandbox selects what the offered presets assume about the host
// sandbox; only tests pin it, the product always probes the host.
var presetSandbox atomic.Int32

// SetPresetSandboxForTest pins whether the offered presets see a host sandbox,
// so preset tests behave the same on hosts with and without one.
func SetPresetSandboxForTest(available bool) (restore func()) {
	next := presetSandboxPinnedOff
	if available {
		next = presetSandboxPinnedOn
	}
	prev := presetSandbox.Swap(next)
	return func() { presetSandbox.Store(prev) }
}

func platformPermissionCapabilities() PermissionCapabilities {
	available := sandbox.Available()
	switch presetSandbox.Load() {
	case presetSandboxPinnedOn:
		available = true
	case presetSandboxPinnedOff:
		available = false
	}
	return permissionCapabilitiesForPlatform(runtime.GOOS, available, sandbox.UnavailableMessage())
}

func permissionCapabilitiesForPlatform(goos string, available bool, unavailableReason string) PermissionCapabilities {
	backend := "none"
	writeIsolation := ""
	readIsolation := ""
	networkIsolation := ""
	switch goos {
	case "darwin":
		backend = "seatbelt"
		writeIsolation = "seatbelt-filesystem"
		readIsolation = "seatbelt-filesystem"
		networkIsolation = "seatbelt-network"
	case "linux":
		backend = "bubblewrap"
		writeIsolation = "bubblewrap-mount-namespace"
		readIsolation = "bubblewrap-mount-namespace"
		networkIsolation = "bubblewrap-network-namespace"
	case "windows":
		// No OS backend, but the presets still apply as tool-layer boundaries
		// (file writers, approval prompts), so every preset stays selectable.
		return PermissionCapabilities{
			Backend: "none", Enforcement: "unavailable",
			SupportedPresets:  []string{string(permissionpreset.ReadOnly), string(permissionpreset.WorkspaceWrite), string(permissionpreset.DangerFullAccess)},
			UnavailableReason: unavailableReason,
		}
	}
	if available {
		return PermissionCapabilities{
			Backend: backend, Enforcement: "full",
			SupportedPresets: []string{string(permissionpreset.ReadOnly), string(permissionpreset.WorkspaceWrite), string(permissionpreset.DangerFullAccess)},
			WriteIsolation:   writeIsolation, ReadIsolation: readIsolation, NetworkIsolation: networkIsolation,
		}
	}
	return PermissionCapabilities{
		Backend: backend, Enforcement: "unavailable",
		SupportedPresets:  []string{string(permissionpreset.DangerFullAccess)},
		UnavailableReason: unavailableReason,
	}
}

func (c *Controller) PermissionSnapshot() PermissionSnapshot {
	c.permissionStateMu.RLock()
	defer c.permissionStateMu.RUnlock()
	auth := c.SessionAuthorizations()
	grants := make([]SessionGrantSummary, 0, len(auth.Grants)+len(auth.WriteRoots)+len(auth.PlanModeReadOnlyCommands))
	for _, target := range auth.Grants {
		grants = append(grants, SessionGrantSummary{Scope: "tool", Target: target})
	}
	for _, target := range auth.WriteRoots {
		grants = append(grants, SessionGrantSummary{Scope: "directory", Target: target})
	}
	for _, target := range auth.PlanModeReadOnlyCommands {
		grants = append(grants, SessionGrantSummary{Scope: "command-prefix", Target: target})
	}
	return PermissionSnapshot{
		SessionID: c.parentSessionID(), Generation: c.runtimeGeneration,
		Revision: c.permissionRevision.Load(), Preset: c.ToolApprovalMode(),
		WorkspaceRoot: strings.TrimSpace(c.workspaceRoot), Grants: grants,
		Capabilities: platformPermissionCapabilities(),
	}
}

// RestoreSessionAuthorizations re-applies grants captured from a prior
// controller when the same logical session is rebuilt.
func (c *Controller) RestoreSessionAuthorizations(auth SessionAuthorizations) {
	c.permissionStateMu.Lock()
	defer c.permissionStateMu.Unlock()
	c.approval.restoreSessionAuthorizations(auth)
	if c.writeAccess.roots != nil && len(auth.WriteRoots) > 0 {
		c.writeAccess.roots.GrantVerifiedSession(auth.WriteRoots)
	}
}

// SetPermissionPreset applies a compare-and-set update. A stale UI or remote
// reply cannot mutate a newer permission generation.
func (c *Controller) SetPermissionPreset(preset string, expectedRevision uint64) (PermissionSnapshot, []string, error) {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	current := c.permissionRevision.Load()
	if expectedRevision != current {
		return c.PermissionSnapshot(), nil, fmt.Errorf("permission revision changed: have %d, expected %d", current, expectedRevision)
	}
	raw := strings.ToLower(strings.TrimSpace(preset))
	if !permissionpreset.Valid(raw) {
		return c.PermissionSnapshot(), nil, fmt.Errorf("permission preset must be read-only, workspace-write, or danger-full-access")
	}
	capabilities := platformPermissionCapabilities()
	if !slices.Contains(capabilities.SupportedPresets, raw) {
		return c.PermissionSnapshot(), nil, fmt.Errorf("permission preset %q is unavailable: %s", raw, capabilities.UnavailableReason)
	}
	drained := c.applyToolApprovalModeLocked(raw)
	return c.PermissionSnapshot(), drained, nil
}

func (c *Controller) applyDurablePermissionPresetLocked(preset string) []string {
	previous := c.ToolApprovalMode()
	drained := c.applyToolApprovalModeLocked(preset)
	if previous == preset {
		// A same-value explicit choice still wrote a new durable event. Advance
		// the CAS revision so a delayed choice cannot overwrite that intent.
		c.permissionStateMu.Lock()
		c.permissionRevision.Add(1)
		c.permissionStateMu.Unlock()
	}
	return drained
}

// SetSessionPermissionPreset records an explicit choice in the canonical
// session before exposing it to execution or returning success to a caller.
func (c *Controller) SetSessionPermissionPreset(ctx context.Context, preset string, expectedRevision uint64) (PermissionSnapshot, []string, error) {
	if c == nil {
		return PermissionSnapshot{}, nil, fmt.Errorf("controller is nil")
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	if current := c.permissionRevision.Load(); expectedRevision != current {
		return c.PermissionSnapshot(), nil, fmt.Errorf("permission revision changed: have %d, expected %d", current, expectedRevision)
	}
	raw := strings.ToLower(strings.TrimSpace(preset))
	if !permissionpreset.Valid(raw) {
		return c.PermissionSnapshot(), nil, fmt.Errorf("permission preset must be read-only, workspace-write, or danger-full-access")
	}
	capabilities := platformPermissionCapabilities()
	if !slices.Contains(capabilities.SupportedPresets, raw) {
		return c.PermissionSnapshot(), nil, fmt.Errorf("permission preset %q is unavailable: %s", raw, capabilities.UnavailableReason)
	}
	_, runtime, exclusive := c.v3Binding()
	if !exclusive || runtime == nil {
		return c.PermissionSnapshot(), nil, session.ErrSessionNotRunning
	}
	if err := c.persistSessionPermissionPreset(ctx, runtime, raw); err != nil {
		drained, failed := c.applyFailedPermissionDowngradeLocked(raw, err)
		return c.PermissionSnapshot(), drained, failed
	}
	drained := c.applyDurablePermissionPresetLocked(raw)
	return c.PermissionSnapshot(), drained, nil
}

func (c *Controller) applyFailedPermissionDowngradeLocked(preset string, cause error) ([]string, error) {
	if permissionPresetRank(preset) < permissionPresetRank(c.ToolApprovalMode()) {
		return c.applyToolApprovalModeLocked(preset), cause
	}
	return nil, cause
}

func (c *Controller) persistSessionPermissionPreset(ctx context.Context, runtime *session.Runtime, preset string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(struct {
		Preset string `json:"preset"`
	}{Preset: preset})
	if err != nil {
		return err
	}
	commit, err := c.appendSessionBatch(ctx, runtime.Session(), session.Batch{
		OperationID: "session-permission-preset:" + agent.NewMessageID(),
		Events:      []session.Event{{Kind: "session/permission-preset", Payload: payload}},
	})
	if err != nil {
		return err
	}
	// An accepted append cannot be withdrawn. Finish its durability wait even if
	// the requesting client disconnects, so storage and enforcement do not split.
	_, err = runtime.Session().FlushThrough(context.WithoutCancel(ctx), commit.LastSequence())
	return err
}

// InvalidatePermissionSnapshots advances the revision with no permission
// change, so a compare-and-set against any earlier snapshot is refused.
func (c *Controller) InvalidatePermissionSnapshots() {
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	c.permissionStateMu.Lock()
	c.permissionRevision.Add(1)
	c.permissionStateMu.Unlock()
}

// RevokeSessionGrant removes one exact in-memory authorization. Revocation is
// compare-and-set protected; the new revision is published atomically with the
// removal before in-flight work and background processes are stopped.
func (c *Controller) RevokeSessionGrant(scope, target string, expectedRevision uint64) (PermissionSnapshot, error) {
	if c == nil {
		return PermissionSnapshot{}, fmt.Errorf("controller is nil")
	}
	c.permissionMu.Lock()
	defer c.permissionMu.Unlock()
	current := c.permissionRevision.Load()
	if expectedRevision != current {
		return c.PermissionSnapshot(), fmt.Errorf("permission revision changed: have %d, expected %d", current, expectedRevision)
	}
	c.promptResolveMu.Lock()
	c.permissionStateMu.Lock()
	removed := false
	switch strings.TrimSpace(scope) {
	case "directory":
		if c.writeAccess.roots != nil {
			removed = c.writeAccess.roots.RevokeSession(target)
		}
	case "tool", "command-prefix":
		removed = c.approval.revokeSessionAuthorization(scope, target)
	default:
		c.permissionStateMu.Unlock()
		c.promptResolveMu.Unlock()
		return c.PermissionSnapshot(), fmt.Errorf("unknown session grant scope %q", scope)
	}
	if !removed {
		c.permissionStateMu.Unlock()
		c.promptResolveMu.Unlock()
		return c.PermissionSnapshot(), fmt.Errorf("session grant was not found")
	}
	c.permissionRevision.Add(1)
	c.permissionStateMu.Unlock()
	turnID, cancelled := "", false
	if c.Running() {
		turnID, cancelled = c.cancelTurnLocked()
	}
	c.promptResolveMu.Unlock()
	if cancelled {
		c.finishCancel(turnID, true)
	}
	for _, job := range c.Jobs() {
		c.CancelJob(job.ID)
	}
	return c.PermissionSnapshot(), nil
}
