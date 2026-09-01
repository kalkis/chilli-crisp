package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/vars"
)

// MethodQuery is the HTTP QUERY method (RFC 10008): safe, idempotent and
// cacheable, with the query itself carried in the request content. net/http
// has no constant for it.
const MethodQuery = "QUERY"

// maxResponseBody caps how much of a response body is read into memory.
const maxResponseBody = 10 << 20 // 10 MiB

// Response is the captured result of a sent request.
type Response struct {
	Status     string
	StatusCode int
	Headers    http.Header
	Body       []byte
	Truncated  bool // body hit the maxResponseBody cap
	Duration   time.Duration
}

// FormatSize renders a body size in human-readable units.
func FormatSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Client sends resolved requests.
type Client struct {
	http *http.Client
}

// New returns a Client with the given total-request timeout (0 = none).
func New(timeout time.Duration) *Client {
	return &Client{http: &http.Client{Timeout: timeout, CheckRedirect: preserveQuery}}
}

// Timeout is the per-request timeout this client was built with. It exists so
// that a caller can assert which workspace's configured timeout is in force —
// the TUI and the runner are meant to share one value, and a front end that
// rebuilds its client has to be checkable from outside this package.
func (c *Client) Timeout() time.Duration { return c.http.Timeout }

// maxRedirects matches the cap net/http applies when CheckRedirect is nil.
// Supplying our own hook means owning that cap too.
const maxRedirects = 10

// preserveQuery keeps a QUERY's method and content across a redirect, as
// RFC 10008 requires. net/http rewrites 301 and 302 to a bodiless GET for
// every method but GET and HEAD, which would send a request the user never
// built and then report it as the result; 307 and 308 it already leaves
// alone. Every other method keeps net/http's behaviour untouched.
func preserveQuery(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", len(via))
	}
	first := via[0]
	if first.Method != MethodQuery || req.Method == MethodQuery {
		return nil // not ours, or net/http kept the method already
	}
	req.Method = first.Method
	if first.GetBody == nil {
		return nil
	}
	body, err := first.GetBody()
	if err != nil {
		return fmt.Errorf("replay query content: %w", err)
	}
	req.Body = body
	req.GetBody = first.GetBody
	req.ContentLength = first.ContentLength
	// Dropping the body drops its content headers with it, and a QUERY whose
	// Content-Type is missing is one the server must reject.
	req.Header.Set("Content-Type", first.Header.Get("Content-Type"))
	return nil
}

// Send resolves r and performs the HTTP call, returning a captured response
// with wall-clock duration.
func (c *Client) Send(ctx context.Context, r *model.Request, col *model.Collection, res *vars.Resolver) (*Response, error) {
	resolved, err := Resolve(r, col, res)
	if err != nil {
		return nil, err
	}
	return c.SendResolved(ctx, resolved)
}

// SendResolved performs the HTTP call for an already-resolved request.
func (c *Client) SendResolved(ctx context.Context, r *Resolved) (*Response, error) {
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return nil, err
	}
	for _, h := range r.Headers {
		req.Header.Set(h.Name, h.Value)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	duration := time.Since(start)
	if err != nil {
		return nil, err
	}
	truncated := false
	if len(data) > maxResponseBody {
		data = data[:maxResponseBody]
		truncated = true
	}
	return &Response{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       data,
		Truncated:  truncated,
		Duration:   duration,
	}, nil
}
