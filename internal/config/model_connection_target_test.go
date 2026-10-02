package config

import "testing"

func TestModelConnectionTargetRedactsCredentialsAndPaths(t *testing.T) {
	for input, want := range map[string]string{
		"https://user:secret@example.com:443/tenant-secret?key=secret#secret": "https://example.com:443",
		"http://127.0.0.1:8080/token":                                         "http://127.0.0.1:8080",
		"file:///secret":                                                      "", "not a URL": "",
	} {
		if got := SafeModelConnectionTarget(input); got != want {
			t.Fatalf("safe endpoint = %q, want %q", got, want)
		}
	}
}
