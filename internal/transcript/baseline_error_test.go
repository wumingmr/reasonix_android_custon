package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestBaselineFailureDiagnosticsPreserveCauseWithoutContent(t *testing.T) {
	const secret = "PRIVATE-CHAT-PATH-TOKEN"
	for _, test := range []struct {
		name string
		rows []Message
		code string
	}{
		{"duplicate", []Message{
			{RecordID: secret, MessageID: secret + "-first", Role: "tool", Content: secret},
			{RecordID: secret, MessageID: secret + "-second", ToolCallID: secret, Role: "tool", Content: strings.Repeat(secret, 10000),
				Reasoning: secret, ToolCalls: []ToolCall{{Arguments: secret}}},
		}, "duplicate_record_identity"},
		{"missing", []Message{{Role: secret, Content: secret}}, "missing_record_identity"},
		{"encode", []Message{{RecordID: "one", ServerSearch: []provider.ServerSearchCall{{Raw: json.RawMessage(secret)}}}}, "baseline_encode_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection, err := NewProjection(testIdentity, test.rows, 0)
			var diagnostic *BaselineError
			if projection != nil || !errors.As(err, &diagnostic) {
				t.Fatalf("projection = %v, error = %v", projection, err)
			}
			if diagnostic.Code() != test.code || !errors.Is(err, diagnostic.cause) {
				t.Fatalf("lost error classification or cause: %v", err)
			}
			if test.name == "encode" {
				var marshalError *json.MarshalerError
				if !errors.As(err, &marshalError) {
					t.Fatal("original JSON error type is no longer available")
				}
			}
			var output bytes.Buffer
			slog.New(slog.NewJSONHandler(&output, nil)).Error("failed", "diagnostic", diagnostic)
			if strings.Contains(output.String(), secret) || output.Len() > 1500 {
				t.Fatal("diagnostic leaked imported data or exceeded its bounded size")
			}
			var textOutput bytes.Buffer
			slog.New(slog.NewTextHandler(&textOutput, nil)).Error("failed", "diagnostic", diagnostic)
			if strings.Contains(textOutput.String(), secret) || textOutput.Len() > 1500 ||
				!strings.Contains(textOutput.String(), "diagnostic.code="+test.code) {
				t.Fatal("text log leaked imported data or lost structured diagnostics")
			}
			var entry struct {
				Diagnostic map[string]any `json:"diagnostic"`
			}
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			fields := entry.Diagnostic
			if fields["code"] != test.code || fields["record_count"] != float64(len(test.rows)) {
				t.Fatalf("wrong persisted diagnostic: %+v", fields)
			}
			if test.name == "duplicate" && (fields["record_index"] != float64(1) || fields["previous_record_index"] != float64(0) || fields["record_key"] != diagnosticKey(secret)) {
				t.Fatalf("missing conflict positions or stable identity: %+v", fields)
			}
			if test.name == "missing" && (fields["record_index"] != float64(0) || fields["role"] != "other") {
				t.Fatalf("invalid role was not classified safely: %+v", fields)
			}
		})
	}
}
