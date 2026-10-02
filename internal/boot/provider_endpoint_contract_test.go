package boot

import (
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/netclient"
)

func TestRuntimeRejectsProtocolEndpointMismatch(t *testing.T) {
	entry := &config.ProviderEntry{
		Name: "deepseek-anthropic", PresetID: "deepseek-anthropic", Kind: "openai",
		BaseURL: "https://api.deepseek.com/anthropic/v1", RequestURL: "https://api.deepseek.com/anthropic/v1/chat/completions",
		Model: "deepseek-v4-flash",
	}
	_, err := NewProviderWithProxyAndModelInfo(entry, netclient.ProxySpec{}, nil)
	if err == nil || !strings.Contains(err.Error(), "https://api.deepseek.com/v1/chat/completions") {
		t.Fatalf("runtime mismatch error = %v", err)
	}
}

func TestRuntimeRepairsExactCatalogProtocolMismatch(t *testing.T) {
	entry := &config.ProviderEntry{
		Name: "deepseek-anthropic", PresetID: "deepseek-anthropic", Kind: "responses",
		BaseURL: "https://api.deepseek.com", RequestURL: "https://api.deepseek.com/anthropic/v1/messages",
		Model: "deepseek-v4-flash", ResponsesMode: "stateful",
	}
	p, err := NewProviderWithProxyAndModelInfo(entry, netclient.ProxySpec{}, nil)
	if err != nil {
		t.Fatalf("runtime repair: %v", err)
	}
	typ := reflect.TypeOf(p)
	if p == nil || typ == nil || typ.Kind() != reflect.Pointer ||
		typ.Elem().PkgPath() != "reasonix/internal/provider/anthropic" {
		t.Fatalf("runtime provider was not repaired: provider=%T", p)
	}
	if entry.Kind != "responses" || entry.BaseURL != "https://api.deepseek.com" ||
		entry.RequestURL != "https://api.deepseek.com/anthropic/v1/messages" || entry.ResponsesMode != "stateful" {
		t.Fatalf("runtime construction mutated shared config entry: %+v", entry)
	}
}
