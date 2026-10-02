package boot

import (
	"fmt"
	"strings"

	"reasonix/internal/config"
	"reasonix/internal/provider"
)

// subagentModelSelection resolves a child's model override to the entry that
// prices it and the reference the resolver builds it from.
func subagentModelSelection(cfg *config.Config, resolver provider.Resolver, parent *config.ProviderEntry, modelRef string) (config.ProviderEntry, string, error) {
	if strings.TrimSpace(modelRef) == "" {
		return *parent, modelRefFromEntry(parent), nil
	}
	modelRef = childModelRef(cfg, parent, modelRef)
	if resolved, ok := cfg.ResolveModel(modelRef); ok {
		return *resolved, modelRefFromEntry(resolved), nil
	}
	if resolver != nil {
		return *syntheticEntryFromResolver(resolver, modelRef), modelRef, nil
	}
	return config.ProviderEntry{}, "", fmt.Errorf("unknown model %q", modelRef)
}

// childModelRef qualifies a bare model name with the parent's provider when
// that provider serves it. Config resolves a bare name to the first provider
// listing it, which would move a child onto a provider the user never picked.
func childModelRef(cfg *config.Config, parent *config.ProviderEntry, ref string) string {
	ref = strings.TrimSpace(ref)
	if cfg == nil || parent == nil || ref == "" || strings.Contains(ref, "/") || !parent.HasModel(ref) {
		return ref
	}
	if _, isProvider := cfg.Provider(ref); isProvider {
		return ref
	}
	return parent.Name + "/" + ref
}
