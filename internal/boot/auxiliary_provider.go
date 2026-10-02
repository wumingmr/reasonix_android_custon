package boot

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"reasonix/internal/config"
	"reasonix/internal/extension"
	"reasonix/internal/extension/protocol"
	"reasonix/internal/extension/sidecar"
	"reasonix/internal/provider"
)

// AuxiliaryProviderRequest describes a bounded provider-only operation that
// belongs to a durable session but must not construct a conversation
// controller. Extension sidecars receive the target session identity.
type AuxiliaryProviderRequest struct {
	Config        *config.Config
	SessionID     string
	WorkspaceRoot string
	ModelRef      string
	OnWarning     func(string)
}

// AuxiliaryProviderHandle owns the resolver and any provider sidecars started
// for one auxiliary request. Close is idempotent.
type AuxiliaryProviderHandle struct {
	Resolver   provider.Resolver
	Generation uint64

	once    sync.Once
	manager *sidecar.Manager
}

// Close retires only the sidecars owned by this auxiliary generation.
func (h *AuxiliaryProviderHandle) Close() error {
	if h == nil {
		return nil
	}
	var err error
	h.once.Do(func() {
		if h.manager != nil {
			err = h.manager.Close()
		}
	})
	return err
}

// AcquireAuxiliaryProvider builds the same config+extension provider resolver
// used by normal boot without creating a Controller or publishing tools, MCP,
// hooks, commands, or extension UI into an active chat runtime.
func AcquireAuxiliaryProvider(ctx context.Context, request AuxiliaryProviderRequest) (*AuxiliaryProviderHandle, error) {
	if request.Config == nil {
		return nil, fmt.Errorf("auxiliary provider config is required")
	}
	sessionID := strings.TrimSpace(request.SessionID)
	root := strings.TrimSpace(request.WorkspaceRoot)
	if sessionID == "" || root == "" {
		return nil, fmt.Errorf("auxiliary provider target identity is required")
	}
	generation := nextRuntimeGeneration()
	base := NewLocalProviderResolverWithCapabilities(
		request.Config,
		request.Config.NetworkProxySpec(),
		config.NewModelCapabilityResolver(),
	)
	modelRef := strings.TrimSpace(request.ModelRef)
	if modelRef == "" {
		modelRef = strings.TrimSpace(request.Config.DefaultModel)
	}
	// Config-backed providers need no sidecar at all. This is both cheaper and
	// prevents unrelated mixed-capability extensions from receiving a session
	// context for an operation they do not own.
	if _, err := base.Resolve(provider.Selection{Ref: modelRef}); err == nil {
		return &AuxiliaryProviderHandle{Resolver: base, Generation: generation}, nil
	}
	pluginName := auxiliaryProviderPluginName(modelRef)
	if pluginName == "" {
		return nil, fmt.Errorf("auxiliary provider model %q is unavailable", modelRef)
	}
	manager, warnings, err := sidecar.StartPackagesByName(ctx, config.ReasonixHomeDir(), protocol.SessionContext{
		SessionID: sessionID, WorkspaceRoot: root, Generation: generation,
	}, nil, pluginName)
	for _, warning := range warnings {
		if request.OnWarning != nil {
			request.OnWarning(warning)
		}
	}
	if err != nil {
		return nil, err
	}
	handle := &AuxiliaryProviderHandle{Resolver: base, Generation: generation, manager: manager}
	if manager == nil {
		return handle, nil
	}
	claims, err := resolveReplacementClaims(manager.Contributions())
	if err != nil {
		_ = handle.Close()
		return nil, err
	}
	merged, err := mergeSidecarProviders(base, manager, claims, extension.NewRuntimeOwner())
	if err != nil {
		_ = handle.Close()
		return nil, err
	}
	installSidecarStreamRouters(manager, merged)
	if _, err := merged.Resolve(provider.Selection{Ref: modelRef}); err != nil {
		_ = handle.Close()
		return nil, fmt.Errorf("auxiliary provider model %q is unavailable: %w", modelRef, err)
	}
	handle.Resolver = merged
	return handle, nil
}

func auxiliaryProviderPluginName(modelRef string) string {
	parts := strings.Split(strings.TrimSpace(modelRef), "/")
	if len(parts) < 4 || parts[0] != "plugin" {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
