package config

import "net/url"

// SafeModelConnectionTarget omits paths, credentials, query and fragments.
// A hostname and port identify the selected connection without revealing URL
// embedded authentication or provider-specific secret path components.
func SafeModelConnectionTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
