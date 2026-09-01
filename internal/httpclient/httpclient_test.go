package httpclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/vars"
)

func emptyResolver() *vars.Resolver { return vars.NewResolver() }

func TestResolveMergesQueryAndExpandsVars(t *testing.T) {
	res := vars.NewResolver(map[string]string{"baseUrl": "https://api.example.com", "id": "42"})
	r := &model.Request{
		Method: "get",
		URL:    "{{baseUrl}}/users?existing=1",
		Query: []model.Param{
			{Name: "id", Value: "{{id}}"},
			{Name: "skip", Value: "yes", Disabled: true},
		},
	}
	got, err := Resolve(r, nil, res)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "GET" {
		t.Errorf("method should be upper-cased, got %q", got.Method)
	}
	if !strings.Contains(got.URL, "existing=1") || !strings.Contains(got.URL, "id=42") {
		t.Errorf("query merge failed: %s", got.URL)
	}
	if strings.Contains(got.URL, "skip") {
		t.Errorf("disabled query param leaked: %s", got.URL)
	}
}

func TestResolveUndefinedVariableFails(t *testing.T) {
	r := &model.Request{Method: "GET", URL: "{{nope}}/x"}
	if _, err := Resolve(r, nil, emptyResolver()); err == nil {
		t.Fatal("expected error for undefined variable")
	}
}

func TestResolveAuthVariants(t *testing.T) {
	find := func(rr *Resolved, name string) string {
		for _, h := range rr.Headers {
			if strings.EqualFold(h.Name, name) {
				return h.Value
			}
		}
		return ""
	}

	t.Run("basic", func(t *testing.T) {
		r := &model.Request{URL: "http://x", Auth: &model.Auth{Type: model.AuthBasic, Username: "ada", Password: "pw"}}
		rr, err := Resolve(r, nil, emptyResolver())
		if err != nil {
			t.Fatal(err)
		}
		if got := find(rr, "Authorization"); got != "Basic YWRhOnB3" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("bearer from collection default", func(t *testing.T) {
		col := &model.Collection{Auth: &model.Auth{Type: model.AuthBearer, Token: "{{token}}"}}
		r := &model.Request{URL: "http://x"}
		rr, err := Resolve(r, col, vars.NewResolver(map[string]string{"token": "t0k"}))
		if err != nil {
			t.Fatal(err)
		}
		if got := find(rr, "Authorization"); got != "Bearer t0k" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("auth overrides manual header", func(t *testing.T) {
		r := &model.Request{
			URL:     "http://x",
			Headers: []model.Param{{Name: "authorization", Value: "stale"}},
			Auth:    &model.Auth{Type: model.AuthBearer, Token: "fresh"},
		}
		rr, err := Resolve(r, nil, emptyResolver())
		if err != nil {
			t.Fatal(err)
		}
		if got := find(rr, "Authorization"); got != "Bearer fresh" {
			t.Errorf("configured auth should override manual header, got %q", got)
		}
		count := 0
		for _, h := range rr.Headers {
			if strings.EqualFold(h.Name, "Authorization") {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly one Authorization header, got %d", count)
		}
	})

	t.Run("apikey in query", func(t *testing.T) {
		r := &model.Request{URL: "http://x/p", Auth: &model.Auth{Type: model.AuthAPIKey, Key: "api_key", Value: "s3cret", In: model.APIKeyInQuery}}
		rr, err := Resolve(r, nil, emptyResolver())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rr.URL, "api_key=s3cret") {
			t.Errorf("api key missing from query: %s", rr.URL)
		}
	})

	t.Run("apikey in header", func(t *testing.T) {
		r := &model.Request{URL: "http://x", Auth: &model.Auth{Type: model.AuthAPIKey, Key: "X-Api-Key", Value: "s3cret", In: model.APIKeyInHeader}}
		rr, err := Resolve(r, nil, emptyResolver())
		if err != nil {
			t.Fatal(err)
		}
		if got := find(rr, "X-Api-Key"); got != "s3cret" {
			t.Errorf("got %q", got)
		}
	})
}

func TestResolveBodyTypes(t *testing.T) {
	t.Run("json sets default content type", func(t *testing.T) {
		r := &model.Request{URL: "http://x", Body: &model.Body{Type: model.BodyJSON, Content: `{"a":1}`}}
		rr, err := Resolve(r, nil, emptyResolver())
		if err != nil {
			t.Fatal(err)
		}
		if rr.Body != `{"a":1}` || len(rr.Headers) != 1 || rr.Headers[0].Value != "application/json" {
			t.Errorf("got body %q headers %+v", rr.Body, rr.Headers)
		}
	})

	t.Run("explicit content type wins", func(t *testing.T) {
		r := &model.Request{
			URL:     "http://x",
			Headers: []model.Param{{Name: "Content-Type", Value: "application/vnd.custom+json"}},
			Body:    &model.Body{Type: model.BodyJSON, Content: `{}`},
		}
		rr, err := Resolve(r, nil, emptyResolver())
		if err != nil {
			t.Fatal(err)
		}
		if len(rr.Headers) != 1 || rr.Headers[0].Value != "application/vnd.custom+json" {
			t.Errorf("headers %+v", rr.Headers)
		}
	})

	t.Run("form encodes fields", func(t *testing.T) {
		r := &model.Request{URL: "http://x", Body: &model.Body{Type: model.BodyForm, Form: []model.Param{
			{Name: "name", Value: "Ada Lovelace"},
			{Name: "off", Value: "x", Disabled: true},
		}}}
		rr, err := Resolve(r, nil, emptyResolver())
		if err != nil {
			t.Fatal(err)
		}
		if rr.Body != "name=Ada+Lovelace" {
			t.Errorf("got %q", rr.Body)
		}
	})
}

func TestSendEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"path": r.URL.Path, "q": r.URL.Query().Get("verbose"), "echo": string(body),
		})
	}))
	defer srv.Close()

	res := vars.NewResolver(map[string]string{"baseUrl": srv.URL, "token": "t0k"})
	col := &model.Collection{Auth: &model.Auth{Type: model.AuthBearer, Token: "{{token}}"}}
	r := &model.Request{
		Name:   "create",
		Method: "POST",
		URL:    "{{baseUrl}}/v1/users",
		Query:  []model.Param{{Name: "verbose", Value: "true"}},
		Body:   &model.Body{Type: model.BodyJSON, Content: `{"name":"Ada"}`},
	}
	resp, err := New(5*time.Second).Send(context.Background(), r, col, res)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("got %d: %s", resp.StatusCode, resp.Body)
	}
	var payload struct{ Path, Q, Echo string }
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Path != "/v1/users" || payload.Q != "true" || payload.Echo != `{"name":"Ada"}` {
		t.Errorf("server saw %+v", payload)
	}
	if resp.Duration <= 0 {
		t.Error("duration not measured")
	}
}

