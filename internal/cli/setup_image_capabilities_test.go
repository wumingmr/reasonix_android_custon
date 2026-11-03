package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

func setupImageFixture(t *testing.T) (*providerSetupSession, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("REASONIX_CACHE_HOME", filepath.Join(home, "cache"))
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{{Name: "relay", Kind: "openai", BaseURL: "http://127.0.0.1:8790/v1", Models: []string{"image", "text", "unknown"}, Default: "image", APIKeyEnv: "SETUP_IMAGE_TEST_KEY"}}
	cfg.DefaultModel = "relay/image"
	path := filepath.Join(home, "config.toml")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	return newProviderSetupSession(fresh), path
}

func setupImageTestCatalog() []provider.ModelInfo {
	return []provider.ModelInfo{
		{ID: "image", InputModalities: []provider.ModelModality{provider.ModalityText, provider.ModalityImage}},
		{ID: "text", InputModalities: []provider.ModelModality{provider.ModalityText}},
		{ID: "unknown"},
	}
}

func TestSetupImageCatalogSavesOnlyAfterCommit(t *testing.T) {
	s, path := setupImageFixture(t)
	before, _ := os.ReadFile(path)
	s.stageImageCatalog(s.cfg.Providers[0], setupImageTestCatalog(), time.Now())
	entry, _ := s.cfg.ResolveModel("relay/image")
	if got := config.NewModelCapabilityResolver().Resolve(entry); got.State != config.CapabilityUnknown {
		t.Fatalf("draft wrote capability cache: %+v", got)
	}
	written, err := commitProviderSetupSession(s, path)
	if err != nil || written {
		t.Fatalf("cache-only commit: written=%v err=%v", written, err)
	}
	for model, want := range map[string]config.CapabilityState{"image": config.CapabilitySupported, "text": config.CapabilityUnsupported, "unknown": config.CapabilityUnknown} {
		entry, _ := s.cfg.ResolveModel("relay/" + model)
		if got := config.NewModelCapabilityResolver().Resolve(entry); got.State != want {
			t.Fatalf("%s after cache reload: %+v", model, got)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || s.cfg.DefaultModel != "relay/image" {
		t.Fatal("discovery rewrote user config/default")
	}
}

func TestSetupImageCatalogDoesNotCertifyEditedOrConflictingRoutes(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprint(concurrent), func(t *testing.T) {
			s, path := setupImageFixture(t)
			s.stageImageCatalog(s.cfg.Providers[0], setupImageTestCatalog(), time.Now())
			if concurrent {
				other, err := config.LoadForEditReadOnlyStrict(path)
				if err != nil {
					t.Fatal(err)
				}
				other.Providers[0].BaseURL = "http://127.0.0.1:9999/v1"
				if err := other.SaveTo(path); err != nil {
					t.Fatal(err)
				}
			} else {
				edited := s.cfg.Providers[0]
				edited.BaseURL = "http://127.0.0.1:9999/v1"
				if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = commitProviderSetupSession(s, path)
			fresh, err := config.LoadForEditReadOnlyStrict(path)
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := fresh.ResolveModel("relay/image")
			if got := config.NewModelCapabilityResolver().Resolve(entry); got.State != config.CapabilityUnknown {
				t.Fatalf("new route inherited old image fact: %+v", got)
			}
		})
	}
}

func TestSetupImageCatalogConflictDoesNotWriteCache(t *testing.T) {
	s, path := setupImageFixture(t)
	edited := s.cfg.Providers[0]
	edited.ContextWindow = 200000
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.cfg.Provider("relay")
	s.stageImageCatalog(*current, setupImageTestCatalog(), time.Now())
	concurrent, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	concurrent.Providers[0].ContextWindow = 300000
	if err := concurrent.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	if _, err := commitProviderSetupSession(s, path); err == nil {
		t.Fatal("concurrent edit was accepted")
	}
	entry, _ := concurrent.ResolveModel("relay/image")
	if got := config.NewModelCapabilityResolver().Resolve(entry); got.State != config.CapabilityUnknown {
		t.Fatalf("failed save wrote cache: %+v", got)
	}
}

func TestSetupImageCatalogCommitPreservesManualOverride(t *testing.T) {
	s, path := setupImageFixture(t)
	disabled := false
	edited := s.cfg.Providers[0]
	edited.ModelOverrides = map[string]config.ProviderModelOverride{"image": {Vision: &disabled, ContextWindow: 200000}}
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.cfg.Provider("relay")
	s.stageImageCatalog(*current, setupImageTestCatalog(), time.Now())
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := fresh.ResolveModel("relay/image")
	got := config.NewModelCapabilityResolver().Resolve(entry)
	if got.State != config.CapabilityUnsupported || got.Source != config.CapabilitySourceOverride || got.AutomaticState != config.CapabilitySupported || entry.ContextWindow != 200000 {
		t.Fatalf("manual setting lost: %+v", got)
	}
}

func TestSetupImageCatalogFollowsCommittedCredentialRotation(t *testing.T) {
	s, path := setupImageFixture(t)
	if err := s.setCredentialForProviders([]string{"relay"}, "SETUP_IMAGE_TEST_KEY", "test-only-key"); err != nil {
		t.Fatal(err)
	}
	current, _ := s.cfg.Provider("relay")
	s.stageImageCatalog(*current, setupImageTestCatalog(), time.Now())
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := fresh.ResolveModel("relay/image")
	if got := config.NewModelCapabilityResolver().Resolve(entry); got.State != config.CapabilitySupported {
		t.Fatalf("new credential slot lost discovery: %+v", got)
	}
}

func TestSetupImageCatalogRejectsChangedPendingCredential(t *testing.T) {
	s, path := setupImageFixture(t)
	if err := s.setCredentialForProviders([]string{"relay"}, "SETUP_IMAGE_TEST_KEY", "first-key"); err != nil {
		t.Fatal(err)
	}
	current, _ := s.cfg.Provider("relay")
	s.stageImageCatalog(*current, setupImageTestCatalog(), time.Now())
	if err := s.setCredentialForProviders([]string{"relay"}, "SETUP_IMAGE_TEST_KEY", "replacement-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := fresh.ResolveModel("relay/image")
	if got := config.NewModelCapabilityResolver().Resolve(entry); got.State != config.CapabilityUnknown {
		t.Fatalf("replacement credential inherited discovery: %+v", got)
	}
}

func TestSetupImageItemsShowStateAndRespectManualChoice(t *testing.T) {
	s, _ := setupImageFixture(t)
	items := setupModelItems(s.cfg.Providers[0], setupImageTestCatalog())
	for i, want := range []string{i18n.M.SetupImageSupported, i18n.M.SetupImageUnsupported, i18n.M.SetupImageUnknown} {
		if !strings.Contains(items[i].desc, "img:") || !strings.Contains(items[i].desc, want) {
			t.Fatalf("item %d: %+v", i, items[i])
		}
	}
	entry := s.cfg.Providers[0]
	no := false
	entry.ModelOverrides = map[string]config.ProviderModelOverride{"image": {Vision: &no}}
	if got := setupModelItems(entry, setupImageTestCatalog())[0].desc; !strings.Contains(got, i18n.M.SetupImageManual) || !strings.Contains(got, i18n.M.SetupImageUnsupported) {
		t.Fatal(got)
	}
}

func TestSetupImageCatalogNewConnectionRetainsMetadata(t *testing.T) {
	s, path := setupImageFixture(t)
	entry := config.ProviderEntry{Name: "new-relay", Kind: "openai", BaseURL: "http://127.0.0.1:8888/v1", Model: "image"}
	result := newProviderPromptResult([]config.ProviderEntry{entry}, "", "", false, "")
	result.imageCatalog, result.imageStarted = setupImageTestCatalog(), time.Now()
	result.imageCredentialRevision = config.CredentialStoreRevision()
	if !s.addPrompted(nil, nil, result) {
		t.Fatal("new connection rejected")
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	selected, ok := fresh.ResolveModel("new-relay/image")
	if !ok || config.NewModelCapabilityResolver().Resolve(selected).State != config.CapabilitySupported {
		t.Fatal("new connection lost image discovery")
	}
}

func TestSetupImageRefreshStagesMetadataUntilSave(t *testing.T) {
	s, path := setupImageFixture(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected probe: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"image","supports_vision":true},{"id":"unknown"}]}`)
	}))
	defer srv.Close()
	entry := s.cfg.Providers[0]
	entry.BaseURL = srv.URL + "/v1"
	if err := s.upsert([]config.ProviderEntry{entry}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SETUP_IMAGE_TEST_KEY", "test-only-key")
	old := selectSetupModels
	t.Cleanup(func() { selectSetupModels = old })
	selectSetupModels = func(_ string, items []menuItem) ([]int, error) {
		if len(items) != 2 || !strings.Contains(items[0].desc, i18n.M.SetupImageSupported) || !strings.Contains(items[1].desc, i18n.M.SetupImageUnknown) {
			t.Fatalf("items=%+v", items)
		}
		return nil, errors.New("cancelled")
	}
	testAndRefreshProvider(s, entry)
	if len(s.imageCatalogs) != 0 {
		t.Fatal("cancelled picker staged capabilities")
	}
	selectSetupModels = func(string, []menuItem) ([]int, error) { return []int{0, 1}, nil }
	testAndRefreshProvider(s, entry)
	selected, _ := s.cfg.ResolveModel("relay/image")
	if config.NewModelCapabilityResolver().Resolve(selected).State != config.CapabilityUnknown {
		t.Fatal("refresh persisted before Save")
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	fresh, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	selected, _ = fresh.ResolveModel("relay/image")
	if config.NewModelCapabilityResolver().Resolve(selected).State != config.CapabilitySupported || requests != 2 {
		t.Fatalf("refresh did not save metadata: requests=%d", requests)
	}
}

func TestSetupImageCatalogFetchUsesOnlyModelMetadata(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected inference probe: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"image","architecture":{"input_modalities":["text","image"]}},{"id":"unknown"}]}`)
	}))
	defer srv.Close()
	catalog, err := fetchModelCatalogCompat(context.Background(), srv.URL+"/v1", "", netclient.ProxySpec{})
	if err != nil || len(catalog) != 2 || !catalog[0].SupportsInput(provider.ModalityImage) || catalog[1].InputModalities != nil || requests != 1 {
		t.Fatalf("catalog=%+v requests=%d err=%v", catalog, requests, err)
	}
}
