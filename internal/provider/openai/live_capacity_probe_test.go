package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The listing this reads is the shape OpenRouter actually serves: a top-level
// context_length and a top_provider block with the completion ceiling. The
// fixture is inline so the test states exactly which fields it depends on.
func TestFetchModelCatalogKeepsAnOpenRouterShapedWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"particle-ai/particle-1","context_length":1048576,"top_provider":{"context_length":1048576,"max_completion_tokens":131072}},
			{"id":"silent-model"},
			{"id":"vllm-model","max_model_len":32768}
		]}`))
	}))
	defer srv.Close()

	catalog, err := FetchModelCatalogWithOptions(context.Background(), srv.URL, "", FetchModelsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct{ window, output int }{}
	for _, info := range catalog {
		byID[info.ID] = struct{ window, output int }{info.ContextWindow, info.MaxOutputTokens}
	}
	if got := byID["particle-ai/particle-1"]; got.window != 1048576 || got.output != 131072 {
		t.Fatalf("openrouter row = %+v", got)
	}
	if got := byID["vllm-model"]; got.window != 32768 {
		t.Fatalf("vllm row = %+v", got)
	}
	if got := byID["silent-model"]; got.window != 0 || got.output != 0 {
		t.Fatalf("silent row gained a capacity it never declared: %+v", got)
	}
}
