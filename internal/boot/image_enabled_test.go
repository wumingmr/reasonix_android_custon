package boot

import (
	"testing"

	"reasonix/internal/provider"
)

type modalityProvider struct {
	provider.Provider
	modalities []provider.ModelModality
}

func (p modalityProvider) ModelInfo() provider.ModelInfo {
	return provider.ModelInfo{InputModalities: p.modalities}
}

func TestRuntimeImageEnabledOnlyTrustsADeclaredModalityList(t *testing.T) {
	text := []provider.ModelModality{provider.ModalityText}
	both := []provider.ModelModality{provider.ModalityText, provider.ModalityImage}
	cases := []struct {
		name       string
		modalities []provider.ModelModality
		fallback   bool
		want       bool
	}{
		{"declared text-only stays blocked", text, true, false},
		{"declared image model reads images", both, false, true},
		{"undeclared model follows the resolved capability", nil, true, true},
		{"undeclared model with a fallback model stays routed", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeImageEnabled(modalityProvider{modalities: tc.modalities}, tc.fallback); got != tc.want {
				t.Fatalf("runtimeImageEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}
