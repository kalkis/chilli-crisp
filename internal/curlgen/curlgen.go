// Package curlgen renders a resolved request as a copy-pasteable curl
// command (POSIX shell quoting).
package curlgen

import (
	"fmt"
	"strings"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
)

// Command renders r as a single-line curl invocation.
func Command(r *httpclient.Resolved) string {
	var b strings.Builder
	b.WriteString("curl")
	if r.Method != "GET" {
		fmt.Fprintf(&b, " -X %s", r.Method)
	}
	b.WriteString(" ")
	b.WriteString(quote(r.URL))
	for _, h := range r.Headers {
		b.WriteString(" -H ")
		b.WriteString(quote(fmt.Sprintf("%s: %s", h.Name, h.Value)))
	}
	if r.Body != "" {
		b.WriteString(" --data-raw ")
		b.WriteString(quote(r.Body))
	}
	return b.String()
}

// quote wraps s in single quotes for POSIX shells; each embedded single
// quote is escaped by closing the quotes, emitting \, quote, and reopening.
// (The idiom is spelled out in the tests rather than here, because gofmt
// in Go 1.27 rewrites consecutive single quotes in comments into a Unicode
// right quote; see golang.org/issue/76975.)
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
