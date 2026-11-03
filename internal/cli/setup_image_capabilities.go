package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
)

// A discovery stays in the setup draft until configuration and credentials commit.
type setupImageCatalog struct {
	entry                   config.ProviderEntry
	models                  []provider.ModelInfo
	started                 time.Time
	credentialRevision      string
	pendingCredentialDigest [32]byte
	pendingCredential       bool
}

func (s *providerSetupSession) stageImageCatalog(entry config.ProviderEntry, models []provider.ModelInfo, started time.Time) {
	s.stageImageCatalogAt(entry, models, started, config.CredentialStoreRevision())
}

func (s *providerSetupSession) stageImageCatalogAt(entry config.ProviderEntry, models []provider.ModelInfo, started time.Time, revision string) {
	if s.imageCatalogs == nil {
		s.imageCatalogs = make(map[string]setupImageCatalog)
	}
	copied := append([]provider.ModelInfo(nil), models...)
	for i := range copied {
		copied[i].InputModalities = append([]provider.ModelModality(nil), copied[i].InputModalities...)
		if copied[i].ReasoningInfo != nil {
			copy := *copied[i].ReasoningInfo
			copy.Efforts = append([]string(nil), copy.Efforts...)
			if copy.Supported != nil {
				value := *copy.Supported
				copy.Supported = &value
			}
			copied[i].ReasoningInfo = &copy
		}
	}
	value, pending := s.pendingConnectionCredentials[entry.Name]
	s.imageCatalogs[entry.Name] = setupImageCatalog{
		entry: config.ProviderEntryConfigSnapshot(entry), models: copied, started: started,
		credentialRevision: revision,
		pendingCredential:  pending, pendingCredentialDigest: sha256.Sum256([]byte(value)),
	}
}

// Discard metadata if credentials changed after discovery. This runs under the
// setup commit locks, before its own credential rotations change the revision.
func (s *providerSetupSession) validateImageCatalogCredentials() {
	revision := config.CredentialStoreRevision()
	for name, discovery := range s.imageCatalogs {
		value, pending := s.pendingConnectionCredentials[name]
		if revision != discovery.credentialRevision || pending != discovery.pendingCredential || sha256.Sum256([]byte(value)) != discovery.pendingCredentialDigest {
			delete(s.imageCatalogs, name)
		}
	}
}

// Compare against the draft before rebinding discoveries to committed credential
// slots. A later edit or a concurrent config change must not certify a new route.
func (s *providerSetupSession) persistImageCatalogs(fresh *config.Config) {
	resolver := config.NewModelCapabilityResolver()
	for name, discovery := range s.imageCatalogs {
		draft, ok := s.cfg.Provider(name)
		if !ok || !config.ProviderEntriesConfigEqual(*draft, discovery.entry) {
			continue
		}
		entry, ok := fresh.Provider(name)
		if !ok {
			continue
		}
		committed := config.ProviderEntryConfigSnapshot(*entry)
		// Successful setup may rotate only APIKeyEnv after replaying the draft.
		if _, rotated := s.pendingConnectionCredentials[name]; rotated {
			committed.APIKeyEnv = discovery.entry.APIKeyEnv
		}
		committed.ModelOverrides = cloneSetupOverridesWithoutDiscovery(committed.ModelOverrides)
		expected := discovery.entry
		expected.ModelOverrides = cloneSetupOverridesWithoutDiscovery(expected.ModelOverrides)
		if !config.ProviderEntriesConfigEqual(committed, expected) {
			continue
		}
		resolver.PutCatalogAt(*entry, discovery.models, discovery.started)
	}
}

func setupModelItems(entry config.ProviderEntry, catalog []provider.ModelInfo) []menuItem {
	resolver := config.NewTransientModelCapabilityResolver()
	builtin := config.NewTransientModelCapabilityResolver()
	resolver.PutCatalog(entry, catalog)
	items := make([]menuItem, 0, len(catalog))
	for _, model := range catalog {
		selected := entry
		selected.Model = model.ID
		capability := resolver.Resolve(&selected)
		state := i18n.M.SetupImageUnknown
		switch capability.State {
		case config.CapabilitySupported:
			state = i18n.M.SetupImageSupported
		case config.CapabilityUnsupported:
			state = i18n.M.SetupImageUnsupported
		}
		source := i18n.M.SetupImageNoMetadata
		switch capability.Source {
		case config.CapabilitySourceOverride, config.CapabilitySourceLegacy:
			source = i18n.M.SetupImageManual
		case config.CapabilitySourcePreset, config.CapabilitySourceProtocol:
			source = i18n.M.SetupImageBuiltin
		case config.CapabilitySourceAdapter:
			source = i18n.M.SetupImageEndpoint
			if capability.InputModalities == nil {
				source = i18n.M.SetupImageNoMetadata
			}
			if builtin.Resolve(&selected).Source == config.CapabilitySourceAdapter {
				source = i18n.M.SetupImageBuiltin
			}
		}
		items = append(items, menuItem{name: model.ID, desc: fmt.Sprintf("img: %s · %s · %s", state, source, setupReasoningDescription(entry, model))})
	}
	return items
}

// Keep the URL wizard's existing endpoint/auth fallback policy, preserving
// metadata instead of reducing the response to model IDs before selection.
func fetchModelCatalogCompat(ctx context.Context, baseURL, apiKey string, proxy netclient.ProxySpec) ([]provider.ModelInfo, error) {
	candidates, err := config.BuildModelFetchURLs(baseURL, "")
	if err != nil {
		return nil, err
	}
	var firstHardErr error
	for _, u := range candidates {
		catalog, err := openai.FetchModelCatalogWithOptions(ctx, u, apiKey, openai.FetchModelsOptions{Proxy: proxy})
		if err == nil {
			return catalog, nil
		}
		if !openai.IsModelFetchEndpointMiss(err) && firstHardErr == nil {
			firstHardErr = err
		}
	}
	return nil, firstHardErr
}

func setupCatalogModelIDs(catalog []provider.ModelInfo) []string {
	models := make([]string, 0, len(catalog))
	for _, info := range catalog {
		if strings.TrimSpace(info.ID) != "" {
			models = append(models, info.ID)
		}
	}
	return models
}
