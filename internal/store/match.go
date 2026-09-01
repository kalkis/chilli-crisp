package store

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/model"
)

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true,
	httpclient.MethodQuery: true,
}

// MatchRequests returns the indices of the requests in col that a selector
// refers to. A selector is a request name, a "folder / name" qualified name,
// or a bare folder name (matching every request in that folder). It may be
// prefixed with an HTTP method to disambiguate requests that share a name,
// e.g. "POST manager / /drinks". Matching is slug-based, so punctuation and
// case differences are forgiven.
func MatchRequests(col *model.Collection, selector string) []int {
	// The whole selector wins first, so a folder or request whose name starts
	// with a method word (e.g. "post deploy") is not mistaken for a filter.
	if out := matchWith(col, "", selector); len(out) > 0 {
		return out
	}
	if method, rest := splitMethod(selector); method != "" {
		return matchWith(col, method, rest)
	}
	return nil
}

// matchWith scans col for requests matching want, optionally restricted to
// one HTTP method.
func matchWith(col *model.Collection, method, selector string) []int {
	want := Slug(selector)
	var out []int
	for i := range col.Requests {
		r := &col.Requests[i]
		if method != "" && r.Method != method {
			continue
		}
		if Slug(r.Name) == want || Slug(r.QualifiedName()) == want ||
			(r.Folder != "" && Slug(r.Folder) == want) {
			out = append(out, i)
		}
	}
	return out
}

// splitMethod peels a leading HTTP method off a selector, returning the
// method (upper-cased, or empty) and the remainder.
func splitMethod(selector string) (string, string) {
	first, rest, ok := strings.Cut(selector, " ")
	if m := strings.ToUpper(first); ok && httpMethods[m] {
		return m, rest
	}
	return "", selector
}

// copySuffix matches the " (copy)" / " (copy 2)" tail UniqueRequestName adds,
// so copying a copy ladders from the original base rather than stacking.
var copySuffix = regexp.MustCompile(` \(copy(?: \d+)?\)$`)

// UniqueRequestName returns a name for a request being added to col that
// nothing else in col already answers to: name itself when it is free, and
// otherwise the first unused "base (copy)", "base (copy 2)", …
//
// Request names are not required to be unique and this does not make them so —
// it keeps a *duplicate* from stealing its original's address. MatchRequests
// resolves --request by slug and returns every hit, which cli reports as an
// ambiguity, so a copy that kept its name would leave the original
// unreachable from the command line. Taken-ness is tested with MatchRequests
// itself rather than by comparing names, so the two cannot drift: a name that
// slugs like an existing request, its qualified name, or a folder is just as
// ambiguous as an identical one.
//
// The trailing " (copy)" is stripped before laddering, so a copy of
// "listTodos (copy)" is "listTodos (copy 2)" and not "listTodos (copy) (copy)".
// The verbatim name is still tried first, so pasting into a collection where
// nothing conflicts keeps the name it arrived with.
func UniqueRequestName(col *model.Collection, name string) string {
	if len(MatchRequests(col, name)) == 0 {
		return name
	}
	base := copySuffix.ReplaceAllString(name, "")
	for i := 1; ; i++ {
		candidate := base + " (copy)"
		if i > 1 {
			candidate = fmt.Sprintf("%s (copy %d)", base, i)
		}
		if len(MatchRequests(col, candidate)) == 0 {
			return candidate
		}
	}
}
