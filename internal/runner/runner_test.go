package runner

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/store"
)

// testWorkspace makes a workspace whose history lands in a temp directory
// rather than the user's real state directory, which is where a run would
// otherwise write it.
func testWorkspace(t *testing.T) *store.Workspace {
	t.Helper()
	t.Setenv(store.StateDirEnv, t.TempDir())
	w, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestStatusMatches(t *testing.T) {
	cases := []struct {
		assert string
		code   int
		want   bool
	}{
		{"", 500, true},
		{"200", 200, true},
		{"200", 201, false},
		{"2xx", 204, true},
		{"2XX", 204, true},
		{"2xx", 301, false},
		{"5xx", 503, true},
	}
	for _, c := range cases {
		if got := statusMatches(c.assert, c.code); got != c.want {
			t.Errorf("statusMatches(%q, %d) = %v, want %v", c.assert, c.code, got, c.want)
		}
	}
}

func TestRunCollection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/broken":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	w := testWorkspace(t)
	col := &model.Collection{
		Name: "smoke",
		Vars: map[string]string{"baseUrl": srv.URL},
		Requests: []model.Request{
			{Name: "health", Method: "GET", URL: "{{baseUrl}}/ok"},
			{Name: "broken", Method: "GET", URL: "{{baseUrl}}/broken"},
		},
	}
	if err := w.SaveCollection(col); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok, err := Run(context.Background(), w, "smoke", Options{AssertStatus: "2xx"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("run should fail: /broken returns 500")
	}
	text := out.String()
	if !strings.Contains(text, "PASS") || !strings.Contains(text, "FAIL") {
		t.Errorf("output should contain PASS and FAIL lines:\n%s", text)
	}

	// Single-request filter passes.
	ok, err = Run(context.Background(), w, "smoke", Options{Request: "health", AssertStatus: "200"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("health request should pass")
	}

	// Both runs recorded history (2 + 1 requests).
	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 history entries, got %d", len(entries))
	}

	if _, err := Run(context.Background(), w, "smoke", Options{Request: "nope"}, &out); err == nil {
		t.Error("unknown request name should error")
	}
	if _, err := Run(context.Background(), w, "missing", Options{}, &out); err == nil {
		t.Error("unknown collection should error")
	}
}

// TestRunStopsOnCancelledContext covers an interrupted headless run: the
// request in flight is reported but not logged, the requests after it never
// run, and the run ends with an error rather than a misleading assertion
// failure.
func TestRunStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Cancel from inside the first request so the abort lands mid-flight,
		// which is what Ctrl-C actually does.
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()

	w := testWorkspace(t)
	col := &model.Collection{
		Name: "slow",
		Vars: map[string]string{"baseUrl": srv.URL},
		Requests: []model.Request{
			{Name: "first", Method: "GET", URL: "{{baseUrl}}/a"},
			{Name: "second", Method: "GET", URL: "{{baseUrl}}/b"},
			{Name: "third", Method: "GET", URL: "{{baseUrl}}/c"},
		},
	}
	if err := w.SaveCollection(col); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok, err := Run(ctx, w, "slow", Options{}, &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run should report cancellation, got err=%v", err)
	}
	if ok {
		t.Error("an interrupted run must not report success")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("only the first request should have been sent, got %d", n)
	}
	if !strings.Contains(out.String(), "first") {
		t.Errorf("the in-flight request should still be reported:\n%s", out.String())
	}
	if strings.Contains(out.String(), "second") {
		t.Errorf("requests after the interrupt should not run:\n%s", out.String())
	}

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("an aborted send is not a result; got %d history entries", len(entries))
	}
}

// TestRunUsesConfigTimeout proves the workspace config value reaches the HTTP
// client, and that an explicit --timeout still wins.
func TestRunUsesConfigTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := testWorkspace(t)
	cfg := filepath.Join(w.Root, store.ConfigName)
	if err := os.WriteFile(cfg, []byte("timeout: 20ms\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	col := &model.Collection{
		Name:     "slow",
		Vars:     map[string]string{"baseUrl": srv.URL},
		Requests: []model.Request{{Name: "sleepy", Method: "GET", URL: "{{baseUrl}}/"}},
	}
	if err := w.SaveCollection(col); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok, err := Run(context.Background(), w, "slow", Options{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a 20ms config timeout should fail a 300ms request")
	}

	// An explicit timeout overrides the config.
	out.Reset()
	ok, err = Run(context.Background(), w, "slow", Options{Timeout: 5 * time.Second}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("--timeout should override the config value:\n%s", out.String())
	}
}

// TestRunNoHistory covers the per-run override: results are still printed,
// nothing is written down. The workspace itself is left at the default
// recording level, so this is the flag doing it and not the config.
func TestRunNoHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := testWorkspace(t)
	col := &model.Collection{
		Name:     "smoke",
		Vars:     map[string]string{"baseUrl": srv.URL},
		Requests: []model.Request{{Name: "health", Method: "GET", URL: "{{baseUrl}}/ok"}},
	}
	if err := w.SaveCollection(col); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok, err := Run(context.Background(), w, "smoke", Options{NoHistory: true}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("the run should still pass:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "health") {
		t.Errorf("the result should still be printed:\n%s", out.String())
	}

	entries, err := w.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("--no-history should record nothing, got %d entries", len(entries))
	}

	// Without the flag the same run does record, so the workspace was never
	// the reason the log was empty.
	if _, err := Run(context.Background(), w, "smoke", Options{}, &out); err != nil {
		t.Fatal(err)
	}
	if entries, err := w.History(0); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Errorf("expected 1 entry from the unflagged run, got %d", len(entries))
	}
}
