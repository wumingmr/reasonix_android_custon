package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

// A browser parses the sanitized bytes as an XML document, so the output must be
// well-formed on its own. Emitting the element's namespace twice makes the
// whole document a parse error, and the picture silently fails to load.
func TestSanitizeMarkdownSVGStaysWellFormedForTheRenderer(t *testing.T) {
	app := NewApp()
	for _, test := range []struct {
		name string
		body string
	}{
		{"namespaced root", `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="1" height="1"/></svg>`},
		{"namespace-free root", `<svg width="10" height="10"><rect width="1" height="1"/></svg>`},
		{"prolog and comment", "<?xml version=\"1.0\"?>\n<!-- x -->\n<svg xmlns=\"http://www.w3.org/2000/svg\"><text x=\"1\" y=\"1\">hi</text></svg>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := app.SanitizeMarkdownSVG(test.body)
			if !view.OK {
				t.Fatalf("a valid SVG was refused: %+v", view)
			}
			if n := strings.Count(view.SVG, "xmlns="); n > 1 {
				t.Fatalf("the sanitized SVG carries %d xmlns attributes; a browser rejects the duplicate:\n%s", n, view.SVG)
			}
			var root struct {
				XMLName xml.Name
				Space   string `xml:"xmlns,attr"`
			}
			if err := xml.Unmarshal([]byte(view.SVG), &root); err != nil {
				t.Fatalf("the sanitized SVG is not well-formed XML: %v\n%s", err, view.SVG)
			}
			if root.XMLName.Local != "svg" || root.XMLName.Space != "http://www.w3.org/2000/svg" {
				t.Fatalf("the sanitized root is not an SVG document element: %+v\n%s", root.XMLName, view.SVG)
			}
		})
	}
}

func TestSanitizeMarkdownSVGRejectsEscapedCSSReferences(t *testing.T) {
	view := NewApp().SanitizeMarkdownSVG(`<svg><rect style="fill:u\72 l(https://example.invalid/pixel)"/></svg>`)
	if !view.OK {
		t.Fatalf("document should remain previewable: %+v", view)
	}
	if strings.Contains(view.SVG, "example.invalid") || strings.Contains(view.SVG, `\72`) {
		t.Fatalf("escaped external reference survived sanitizing: %s", view.SVG)
	}
}
