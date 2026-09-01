package tui

import (
	"strings"
	"testing"
)

func TestLangFor(t *testing.T) {
	cases := []struct {
		ct   string
		body string
		want string
	}{
		{"application/json; charset=utf-8", "", "json"},
		{"application/vnd.api+json", "", "json"},
		{"text/html", "<html>", "html"},
		{"application/xml", "", "xml"},
		{"text/plain", `{"sniffed": true}`, "json"},
		{"text/plain", "hello", ""},
		{"", "[1,2]", "json"},
	}
	for _, c := range cases {
		if got := langFor(c.ct, []byte(c.body)); got != c.want {
			t.Errorf("langFor(%q, %q) = %q, want %q", c.ct, c.body, got, c.want)
		}
	}
}

func TestHighlight(t *testing.T) {
	out := highlight(`{"a": 1}`, "json")
	if !strings.Contains(out, "\x1b[") {
		t.Error("highlighted JSON should contain ANSI escapes")
	}

	// Failure paths must return input unchanged.
	if got := highlight("plain", ""); got != "plain" {
		t.Errorf("empty lang: %q", got)
	}
	if got := highlight("x", "no-such-lexer"); got != "x" {
		t.Errorf("unknown lexer: %q", got)
	}
	big := strings.Repeat("a", maxHighlightSize+1)
	if got := highlight(big, "json"); got != big {
		t.Error("oversized input should pass through unhighlighted")
	}
}
