package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// ProviderEndpointMismatch describes a high-confidence conflict between a
// selected protocol and an exact request URL. It is intentionally conservative:
// custom gateways and query-bearing routes remain user-owned.
type ProviderEndpointMismatch struct {
	Protocol    string
	RequestURL  string
	Recommended string
}

// ProviderEndpointRepair records a high-confidence correction where an exact
// catalog request URL proves that the saved protocol is stale.
type ProviderEndpointRepair struct {
	ProviderName string
	RequestURL   string
	FromProtocol string
	ToProtocol   string
}

var providerEndpointRepairReceipts = struct {
	sync.Mutex
	byPath map[string][]ProviderEndpointRepair
}{byPath: make(map[string][]ProviderEndpointRepair)}

func (e *ProviderEndpointMismatch) Error() string {
	if e == nil {
		return ""
	}
	if e.Recommended != "" {
		return fmt.Sprintf("provider endpoint %q does not match protocol %q; use %s", e.RequestURL, e.Protocol, e.Recommended)
	}
	return fmt.Sprintf("provider endpoint %q does not match protocol %q", e.RequestURL, e.Protocol)
}

func normalizedProviderProtocol(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "dashscope-responses" {
		return "responses"
	}
	return kind
}

func providerProtocolSuffix(kind string) string {
	switch normalizedProviderProtocol(kind) {
	case "anthropic":
		return "/messages"
	case "responses":
		return "/responses"
	case "openai":
		return "/chat/completions"
	default:
		return ""
	}
}

// ProviderRequestURL builds the complete request URL represented by one SDK
// base URL in the protocol registry.
func ProviderRequestURL(kind, baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	switch normalizedProviderProtocol(kind) {
	case "anthropic":
		if strings.HasSuffix(base, "/v1") {
			return base + "/messages"
		}
		return base + "/v1/messages"
	case "responses":
		return base + "/responses"
	case "openai":
		return base + "/chat/completions"
	default:
		return base
	}
}

// ProviderEffectiveRequestURL resolves current and legacy endpoint fields with
// the same precedence used by the runtime adapters.
func ProviderEffectiveRequestURL(e *ProviderEntry) string {
	if e == nil {
		return ""
	}
	if requestURL := strings.TrimSpace(e.RequestURL); requestURL != "" {
		return requestURL
	}
	if normalizedProviderProtocol(e.Kind) == "openai" {
		if chatURL := strings.TrimRight(strings.TrimSpace(e.ChatURL), "/"); chatURL != "" {
			return chatURL
		}
	}
	return ProviderRequestURL(e.Kind, e.BaseURL)
}

// CatalogForProviderEntry resolves metadata for installed connections. Falling
// back to Name keeps hidden legacy presets useful without listing them for new
// connections.
func CatalogForProviderEntry(e *ProviderEntry) (string, ProviderCatalog, bool) {
	if e == nil {
		return "", ProviderCatalog{}, false
	}
	ids := []string{strings.TrimSpace(e.PresetID), strings.TrimSpace(e.Name)}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if preset, ok := CuratedProviderPreset(id); ok {
			return preset.ID, CatalogForProviderPreset(preset), true
		}
	}
	return "", ProviderCatalog{}, false
}

func recommendedProviderRequestURL(kind string, catalog ProviderCatalog) string {
	route, ok := catalog.Protocols[normalizedProviderProtocol(kind)]
	if !ok {
		return ""
	}
	return ProviderRequestURL(kind, route.BaseURL)
}

func normalizedExactProviderRequestURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path, true
}

