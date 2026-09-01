package curlgen

import (
	"strings"
	"testing"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/model"
)

func TestCommand(t *testing.T) {
	r := &httpclient.Resolved{
		Method: "POST",
		URL:    "https://api.example.com/v1/users?verbose=true",
		Headers: []model.Param{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer t0k"},
		},
		Body: `{"name":"O'Brien"}`,
	}
	got := Command(r)
	want := `curl -X POST 'https://api.example.com/v1/users?verbose=true' -H 'Content-Type: application/json' -H 'Authorization: Bearer t0k' --data-raw '{"name":"O'\''Brien"}'`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCommandSimpleGet(t *testing.T) {
	got := Command(&httpclient.Resolved{Method: "GET", URL: "http://localhost:8080/health"})
	if got != "curl 'http://localhost:8080/health'" {
		t.Errorf("got %q", got)
	}
}

// TestCommandHonoursRequestVars guards the "copy as curl is honest" rule
// across the request-level variable layer: curl renders a Resolved, so it can
// only agree with the send if the override was applied upstream.
func TestCommandHonoursRequestVars(t *testing.T) {
	col := &model.Collection{Vars: map[string]string{"baseUrl": "https://api.example.com", "id": "1"}}
	req := &model.Request{
		Method: "GET",
		URL:    "{{baseUrl}}/todos/{{id}}",
		Vars:   map[string]string{"id": "10"},
	}
	resolved, err := httpclient.Resolve(req, col, httpclient.ResolverFor(req, col, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := Command(resolved); !strings.Contains(got, "https://api.example.com/todos/10") {
		t.Errorf("curl command = %q, want the overridden path", got)
	}
}

func TestCommandRendersQuery(t *testing.T) {
	got := Command(&httpclient.Resolved{
		Method:  httpclient.MethodQuery,
		URL:     "https://api.example.com/search",
		Headers: []model.Param{{Name: "Content-Type", Value: "application/json"}},
		Body:    `{"q":"chilli"}`,
	})
	for _, want := range []string{"-X QUERY", "--data-raw", `{"q":"chilli"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("curl command %q is missing %q", got, want)
		}
	}
}
