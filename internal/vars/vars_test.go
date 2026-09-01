package vars

import (
	"strings"
	"testing"
)

func TestExpandLayerPrecedence(t *testing.T) {
	r := NewResolver(
		map[string]string{"token": "local-secret"},
		map[string]string{"token": "shared", "baseUrl": "https://staging.example.com"},
	)
	got, err := r.Expand("{{baseUrl}}/v1?t={{ token }}")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://staging.example.com/v1?t=local-secret"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandUndefined(t *testing.T) {
	r := NewResolver(map[string]string{"a": "1"})
	_, err := r.Expand("{{a}} {{missing}} {{alsoMissing}} {{missing}}")
	if err == nil {
		t.Fatal("expected error for undefined variables")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing") || !strings.Contains(msg, "alsoMissing") {
		t.Errorf("error should name undefined variables, got: %v", msg)
	}
	// "missing" appeared twice in the template; deduplication means the
	// lowercase name shows up only once ("alsoMissing" doesn't match case).
	if strings.Count(msg, "missing") != 1 {
		t.Errorf("undefined names should be deduplicated, got: %v", msg)
	}
}

func TestExpandNoPlaceholders(t *testing.T) {
	r := NewResolver()
	got, err := r.Expand("plain text {not a var} {{}}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "plain text {not a var} {{}}" {
		t.Errorf("text without valid placeholders should pass through, got %q", got)
	}
}

func TestExpandNested(t *testing.T) {
	r := NewResolver(map[string]string{
		"scheme":  "https",
		"host":    "example.com",
		"baseUrl": "{{scheme}}://{{host}}",
		"apiUrl":  "{{baseUrl}}/api",
	})
	got, err := r.Expand("{{apiUrl}}/ping")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/api/ping" {
		t.Errorf("nested expansion: got %q", got)
	}
}

func TestExpandNestedUndefined(t *testing.T) {
	r := NewResolver(map[string]string{"token": "{{secret}}-suffix"})
	_, err := r.Expand("Bearer {{token}}")
	if err == nil || !strings.Contains(err.Error(), "undefined variable(s): secret") {
		t.Errorf("nested undefined var should report as undefined, got: %v", err)
	}
}

func TestExpandCircular(t *testing.T) {
	r := NewResolver(map[string]string{"a": "{{b}}", "b": "{{a}}"})
	_, err := r.Expand("{{a}}")
	if err == nil || !strings.Contains(err.Error(), "circular") {
		t.Errorf("cycle should report a circular reference, got: %v", err)
	}
}

func TestNames(t *testing.T) {
	names := Names("{{b}} {{a}} {{b}}")
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("got %v, want [a b]", names)
	}
}
