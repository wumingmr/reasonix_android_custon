package event

// SessionOperationInfo describes one manual maintenance operation. It is
// display/runtime metadata only and never enters the canonical conversation.
type SessionOperationInfo struct {
	OperationID       string `json:"operationId"`
	OperationRevision uint64 `json:"operationRevision,omitempty"`
	RuntimeEpoch      string `json:"runtimeEpoch,omitempty"`
	Kind              string `json:"kind"`
	Activity          string `json:"activity"`
	Status            string `json:"status"`
	ErrorCode         string `json:"errorCode,omitempty"`
	Detail            string `json:"detail,omitempty"`
	Applied           bool   `json:"applied,omitempty"`
	InputTokens       int    `json:"inputTokens,omitempty"`
	ResultTokens      int    `json:"resultTokens,omitempty"`
	Messages          int    `json:"messages,omitempty"`
	Summary           string `json:"summary,omitempty"`
	Archive           string `json:"archive,omitempty"`
}
