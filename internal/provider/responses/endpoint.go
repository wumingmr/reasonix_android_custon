package responses

import (
	"strings"

	"reasonix/internal/provider"
)

// resolveEndpoints returns the trimmed base URL and the URL requests POST to.
// A request_url that only repeats the base is treated as unset.
func resolveEndpoints(base, requestURL string) (string, string) {
	baseURL := strings.TrimRight(strings.TrimSpace(base), "/")
	requestURL = strings.TrimSpace(requestURL)
	if requestURL == "" || provider.EndpointOverrideRepeatsBase(requestURL, baseURL) {
		requestURL = baseURL + "/responses"
	}
	return baseURL, requestURL
}
