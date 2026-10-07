package responses

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"reasonix/internal/provider"
)

// isCommandCodeTransientResponsesError recognizes Command Code's anonymous,
// intermittent 400. No stable retry signal exists, so keep this external
// protocol quirk narrowly scoped; named schema failures remain terminal.
func isCommandCodeTransientResponsesError(requestURL string, err error) bool {
	u, parseErr := url.Parse(strings.TrimSpace(requestURL))
	if parseErr != nil || strings.ToLower(u.Hostname()) != "api.commandcode.ai" ||
		strings.TrimRight(u.Path, "/") != "/provider/v1/responses" {
		return false
	}
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(apiErr.Body)
	return strings.Contains(body, "invalid_request_error") &&
		strings.Contains(body, "invalid request error") &&
		strings.Contains(body, "trace_id")
}

func isStalePreviousResponseError(err error) bool {
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(apiErr.Body)
	mentionsID := strings.Contains(body, "previous_response_id") || strings.Contains(body, "previous response") || strings.Contains(body, "response id")
	return mentionsID &&
		(strings.Contains(body, "not found") || strings.Contains(body, "invalid") || strings.Contains(body, "expired"))
}
