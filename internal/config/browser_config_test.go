package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// A settings edit rewrites the whole file from the struct, so a section that
// does not render is a section the next edit deletes.
func TestBrowserConfigSurvivesRenderAndLoad(t *testing.T) {
	c := Default()
	c.Browser = BrowserConfig{
		Enabled:             true,
		Endpoint:            "http://127.0.0.1:9222",
		AllowRemoteEndpoint: true,
		ChromePath:          "/opt/chrome/chrome",
		ChromeArgs:          []string{"--proxy-server=socks5://127.0.0.1:1080"},
		UserDataDir:         "/tmp/profile",
		Headless:            true,
	}
	rendered := RenderTOML(c)
	if !strings.Contains(rendered, "[browser]") {
		t.Fatalf("rendered config has no [browser] section:\n%s", rendered)
	}
	var decoded Config
	if _, err := toml.Decode(rendered, &decoded); err != nil {
		t.Fatalf("decode rendered config: %v", err)
	}
	if !reflect.DeepEqual(decoded.Browser, c.Browser) {
		t.Fatalf("browser section round-trip:\n got %+v\nwant %+v", decoded.Browser, c.Browser)
	}
}

func TestBrowserBackendIsOffByDefault(t *testing.T) {
	if Default().Browser.Enabled {
		t.Fatal("the browser backend is enabled by default")
	}
	// The full render documents the switch; a project delta carries only what
	// the project actually changed.
	if rendered := RenderTOML(Default()); !strings.Contains(rendered, "enabled = false   # browser tools") {
		t.Fatalf("the rendered config does not document [browser]:\n%s", rendered)
	}
	if delta := RenderTOMLProjectDelta(Default()); strings.Contains(delta, "[browser]") {
		t.Fatalf("a default project delta rendered a [browser] section:\n%s", delta)
	}
}
