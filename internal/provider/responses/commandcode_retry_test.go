package responses

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

type commandCodeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f commandCodeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCommandCodeTransientResponsesBadRequestRetriesTheSameBodyOnce(t *testing.T) {
	var calls int
	var bodies []string
	transport := commandCodeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Status:     "400 Bad Request",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"message":"{\"type\":\"invalid_request_error\",\"code\":\"\",\"message\":\"invalid request error trace_id: 4f8d\"}\n","type":"invalid_request_error"}`,
				)),
				Request: req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
			)),
			Request: req,
		}, nil
	})
	p := New(Config{
		Name: "Command Code", APIKey: "key",
		BaseURL: "https://api.commandcode.ai/provider/v1",
		Model:   "deepseek/deepseek-v4.1-flash",
	}).(*client)
	p.http = &http.Client{Transport: transport}

	chunks := collect(t, p, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "continue"}}})
	if calls != 2 {
		t.Fatalf("requests = %d, want one transparent retry", calls)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("retry body changed:\nfirst=%s\nsecond=%s", bodies[0], bodies[1])
	}
	if len(chunks) == 0 || chunks[len(chunks)-1].Type != provider.ChunkDone {
		t.Fatalf("chunks = %+v, want completed stream", chunks)
	}
}

func TestCommandCodeTransientRetryRequiresTheCommandCodeResponsesHost(t *testing.T) {
	var calls int
	transport := commandCodeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"invalid request error trace_id: 4f8d"}}`,
			)),
			Request: req,
		}, nil
	})
	p := New(Config{
		Name: "relay", APIKey: "key", BaseURL: "https://relay.example/v1", Model: "model",
	}).(*client)
	p.http = &http.Client{Transport: transport}

	_, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "continue"}}})
	if err == nil {
		t.Fatal("transient-shaped 400 from another host must stay terminal")
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want no retry outside Command Code", calls)
	}
}

func TestCommandCodeTransientRetryDoesNotBroadenToOtherBadRequests(t *testing.T) {
	var calls int
	transport := commandCodeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"Invalid 'summary': summary is required and must be a list for reasoning."}}`,
			)),
			Request: req,
		}, nil
	})
	p := New(Config{
		Name: "Command Code", APIKey: "key",
		BaseURL: "https://api.commandcode.ai/provider/v1",
		Model:   "deepseek/deepseek-v4.1-flash",
	}).(*client)
	p.http = &http.Client{Transport: transport}

	_, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "continue"}}})
	if err == nil {
		t.Fatal("deterministic 400 must stay terminal")
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want no retry for a named schema error", calls)
	}
}
