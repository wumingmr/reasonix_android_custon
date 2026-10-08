package config

import (
	"strings"
	"testing"
)

// ui.spinner = "static" 的解析与渲染。
// 渲染器这一项是硬要求:RenderTOMLForScope 全量重写 [ui] 段,漏掉它就等于
// 删除用户配置(这正是 resume_list_limit 曾经消失的根因)。
func TestUISpinnerStaticParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"static", true},
		{"STATIC", true},
		{" static ", true},
		{"", false},
		{"animated", false},
		{"nonsense", false},
	} {
		c := UIConfig{Spinner: tc.in}
		if got := c.UISpinnerStatic(); got != tc.want {
			t.Fatalf("spinner=%q → %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestUISpinnerTOMLKey(t *testing.T) {
	var cfg Config
	if _, err := decodeTOMLBytes([]byte("[ui]\nspinner = \"static\"\n"), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.EqualFold(cfg.UI.Spinner, "static") {
		t.Fatalf("spinner from TOML = %q, want static", cfg.UI.Spinner)
	}
}

func TestRenderTOMLPreservesSpinner(t *testing.T) {
	cfg := &Config{}
	cfg.UI.Spinner = "static"
	out := RenderTOMLForScope(cfg, RenderScopeUser)
	if !strings.Contains(out, `spinner = "static"`) {
		t.Fatalf("RenderTOMLForScope 丢掉了 spinner:\n%s", out)
	}
	var back Config
	if _, err := decodeTOMLBytes([]byte(out), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if !back.UI.UISpinnerStatic() {
		t.Fatal("round-trip 后 spinner 不再是 static")
	}
}

func TestRenderTOMLCommentsSpinnerWhenUnset(t *testing.T) {
	out := RenderTOMLForScope(&Config{}, RenderScopeUser)
	if !strings.Contains(out, "# spinner =") {
		t.Fatalf("未配置时应留注释提示:\n%s", out)
	}
}
