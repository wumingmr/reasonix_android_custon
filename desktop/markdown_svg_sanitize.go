package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// SVG sanitizing shared by Markdown images and chat blocks. A strict XML pass
// drops executable or external content before bytes reach an image source.

// MarkdownSVGView is the renderer-safe result of sanitizing a chat code block.
// SVG is the sanitized markup; the caller turns it into an image source.
type MarkdownSVGView struct {
	OK     bool   `json:"ok"`
	SVG    string `json:"svg,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// The automatic preview ceiling keeps one pasted diagram from stalling the
// transcript. Past it the code block stays source with a short explanation.
const (
	markdownSVGPreviewMaxBytes    = 1 << 20
	markdownSVGPreviewMaxElements = 10000
	markdownSVGPreviewMaxDepth    = 128
)

// The SVG document namespace, and the attribute namespace `xmlns` itself is
// reported under by encoding/xml.
const markdownSVGNamespace = "http://www.w3.org/2000/svg"

func isNamespaceDeclaration(name xml.Name) bool {
	return name.Space == "xmlns" || (name.Space == "" && name.Local == "xmlns")
}

type svgSanitizeLimits struct {
	maxBytes    int
	maxElements int
	maxDepth    int
}

// SanitizeMarkdownSVG converts a model-authored SVG document into renderer-safe
// markup. It performs no file read and no network access; the content is the
// only input.
func (a *App) SanitizeMarkdownSVG(content string) MarkdownSVGView {
	sanitized, ok := sanitizeMarkdownSVG([]byte(content), svgSanitizeLimits{
		maxBytes:    markdownSVGPreviewMaxBytes,
		maxElements: markdownSVGPreviewMaxElements,
		maxDepth:    markdownSVGPreviewMaxDepth,
	})
	if !ok {
		return MarkdownSVGView{Reason: markdownSVGFailureReason(len(content))}
	}
	return MarkdownSVGView{OK: true, SVG: string(sanitized)}
}

// markdownSVGFailureReason distinguishes the one refusal the reader can act on
// (a document too large to preview) from malformed markup.
func markdownSVGFailureReason(size int) string {
	if size > markdownSVGPreviewMaxBytes {
		return "too-large"
	}
	return "invalid"
}

var markdownSVGForbiddenElements = map[string]bool{
	"animate":          true,
	"animatemotion":    true,
	"animatetransform": true,
	"audio":            true,
	"embed":            true,
	"foreignobject":    true,
	"iframe":           true,
	"object":           true,
	"script":           true,
	"set":              true,
	"style":            true,
	"video":            true,
}

func sanitizeMarkdownSVG(body []byte, limits svgSanitizeLimits) ([]byte, bool) {
	if limits.maxBytes > 0 && len(body) > limits.maxBytes {
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	trimmed = bytes.TrimPrefix(trimmed, []byte{0xef, 0xbb, 0xbf})
	trimmed = bytes.TrimSpace(trimmed)
	if len(trimmed) == 0 {
		return nil, false
	}

	decoder := xml.NewDecoder(bytes.NewReader(trimmed))
	decoder.Strict = true
	sanitizer := newSVGTokenSanitizer(limits)
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false
		}
		if !sanitizer.accept(token) {
			return nil, false
		}
	}
	return sanitizer.finish()
}

type svgTokenSanitizer struct {
	limits               svgSanitizeLimits
	out                  bytes.Buffer
	encoder              *xml.Encoder
	rootSeen             bool
	rootDepth, skipDepth int
	elements             int
}

func newSVGTokenSanitizer(limits svgSanitizeLimits) *svgTokenSanitizer {
	s := &svgTokenSanitizer{limits: limits}
	s.encoder = xml.NewEncoder(&s.out)
	return s
}

func (s *svgTokenSanitizer) accept(token xml.Token) bool {
	switch value := token.(type) {
	case xml.StartElement:
		return s.start(value)
	case xml.EndElement:
		return s.end(value)
	case xml.CharData:
		return s.text(value)
	case xml.Comment, xml.Directive, xml.ProcInst:
		return true
	default:
		return s.skipDepth > 0 || s.encoder.EncodeToken(value) == nil
	}
}

func (s *svgTokenSanitizer) start(value xml.StartElement) bool {
	s.elements++
	if s.limits.maxElements > 0 && s.elements > s.limits.maxElements {
		return false
	}
	if s.skipDepth > 0 {
		s.skipDepth++
		return true
	}
	name := strings.ToLower(value.Name.Local)
	if !s.acceptRoot(name, value.Name.Space) {
		return false
	}
	if markdownSVGForbiddenElements[name] {
		s.skipDepth = 1
		return true
	}
	value.Attr = safeMarkdownSVGAttributes(value.Attr)
	value.Name.Space = outputSVGNamespace(value.Name.Space, s.rootDepth == 0)
	s.rootDepth++
	return (s.limits.maxDepth <= 0 || s.rootDepth <= s.limits.maxDepth) && s.encoder.EncodeToken(value) == nil
}

func (s *svgTokenSanitizer) acceptRoot(name, namespace string) bool {
	if s.rootSeen {
		return s.rootDepth > 0
	}
	if name != "svg" || (namespace != "" && namespace != markdownSVGNamespace) {
		return false
	}
	s.rootSeen = true
	return true
}

func (s *svgTokenSanitizer) end(value xml.EndElement) bool {
	if s.skipDepth > 0 {
		s.skipDepth--
		return true
	}
	if s.rootDepth <= 0 {
		return false
	}
	value.Name.Space = outputSVGNamespace(value.Name.Space, s.rootDepth == 1)
	if s.encoder.EncodeToken(value) != nil {
		return false
	}
	s.rootDepth--
	return true
}

func (s *svgTokenSanitizer) text(value xml.CharData) bool {
	if s.skipDepth > 0 {
		return true
	}
	if !s.rootSeen || s.rootDepth == 0 {
		return len(bytes.TrimSpace(value)) == 0
	}
	return s.encoder.EncodeToken(value) == nil
}

func (s *svgTokenSanitizer) finish() ([]byte, bool) {
	if !s.rootSeen || s.rootDepth != 0 || s.skipDepth != 0 || s.encoder.Flush() != nil {
		return nil, false
	}
	return s.out.Bytes(), true
}

func outputSVGNamespace(namespace string, root bool) string {
	if root {
		return markdownSVGNamespace
	}
	if namespace == markdownSVGNamespace {
		return ""
	}
	return namespace
}

func safeMarkdownSVGAttributes(input []xml.Attr) []xml.Attr {
	attrs := input[:0]
	for _, attr := range input {
		name := strings.ToLower(attr.Name.Local)
		unsafeName := isNamespaceDeclaration(attr.Name) || strings.HasPrefix(name, "on") || name == "srcset" ||
			(attr.Name.Space == "http://www.w3.org/XML/1998/namespace" && name == "base")
		if unsafeName {
			continue
		}
		safe := safeMarkdownSVGAttributeValue(attr.Value)
		if name == "href" || name == "src" {
			safe = safeMarkdownSVGReference(attr.Value)
		}
		if safe {
			attrs = append(attrs, attr)
		}
	}
	return attrs
}

func safeMarkdownSVGReference(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(value, "#") {
		return true
	}
	for _, prefix := range []string{
		"data:image/png;base64,",
		"data:image/jpeg;base64,",
		"data:image/gif;base64,",
		"data:image/webp;base64,",
		"data:image/bmp;base64,",
		"data:image/x-icon;base64,",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func safeMarkdownSVGAttributeValue(raw string) bool {
	if strings.Contains(raw, `\`) {
		return false
	}
	value := strings.ToLower(raw)
	if strings.Contains(value, "javascript:") || strings.Contains(value, "vbscript:") || strings.Contains(value, "data:text/html") {
		return false
	}
	for {
		index := strings.Index(value, "url(")
		if index < 0 {
			return !strings.Contains(value, "@import") && !strings.Contains(value, "expression(")
		}
		value = value[index+4:]
		end := strings.IndexByte(value, ')')
		if end < 0 {
			return false
		}
		target := strings.Trim(strings.TrimSpace(value[:end]), "\"'")
		if !strings.HasPrefix(target, "#") {
			return false
		}
		value = value[end+1:]
	}
}
