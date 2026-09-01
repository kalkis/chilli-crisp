// Package vars resolves {{variable}} placeholders in request templates.
package vars

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_][A-Za-z0-9_.-]*)\s*\}\}`)

// Resolver expands placeholders against layered variable maps. Earlier
// layers take precedence over later ones.
type Resolver struct {
	layers []map[string]string
}

// NewResolver builds a Resolver from precedence-ordered layers
// (highest priority first). Nil layers are allowed.
func NewResolver(layers ...map[string]string) *Resolver {
	return &Resolver{layers: layers}
}

// Lookup returns the value of name from the highest-priority layer that
// defines it.
func (r *Resolver) Lookup(name string) (string, bool) {
	for _, layer := range r.layers {
		if v, ok := layer[name]; ok {
			return v, true
		}
	}
	return "", false
}

// maxExpandDepth bounds nested expansion, so circular definitions fail
// instead of looping.
const maxExpandDepth = 10

// Expand replaces every {{name}} in s, recursively when a value itself
// contains placeholders. It returns an error naming all placeholders that no
// layer defines, or a circular-reference error when expansion never settles.
func (r *Resolver) Expand(s string) (string, error) {
	for range maxExpandDepth {
		var missing []string
		replaced := false
		out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
			name := placeholder.FindStringSubmatch(m)[1]
			if v, ok := r.Lookup(name); ok {
				replaced = true
				return v
			}
			missing = append(missing, name)
			return m
		})
		if len(missing) > 0 {
			return out, fmt.Errorf("undefined variable(s): %s", strings.Join(dedupe(missing), ", "))
		}
		s = out
		if !replaced {
			return s, nil
		}
	}
	if !placeholder.MatchString(s) {
		return s, nil
	}
	return s, fmt.Errorf("variable expansion exceeded %d levels: circular reference in %s",
		maxExpandDepth, strings.Join(Names(s), ", "))
}

// Names returns the unique placeholder names referenced in s, sorted.
func Names(s string) []string {
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		names = append(names, m[1])
	}
	return dedupe(names)
}

func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
