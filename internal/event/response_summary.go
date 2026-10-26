package event

// ResponseSummary is content-free, local evidence for a committed model
// response. It is stored in an optional diagnostic/response event, never in
// model messages. Usage covers the sampling round, including protocol repairs.
// Missing summaries in older sessions mean unknown, not zero output.
type ResponseSummary struct {
	Provider         string         `json:"provider"`
	ModelRef         string         `json:"modelRef,omitempty"`
	FinishReason     string         `json:"finishReason"`
	TextBytes        int            `json:"textBytes"`
	ReasoningBytes   int            `json:"reasoningBytes"`
	ToolCalls        int            `json:"toolCalls"`
	ServerSearches   int            `json:"serverSearches"`
	StreamComplete   bool           `json:"streamComplete"`
	RecoveryAttempts int            `json:"recoveryAttempts"`
	Usage            *ResponseUsage `json:"usage,omitempty"`
}

// A missing Usage means unavailable. Estimated/Unknown preserve the provider
// accounting distinction instead of presenting inferred counters as exact.
type ResponseUsage struct {
	InputTokens     int  `json:"inputTokens"`
	OutputTokens    int  `json:"outputTokens"`
	ReasoningTokens int  `json:"reasoningTokens"`
	RequestCount    int  `json:"requestCount"`
	Estimated       bool `json:"estimated"`
	Unknown         bool `json:"unknown"`
}
