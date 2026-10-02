package config

import (
	"net/url"
	"strings"
)

// RedactMCPConfigValue hides a credential-looking env or header value.
func RedactMCPConfigValue(key, value string) string {
	if looksSensitiveMCPKey(key) || looksSensitiveMCPValue(value) {
		return "<redacted>"
	}
	return value
}

func looksSensitiveMCPKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, needle := range []string{"auth", "token", "secret", "credential", "api_key", "api-key", "apikey", "cookie"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func looksSensitiveMCPQueryKey(key string) bool {
	return strings.EqualFold(strings.TrimSpace(key), "key") || looksSensitiveMCPKey(key)
}

func looksSensitiveMCPValue(value string) bool {
	lower := strings.ToLower(value)
	for _, needle := range []string{"access_token", "id_token", "refresh_token", "api_key", "api-key", "apikey", "bearer "} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// RedactMCPURL hides credential query values in an MCP endpoint.
func RedactMCPURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	u, err := url.Parse(trimmed)
	if err != nil || u == nil {
		if looksSensitiveMCPValue(raw) {
			return "<redacted>"
		}
		return raw
	}
	q := u.Query()
	changed := false
	for key := range q {
		if looksSensitiveMCPQueryKey(key) {
			q.Set(key, "<redacted>")
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}
