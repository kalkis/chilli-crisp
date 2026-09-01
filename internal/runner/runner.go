// Package runner executes collections headlessly (for scripting and CI),
// with simple status assertions and per-request pass/fail output.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/store"
	"github.com/kalkis/chilli-crisp/internal/vars"
)

// Options controls a headless run.
type Options struct {
	Env          string        // environment name; empty = no environment
	Request      string        // run only this request; empty = whole collection
	AssertStatus string        // "2xx", "204", ...; empty = pass unless transport error
	Timeout      time.Duration // per-request timeout; 0 = the workspace default
	Verbose      bool          // print response bodies
	NoHistory    bool          // do not record this run in the request log
}

// Result is the outcome of one request.
type Result struct {
	Request  model.Request
	Response *httpclient.Response
	Err      error
	Pass     bool
}

// Run executes requests from the named collection and writes a line per
// request to out. It returns false if any request failed its assertion.
func Run(ctx context.Context, w *store.Workspace, collectionName string, opts Options, out io.Writer) (bool, error) {
	col, err := w.Collection(collectionName)
	if err != nil {
		return false, err
	}

	var envVars map[string]string
	if opts.Env != "" {
		env, err := w.Environment(opts.Env)
		if err != nil {
			return false, err
		}
		envVars = env.AllVars()
	}
	requests := col.Requests
	if opts.Request != "" {
		idxs := store.MatchRequests(col, opts.Request)
		if len(idxs) == 0 {
			return false, fmt.Errorf("request %q not found in collection %q", opts.Request, col.Name)
		}
		requests = nil
		for _, i := range idxs {
			requests = append(requests, col.Requests[i])
		}
	}

	if opts.Timeout <= 0 {
		t, err := w.RequestTimeout()
		if err != nil {
			return false, err
		}
		opts.Timeout = t
	}
	client := httpclient.New(opts.Timeout)

	allPass := true
	for i := range requests {
		// An interrupted run stops here rather than mid-request, so the
		// request that was in flight still gets its result line.
		if err := ctx.Err(); err != nil {
			return allPass, fmt.Errorf("run interrupted: %w", err)
		}
		// Built per request: the stack includes the request's own vars and
		// its folder's, so it cannot be hoisted out of the loop.
		resolver := httpclient.ResolverFor(&requests[i], col, envVars)
		res := execute(ctx, client, &requests[i], col, resolver, opts)
		if !res.Pass {
			allPass = false
		}
		printResult(out, res, opts.Verbose)
		// A send the operator aborted is not a result, so it is reported but
		// not logged — the same rule the TUI applies to esc.
		if errors.Is(res.Err, context.Canceled) {
			continue
		}
		// --no-history drops the log for one run: a CI job against a real
		// environment can print its results without leaving them on the
		// runner. It only ever records less than the workspace config allows,
		// never more, because AppendHistory applies the configured mode
		// whatever the caller passes.
		if opts.NoHistory {
			continue
		}
		// History is best-effort; a failed write must not fail the run.
		_ = w.AppendHistory(store.NewHistoryEntry(col.Name, opts.Env, res.Request, res.Response, res.Err))
	}
	return allPass, nil
}

func execute(ctx context.Context, client *httpclient.Client, r *model.Request, col *model.Collection, resolver *vars.Resolver, opts Options) Result {
	resp, err := client.Send(ctx, r, col, resolver)
	res := Result{Request: *r, Response: resp, Err: err}
	res.Pass = err == nil && statusMatches(opts.AssertStatus, resp.StatusCode)
	return res
}

// statusMatches checks a code against an assertion like "201" or "2xx".
func statusMatches(assert string, code int) bool {
	if assert == "" {
		return true
	}
	assert = strings.ToLower(strings.TrimSpace(assert))
	if strings.HasSuffix(assert, "xx") && len(assert) == 3 {
		return strconv.Itoa(code/100) == assert[:1]
	}
	want, err := strconv.Atoi(assert)
	return err == nil && code == want
}

func printResult(out io.Writer, res Result, verbose bool) {
	label := "PASS"
	if !res.Pass {
		label = "FAIL"
	}
	if res.Err != nil {
		fmt.Fprintf(out, "%s  %-40s  error: %v\n", label, res.Request.QualifiedName(), res.Err)
		return
	}
	fmt.Fprintf(out, "%s  %-40s  %s  %s  %s\n",
		label, res.Request.QualifiedName(), res.Response.Status,
		res.Response.Duration.Round(time.Millisecond), httpclient.FormatSize(len(res.Response.Body)))
	if verbose && len(res.Response.Body) > 0 {
		fmt.Fprintf(out, "%s\n", res.Response.Body)
	}
}
