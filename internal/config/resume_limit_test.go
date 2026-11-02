package config

import "testing"

func TestUIConfigResumeLimit(t *testing.T) {
	const fallback = 10
	cases := []struct {
		name string
		set  int
		want int
	}{
		{"未配置沿用内置默认", 0, fallback},
		{"显式设置为更大值", 50, 50},
		{"显式设置为更小值", 3, 3},
		{"负值表示不限制", -1, -1},
		{"较大的负值同样不限制", -100, -100},
	}
	for _, tc := range cases {
		got := UIConfig{ResumeListLimit: tc.set}.ResumeLimit(fallback)
		if got != tc.want {
			t.Fatalf("%s: ResumeLimit(%d) with resume_list_limit=%d = %d, want %d",
				tc.name, fallback, tc.set, got, tc.want)
		}
	}
}

func TestUIConfigResumeLimitTOMLKey(t *testing.T) {
	// The key must round-trip through the user's config.toml under [ui].
	var cfg Config
	if _, err := decodeTOMLBytes([]byte("[ui]\nresume_list_limit = 42\n"), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if got := cfg.UI.ResumeLimit(10); got != 42 {
		t.Fatalf("resume_list_limit from TOML = %d, want 42", got)
	}
}

func TestUIConfigResumeLimitAbsentKeepsDefault(t *testing.T) {
	var cfg Config
	if _, err := decodeTOMLBytes([]byte("[ui]\ntheme = \"auto\"\n"), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if got := cfg.UI.ResumeLimit(10); got != 10 {
		t.Fatalf("resume_list_limit absent = %d, want the built-in 10", got)
	}
}