func TestResolverForPrecedence(t *testing.T) {
	col := &model.Collection{
		Name:    "api",
		Vars:    map[string]string{"baseUrl": "http://col", "id": "col", "only": "col"},
		Folders: []model.Folder{{Name: "admin", Vars: map[string]string{"id": "folder", "baseUrl": "http://folder"}}},
	}
	env := map[string]string{"id": "env", "baseUrl": "http://env"}

	r := &model.Request{Folder: "admin", Vars: map[string]string{"id": "req"}}
	res := ResolverFor(r, col, env)
	for name, want := range map[string]string{
		"id":      "req",        // the request beats everything
		"baseUrl": "http://env", // the environment beats folder and collection
		"only":    "col",        // and the collection is the last resort
	} {
		if got, _ := res.Lookup(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// Without a request override the folder wins over the collection, and
	// without a folder the collection is what is left.
	plain := &model.Request{Folder: "admin"}
	if got, _ := ResolverFor(plain, col, nil).Lookup("id"); got != "folder" {
		t.Errorf("folder layer: id = %q, want folder", got)
	}
	loose := &model.Request{}
	if got, _ := ResolverFor(loose, col, nil).Lookup("id"); got != "col" {
		t.Errorf("collection layer: id = %q, want col", got)
	}
}

// TestResolverForIsNilSafe covers a replay whose collection is gone.
func TestResolverForIsNilSafe(t *testing.T) {
	res := ResolverFor(&model.Request{Vars: map[string]string{"id": "9"}}, nil, nil)
	if got, _ := res.Lookup("id"); got != "9" {
		t.Errorf("id = %q, want 9", got)
	}
}

func TestResolveAppliesRequestVarsThroughout(t *testing.T) {
	col := &model.Collection{Vars: map[string]string{"baseUrl": "http://example.test", "id": "1"}}
	r := &model.Request{
		Method:  "POST",
		URL:     "{{baseUrl}}/todos/{{id}}",
		Vars:    map[string]string{"id": "10"},
		Headers: []model.Param{{Name: "X-Todo", Value: "{{id}}"}},
		Body:    &model.Body{Type: model.BodyJSON, Content: `{"id":"{{id}}"}`},
	}
	out, err := Resolve(r, col, ResolverFor(r, col, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "http://example.test/todos/10" {
		t.Errorf("URL = %q", out.URL)
	}
	if out.Headers[0].Value != "10" {
		t.Errorf("header = %q, want 10", out.Headers[0].Value)
	}
	if out.Body != `{"id":"10"}` {
		t.Errorf("body = %q", out.Body)
	}
}

// hop records what one leg of a redirect chain actually received.
type hop struct {
	method      string
	contentType string
	body        string
}

// redirectRecorder serves a redirect with the given status at /start and
// records every request it sees.
func redirectRecorder(t *testing.T, status int) (*httptest.Server, *[]hop) {
	t.Helper()
	var hops []hop
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		hops = append(hops, hop{r.Method, r.Header.Get("Content-Type"), string(data)})
	}
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		record(w, r)
		http.Redirect(w, r, "/end", status)
	})
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) {
		record(w, r)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hops
}

func sendTo(t *testing.T, url, method, body string) *Response {
	t.Helper()
	r := &Resolved{Method: method, URL: url, Body: body}
	if body != "" {
		r.Headers = []model.Param{{Name: "Content-Type", Value: "application/json"}}
	}
	resp, err := New(5*time.Second).SendResolved(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestQuerySurvivesRedirects covers RFC 10008 section 3: a QUERY keeps its
// method across 301, 302, 307 and 308. Go's default rewrites 301 and 302 to a
// bodiless GET, so without preserveQuery the second hop is a GET.
func TestQuerySurvivesRedirects(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently,  // 301
		http.StatusFound,             // 302
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect, // 308
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, hops := redirectRecorder(t, status)
			resp := sendTo(t, srv.URL+"/start", MethodQuery, `{"q":"chilli"}`)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if len(*hops) != 2 {
				t.Fatalf("expected two hops, got %+v", *hops)
			}
			second := (*hops)[1]
			if second.method != MethodQuery {
				t.Errorf("method after redirect = %q, want QUERY", second.method)
			}
			if second.body != `{"q":"chilli"}` {
				t.Errorf("body after redirect = %q, want it replayed", second.body)
			}
			if second.contentType != "application/json" {
				t.Errorf("Content-Type after redirect = %q, want it kept", second.contentType)
			}
		})
	}
}

// TestPostRedirectBehaviourIsUntouched is the contrast case: preserveQuery
// must not change what any other method does.
func TestPostRedirectBehaviourIsUntouched(t *testing.T) {
	srv, hops := redirectRecorder(t, http.StatusMovedPermanently)
	sendTo(t, srv.URL+"/start", "POST", `{"a":1}`)
	if len(*hops) != 2 {
		t.Fatalf("expected two hops, got %+v", *hops)
	}
	if second := (*hops)[1]; second.method != "GET" || second.body != "" {
		t.Errorf("POST should still be downgraded by net/http, got %+v", second)
	}
}

func TestRedirectLoopStops(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/again", http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)

	_, err := New(5*time.Second).SendResolved(context.Background(),
		&Resolved{Method: MethodQuery, URL: srv.URL, Body: "{}"})
	if err == nil {
		t.Fatal("a redirect loop should fail rather than hang")
	}
	if !strings.Contains(err.Error(), "stopped after") {
		t.Errorf("error = %v, want the redirect cap", err)
	}
}
