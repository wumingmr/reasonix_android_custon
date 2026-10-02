package secrets

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactJSONPreservesStructureAndCounters(t *testing.T) {
	input := []byte(`{"token_count":9007199254740993,"api_key":"a secret with spaces","nested":[{"output":"TOKEN=这是很长的中文测试凭证内容\nCookie: sid=abc; secure\npassword=\"escape-me\""}],"empty":[],"flag":true}`)
	got, err := RedactJSON(input)
	if err != nil || !json.Valid(got) || !utf8.Valid(got) {
		t.Fatalf("invalid diagnostic JSON: %q, %v", got, err)
	}
	for _, secret := range []string{"a secret with spaces", "escape-me", "sid=abc", "这是很长的中文测试凭证内容"} {
		if strings.Contains(string(got), secret) {
			t.Fatalf("diagnostic leaked %q", secret)
		}
	}
	for _, preserved := range []string{`"token_count":9007199254740993`, `"empty":[]`, `"flag":true`} {
		if !strings.Contains(string(got), preserved) {
			t.Fatalf("lost structural value %s: %s", preserved, got)
		}
	}
}

func TestRedactJSONRejectsInvalidDocument(t *testing.T) {
	for _, input := range []string{`{} {}`, `{"broken":`, `not json`} {
		if _, err := RedactJSON([]byte(input)); err == nil {
			t.Fatalf("accepted invalid document %q", input)
		}
	}
}
