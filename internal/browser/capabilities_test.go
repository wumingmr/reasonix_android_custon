package browser

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Golden from 61cc393b2027: optional capabilities must not change the
// serialized permanent tool prefix, including descriptions and schemas.
func TestLegacyBrowserToolPrefixUnchanged(t *testing.T) {
	var rows []struct {
		Name, Description string
		Schema            json.RawMessage
	}
	for _, v := range Tools(nil) {
		rows = append(rows, struct {
			Name, Description string
			Schema            json.RawMessage
		}{v.Name(), v.Description(), v.Schema()})
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "1625d99eb8ff973c6d6ebadc2eb12dd30098b6e434d2c4a9b3a80dd3da73bb1b" {
		t.Fatalf("legacy browser tool prefix changed: %s", got)
	}
}

type capabilityFake struct {
	fakeExecutor
	calls int
}

func (f *capabilityFake) BrowserCapability(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	f.calls++
	return json.RawMessage(`{"count":2,"ambiguous":true}`), nil
}
func TestOptionalBrowserCapabilitiesPreserveOldExecutorAndSchemas(t *testing.T) {
	old := &fakeExecutor{}
	tools := CapabilityTools(old)
	if len(Names()) != 14 || len(Tools(old)) != 14 {
		t.Fatal("existing tool surface changed")
	}
	for _, tool := range tools {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"tabId":"t"}`))
		if err == nil || !strings.Contains(err.Error(), "capability_unsupported") {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
	}
	f := &capabilityFake{}
	query := CapabilityTools(f)[0]
	result, err := query.Execute(context.Background(), json.RawMessage(`{"tabId":"t","documentToken":"d","role":"button","name":"Save"}`))
	if err != nil || !strings.Contains(result, `"ambiguous":true`) {
		t.Fatalf("%s %v", result, err)
	}
	_, err = query.Execute(context.Background(), json.RawMessage(`{"tabId":"t","script":"anything"}`))
	if err == nil || f.calls != 1 {
		t.Fatal("unexpected fields reached the host")
	}
	viewport := CapabilityTools(f)[2]
	_, err = viewport.Execute(context.Background(), json.RawMessage(`{"tabId":"t","action":"set","width":393,"height":852}`))
	if err == nil || f.calls != 1 {
		t.Fatal("write reached the host without operation identity")
	}
}