// RepairProviderEndpointContract changes a provider protocol only when its
// effective request URL exactly and uniquely identifies another registered
// route in the provider catalog. Custom gateways and query-bearing overrides
// remain user-owned and continue through the validation path unchanged.
func RepairProviderEndpointContract(entry *ProviderEntry) (*ProviderEndpointRepair, bool) {
	if entry == nil {
		return nil, false
	}
	fromProtocol := normalizedProviderProtocol(entry.Kind)
	requestURL := ProviderEffectiveRequestURL(entry)
	current, ok := normalizedExactProviderRequestURL(requestURL)
	if !ok {
		return nil, false
	}
	_, catalog, ok := CatalogForProviderEntry(entry)
	if !ok {
		return nil, false
	}

	// Sort for deterministic behavior even though a repair is accepted only
	// when one normalized protocol matches.
	kinds := make([]string, 0, len(catalog.Protocols))
	for kind := range catalog.Protocols {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	matches := make(map[string]ProviderProtocolEndpoint)
	for _, kind := range kinds {
		route := catalog.Protocols[kind]
		candidate, exact := normalizedExactProviderRequestURL(ProviderRequestURL(kind, route.BaseURL))
		if exact && candidate == current {
			matches[normalizedProviderProtocol(kind)] = route
		}
	}
	if len(matches) != 1 {
		return nil, false
	}
	var toProtocol string
	var route ProviderProtocolEndpoint
	for toProtocol, route = range matches {
	}
	if toProtocol == "" || toProtocol == fromProtocol {
		return nil, false
	}

	repair := &ProviderEndpointRepair{
		ProviderName: entry.DisplayName,
		RequestURL:   requestURL,
		FromProtocol: fromProtocol,
		ToProtocol:   toProtocol,
	}
	if strings.TrimSpace(repair.ProviderName) == "" {
		repair.ProviderName = entry.Name
	}
	entry.Kind = toProtocol
	entry.BaseURL = route.BaseURL
	entry.RequestURL = ""
	entry.ChatURL = ""
	entry.AuthHeader = route.AuthHeader
	entry.ResponsesStateful = nil
	if toProtocol == "responses" {
		entry.ResponsesMode = route.ResponsesMode
	} else {
		entry.ResponsesMode = ""
	}
	return repair, true
}

func repairProviderEndpointContracts(c *Config) []ProviderEndpointRepair {
	if c == nil {
		return nil
	}
	var repairs []ProviderEndpointRepair
	for i := range c.Providers {
		if repair, changed := RepairProviderEndpointContract(&c.Providers[i]); changed {
			repairs = append(repairs, *repair)
		}
	}
	return repairs
}

func recordProviderEndpointRepairs(path string, repairs []ProviderEndpointRepair) {
	path = strings.TrimSpace(path)
	if path == "" || len(repairs) == 0 {
		return
	}
	providerEndpointRepairReceipts.Lock()
	providerEndpointRepairReceipts.byPath[path] = append(providerEndpointRepairReceipts.byPath[path], repairs...)
	providerEndpointRepairReceipts.Unlock()
}

// TakeProviderEndpointRepairReceipts returns startup repairs that occurred
// before the active runtime had an event sink, then clears the process-local
// receipt so concurrent tab builds do not repeat the notice.
func TakeProviderEndpointRepairReceipts(path string) []ProviderEndpointRepair {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	providerEndpointRepairReceipts.Lock()
	defer providerEndpointRepairReceipts.Unlock()
	repairs := append([]ProviderEndpointRepair(nil), providerEndpointRepairReceipts.byPath[path]...)
	delete(providerEndpointRepairReceipts.byPath, path)
	return repairs
}

// ProviderEndpointMismatchForEntry validates only explicit, recognizable
// conflicts. Unknown paths, hosts, query strings and fragments are preserved.
func ProviderEndpointMismatchForEntry(e *ProviderEntry) *ProviderEndpointMismatch {
	if e == nil {
		return nil
	}
	kind := normalizedProviderProtocol(e.Kind)
	expectedSuffix := providerProtocolSuffix(kind)
	requestURL := ProviderEffectiveRequestURL(e)
	if expectedSuffix == "" || requestURL == "" {
		return nil
	}
	u, err := url.Parse(requestURL)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	recommended := ""
	_, catalog, hasCatalog := CatalogForProviderEntry(e)
	if hasCatalog {
		recommended = recommendedProviderRequestURL(kind, catalog)
		if recommendedURL, parseErr := url.Parse(recommended); parseErr == nil &&
			strings.EqualFold(recommendedURL.Scheme, u.Scheme) &&
			strings.EqualFold(recommendedURL.Host, u.Host) &&
			strings.TrimRight(recommendedURL.EscapedPath(), "/") == path {
			return nil
		}
	}
	for _, suffix := range []string{"/v1/messages", "/messages", "/chat/completions", "/responses"} {
		if strings.HasSuffix(path, suffix) && !strings.HasSuffix(path, expectedSuffix) {
			return &ProviderEndpointMismatch{Protocol: kind, RequestURL: requestURL, Recommended: recommended}
		}
	}
	if !hasCatalog || !strings.HasSuffix(path, expectedSuffix) {
		return nil
	}
	selectedRoute, selected := catalog.Protocols[kind]
	if !selected {
		return nil
	}
	selectedBase, err := url.Parse(selectedRoute.BaseURL)
	if err != nil || !strings.EqualFold(selectedBase.Host, u.Host) {
		return nil
	}
	for otherKind, route := range catalog.Protocols {
		if normalizedProviderProtocol(otherKind) == kind {
			continue
		}
		otherBase, parseErr := url.Parse(ProviderRequestURL(otherKind, route.BaseURL))
		if parseErr != nil || !strings.EqualFold(otherBase.Host, u.Host) {
			continue
		}
		otherSuffix := providerProtocolSuffix(otherKind)
		foreignRequestPath := strings.TrimRight(otherBase.EscapedPath(), "/")
		foreignRoot := strings.TrimSuffix(foreignRequestPath, otherSuffix)
		if foreignRoot == "" || foreignRoot == "/" {
			continue
		}
		if path == foreignRoot+expectedSuffix {
			return &ProviderEndpointMismatch{Protocol: kind, RequestURL: requestURL, Recommended: recommended}
		}
	}
	return nil
}

func ValidateProviderEndpoint(e *ProviderEntry) error {
	if mismatch := ProviderEndpointMismatchForEntry(e); mismatch != nil {
		return mismatch
	}
	return nil
}
