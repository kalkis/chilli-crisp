package tui

import (
	"bytes"
	"strings"
	"sync"

	"charm.land/lipgloss/v2/compat"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// maxHighlightSize keeps chroma off multi-megabyte bodies, which would lag
// the UI; larger content renders plain.
const maxHighlightSize = 256 * 1024

// chromaStyle picks a style once per run based on the terminal background.
var chromaStyle = sync.OnceValue(func() *chroma.Style {
	name := "monokai"
	if !compat.HasDarkBackground {
		name = "friendly"
	}
	if s := styles.Get(name); s != nil {
		return s
	}
	return styles.Fallback
})

// highlight returns src with ANSI colors for the given chroma lexer name.
// On any failure (unknown lexer, tokenise error, oversized input) it
// returns src unchanged.
func highlight(src, lang string) string {
	if lang == "" || len(src) > maxHighlightSize {
		return src
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return src
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, src)
	if err != nil {
		return src
	}
	var b strings.Builder
	if err := formatters.Get("terminal256").Format(&b, chromaStyle(), it); err != nil {
		return src
	}
	return b.String()
}

// langFor maps a response Content-Type to a chroma lexer name, sniffing
// JSON-looking bodies when the header is missing or generic.
func langFor(contentType string, body []byte) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "json"):
		return "json"
	case strings.Contains(ct, "html"):
		return "html"
	case strings.Contains(ct, "xml"):
		return "xml"
	case strings.Contains(ct, "yaml"):
		return "yaml"
	case strings.Contains(ct, "javascript"):
		return "javascript"
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return "json"
	}
	return ""
}
