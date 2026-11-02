// provider-network-probe performs one bounded, synthetic Chat Completions call.
// It never executes tools and outputs only the provider's safe diagnostic model.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/secrets"
)

type singleTransport struct {
	base http.RoundTripper
	used atomic.Bool
}

func (t *singleTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.used.Swap(true) {
		return nil, errors.New("probe request limit reached")
	}
	return t.base.RoundTrip(r)
}

func main() {
	endpoint := flag.String("endpoint", "https://api.deepseek.com/chat/completions", "complete endpoint URL; no credentials or query")
	model := flag.String("model", "deepseek-v4-flash", "fixed model for the comparison")
	protocol := flag.String("protocol", "h2", "h1 or h2; observed protocol is included in output")
	bodyBytes := flag.Int("body-bytes", 1024, "target synthetic JSON bytes (1024..4194304)")
	expect := flag.Bool("expect-continue", false, "independent Expect: 100-continue experiment")
	budget := flag.Duration("timeout", 360*time.Second, "whole probe budget (1s..10m)")
	flag.Parse()
	if err := run(*endpoint, *model, *protocol, *bodyBytes, *expect, *budget); err != nil {
		// Validation errors are fixed local prose, never transport error strings.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(endpoint, model, protocol string, bodyBytes int, expect bool, budget time.Duration) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	if protocol != "h1" && protocol != "h2" {
		return errors.New("protocol must be h1 or h2")
	}
	if bodyBytes < 1024 || bodyBytes > 4<<20 || budget < time.Second || budget > 10*time.Minute {
		return errors.New("body size or timeout outside probe limits")
	}
	key := os.Getenv("REASONIX_PROBE_API_KEY")
	if key == "" {
		return errors.New("set REASONIX_PROBE_API_KEY in the environment")
	}
	if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return errors.New("remote probes require HTTPS")
	}
	body := syntheticBody(model, bodyBytes)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 30 * time.Second
	tr.ResponseHeaderTimeout = 300 * time.Second
	tr.ExpectContinueTimeout = time.Second
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP1(protocol == "h1")
	tr.Protocols.SetHTTP2(protocol == "h2")
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: &singleTransport{base: tr}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var mu sync.Mutex
	var observation provider.RequestObservation
	ctx = provider.WithRequestObserver(ctx, func(v provider.RequestObservation) { mu.Lock(); observation = v; mu.Unlock() })
	resp, requestErr := provider.SendOnce(ctx, client, provider.SendOptions{Provider: "probe", Protocol: "openai"}, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		// Prevent request-body replay inside Transport. A new transport is used
		// for this invocation; the wrapper also rejects any application resend.
		r.GetBody = nil
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		if expect {
			r.Header.Set("Expect", "100-continue")
		}
		return r, nil
	})
	traceID := ""
	if resp != nil {
		traceID = provider.DiagnoseFailure(&provider.APIError{TraceID: resp.Header.Get("x-trace-id")}).TraceID
		if traceID == "" {
			traceID = provider.DiagnoseFailure(&provider.APIError{TraceID: resp.Header.Get("trace-id")}).TraceID
		}
		if traceID == "" {
			traceID = provider.DiagnoseFailure(&provider.APIError{TraceID: resp.Header.Get("trace_id")}).TraceID
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if requestErr == nil {
			requestErr = readErr
		}
	}
	mu.Lock()
	result := struct {
		SchemaVersion     int                         `json:"schemaVersion"`
		RequestedProtocol string                      `json:"requestedProtocol"`
		ExpectContinue    bool                        `json:"expectContinue"`
		TraceID           string                      `json:"traceId,omitempty"`
		Request           provider.RequestObservation `json:"request"`
		Failure           *provider.FailureDiagnostic `json:"failure,omitempty"`
	}{1, protocol, expect, traceID, observation, nil}
	mu.Unlock()
	if requestErr != nil {
		d := provider.DiagnoseFailure(requestErr)
		result.Failure = d
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return errors.New("cannot encode safe probe result")
	}
	encoded, err = secrets.RedactJSON(encoded)
	if err != nil || !json.Valid(encoded) {
		return errors.New("cannot redact probe result")
	}
	fmt.Println(string(encoded))
	return nil
}

func syntheticBody(model string, size int) []byte {
	request := map[string]any{"model": model, "stream": true, "max_tokens": 1, "messages": []map[string]string{{"role": "user", "content": "Reply OK. Synthetic padding: "}}}
	base, _ := json.Marshal(request)
	if padding := size - len(base); padding > 0 {
		request["messages"].([]map[string]string)[0]["content"] += strings.Repeat("x", padding)
	}
	body, _ := json.Marshal(request)
	return body
}
