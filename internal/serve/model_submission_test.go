package serve

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"reasonix/internal/control"
)

func TestSubmissionErrorPreservesUnknownDurabilityOutcome(t *testing.T) {
	for _, tc := range []struct {
		err     error
		outcome string
	}{
		{errors.New("receipt persistence failed"), "unknown"},
		{errors.Join(control.ErrSubmissionNotAccepted, errors.New("configuration pending")), "not_accepted"},
	} {
		response := httptest.NewRecorder()
		writeSubmissionFailure(response, tc.err)
		var body struct {
			Data struct {
				Outcome string `json:"submissionOutcome"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.Outcome != tc.outcome {
			t.Fatalf("outcome=%q, want %q", body.Data.Outcome, tc.outcome)
		}
	}
}
