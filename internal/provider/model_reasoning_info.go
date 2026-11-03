package provider

// ModelReasoningInfo preserves an endpoint declaration without inventing effort
// levels from a reasoning boolean or a supported parameter name.
type ModelReasoningInfo struct {
	Supported *bool    `json:"supported,omitempty"`
	Protocol  string   `json:"protocol,omitempty"`
	Efforts   []string `json:"efforts,omitempty"`
	Default   string   `json:"default,omitempty"`
	Parameter bool     `json:"parameter,omitempty"`
	Invalid   bool     `json:"invalid,omitempty"`
}
