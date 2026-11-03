package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
	"reasonix/internal/netclient"
)

var selectSetupModels = selectMany

func testAndRefreshProvider(s *providerSetupSession, p config.ProviderEntry) {
	restore := temporarilySetCredential(p.APIKeyEnv, s.pendingConnectionCredentials[p.Name])
	defer restore()
	p.ResolveAPIKeyFromProcessEnvForProbe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var proxy netclient.ProxySpec
	if s.cfg != nil {
		proxy = s.cfg.NetworkProxySpec()
	}
	started := time.Now()
	credentialRevision := config.CredentialStoreRevision()
	catalog, err := p.FetchModelCatalogWithProxy(ctx, proxy)
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.M.FetchModelsFailedFmt+"\n", p.Name, err)
		return
	}
	models := setupCatalogModelIDs(catalog)
	if len(models) == 0 {
		fmt.Fprintln(os.Stderr, i18n.M.CustomFetchEmpty)
		return
	}
	idxs, err := selectSetupModels(fmt.Sprintf(i18n.M.SelectModelsLabel, p.Name), setupModelItems(p, catalog))
	if err != nil || len(idxs) == 0 {
		return
	}
	selected := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		selected = append(selected, models[idx])
	}
	p.Models = selected
	p.Model = ""
	if !containsString(selected, p.Default) {
		p.Default = selected[0]
	}
	// Listings fill only missing windows; user settings remain authoritative.
	filled := p.ApplyDiscoveredLimits(catalog)
	if err := s.upsert([]config.ProviderEntry{p}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if current, ok := s.cfg.Provider(p.Name); ok {
		s.stageImageCatalogAt(*current, catalog, started, credentialRevision)
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.FetchModelsSuccessFmt, len(models), p.Name)))
	fmt.Printf("  %s\n", dim(i18n.M.SetupImageSaveHint))
	if filled > 0 {
		fmt.Printf("  %s\n", dim(fmt.Sprintf(i18n.M.SetupModelWindowDiscoveredFmt, filled)))
	}
}
