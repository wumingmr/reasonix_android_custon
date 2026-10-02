package main

import "testing"

// markdownSVGBenchFixtureSource is the SVG the browser bench feeds to the
// preview. It deliberately omits xmlns: that is how a model usually writes an
// SVG, and it is the case that used to fail, because a namespace-free document
// is not SVG and the browser refuses it as an image. The bench cannot call this sanitizer, so it renders
// markdownSVGBenchFixtureOutput instead; this test is what keeps the two
// honest, because it fails the moment the sanitizer stops producing those
// exact bytes.
const markdownSVGBenchFixtureSource = `<svg viewBox="0 0 10 5">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#f60"/></linearGradient></defs>
  <rect width="10" height="5" fill="url(#g)"/>
  <text x="1" y="4">preview</text>
</svg>`

const markdownSVGBenchFixtureOutput = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 5">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#f60"></stop></linearGradient></defs>
  <rect width="10" height="5" fill="url(#g)"></rect>
  <text x="1" y="4">preview</text>
</svg>`

func TestMarkdownSVGBenchFixtureMatchesTheSanitizer(t *testing.T) {
	view := NewApp().SanitizeMarkdownSVG(markdownSVGBenchFixtureSource)
	if !view.OK {
		t.Fatalf("the bench fixture is no longer sanitizable: %+v", view)
	}
	if view.SVG != markdownSVGBenchFixtureOutput {
		t.Fatalf("the browser bench renders bytes the sanitizer no longer produces.\n got: %q\nwant: %q\n\nUpdate markdownSVGBenchFixtureOutput in desktop/frontend/bench/chat-file-reference-fixture.tsx to match.", view.SVG, markdownSVGBenchFixtureOutput)
	}
}
