package hostrpc

import (
	"encoding/json"
	"strings"
	"testing"
)

// A dev build or a telemetry-off configuration has no lifecycle tracker, so the
// identifiers are empty. The shell requires the keys to be present; dropping
// them made every such service fail the handshake.
func TestHelloResultAlwaysCarriesRunAndIncidentKeys(t *testing.T) {
	body, err := json.Marshal(HelloResult{DiagnosticsEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"runId":""`, `"incidentId":""`} {
		if !strings.Contains(string(body), key) {
			t.Fatalf("hello result omitted %s: %s", key, body)
		}
	}
}
