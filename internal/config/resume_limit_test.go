package config

import (
	"strconv"
	"strings"
	"testing"
)

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

// RenderTOMLForScope 是全量重写 [ui] 段,少输出一个字段就等于删除该字段。
// 真实故障:resume_list_limit 定义了却没进渲染器,用户手写
// `resume_list_limit = -1` 后,任何触发配置回写的操作(改设置、跑 doctor)
// 都会把它静默抹掉,表现为"设置自己消失了"。这里锁住两个渲染入口。
func TestRenderTOMLPreservesResumeListLimit(t *testing.T) {
	cases := []struct {
		name string
		set  int
	}{
		{"负值表示不限制", -1},
		{"正数上限", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.UI.ResumeListLimit = tc.set
			out := RenderTOMLForScope(cfg, RenderScopeUser)
			if !strings.Contains(out, "resume_list_limit = "+strconv.Itoa(tc.set)) {
				t.Fatalf("RenderTOMLForScope dropped resume_list_limit=%d:\n%s", tc.set, out)
			}
			// 渲染结果必须能被重新读回同一个值,即真的能 round-trip。
			var back Config
			if _, err := decodeTOMLBytes([]byte(out), &back); err != nil {
				t.Fatalf("re-decode rendered config: %v", err)
			}
			if back.UI.ResumeListLimit != tc.set {
				t.Fatalf("round-trip resume_list_limit = %d, want %d", back.UI.ResumeListLimit, tc.set)
			}
		})
	}
}

func TestRenderTOMLCommentsResumeListLimitWhenUnset(t *testing.T) {
	// 未配置时应留一行注释说明可用,而不是静默缺席。
	out := RenderTOMLForScope(&Config{}, RenderScopeUser)
	if !strings.Contains(out, "# resume_list_limit") {
		t.Fatalf("want a commented hint for resume_list_limit:\n%s", out)
	}
}
