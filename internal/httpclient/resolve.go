// Package httpclient turns request templates into real HTTP calls: it
// expands variables, applies auth, sends the request, and captures a
// response summary with timing.
package httpclient

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/vars"
)

// Resolved is a request after variable expansion with auth and defaults
// applied — everything needed to send it or render it as a curl command.
type Resolved struct {
	Method  string
	URL     string // final URL including query string
	Headers []model.Param
	Body    string
}

// ResolverFor builds the variable precedence stack for one request, highest
// priority first:
//
//	request vars -> environment -> folder vars -> collection vars
//
// It is the single place that fixes that order, so the TUI, the runner, and
// "copy as curl" cannot drift apart. envVars is the selected environment's
// merged variables (see model.Environment.AllVars), or nil.
func ResolverFor(r *model.Request, col *model.Collection, envVars map[string]string) *vars.Resolver {
	var colVars, folderVars map[string]string
	if col != nil {
		colVars = col.Vars
		folderVars = col.FolderVars(r.Folder)
	}
	return vars.NewResolver(r.Vars, envVars, folderVars, colVars)
}

// Resolve expands r against res and produces the concrete request.
// Enabled query params (and query-type API keys) are merged into any query
// string already present in the URL. Configured auth overrides a manually
// set header of the same name.
func Resolve(r *model.Request, col *model.Collection, res *vars.Resolver) (*Resolved, error) {
	rawURL, err := res.Expand(r.URL)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("url %q: %w", rawURL, err)
	}

	q := u.Query()
	for _, p := range r.Query {
		if p.Disabled {
			continue
		}
		name, err := res.Expand(p.Name)
		if err != nil {
			return nil, fmt.Errorf("query param: %w", err)
		}
		value, err := res.Expand(p.Value)
		if err != nil {
			return nil, fmt.Errorf("query param %s: %w", name, err)
		}
		q.Add(name, value)
	}

	out := &Resolved{Method: strings.ToUpper(r.Method)}
	if out.Method == "" {
		out.Method = "GET"
	}

	for _, p := range r.Headers {
		if p.Disabled {
			continue
		}
		value, err := res.Expand(p.Value)
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", p.Name, err)
		}
		out.Headers = append(out.Headers, model.Param{Name: p.Name, Value: value})
	}

	if err := out.applyBody(r.Body, res); err != nil {
		return nil, err
	}
	if err := out.applyAuth(r.EffectiveAuth(col), res, q); err != nil {
		return nil, err
	}

	u.RawQuery = q.Encode()
	out.URL = u.String()
	return out, nil
}

func (out *Resolved) applyBody(b *model.Body, res *vars.Resolver) error {
	if b == nil || b.Type == model.BodyNone || b.Type == "" {
		return nil
	}
	switch b.Type {
	case model.BodyJSON, model.BodyText:
		content, err := res.Expand(b.Content)
		if err != nil {
			return fmt.Errorf("body: %w", err)
		}
		out.Body = content
		contentType := "application/json"
		if b.Type == model.BodyText {
			contentType = "text/plain"
		}
		out.setDefaultHeader("Content-Type", contentType)
	case model.BodyForm:
		form := url.Values{}
		for _, p := range b.Form {
			if p.Disabled {
				continue
			}
			value, err := res.Expand(p.Value)
			if err != nil {
				return fmt.Errorf("form field %s: %w", p.Name, err)
			}
			form.Add(p.Name, value)
		}
		out.Body = form.Encode()
		out.setDefaultHeader("Content-Type", "application/x-www-form-urlencoded")
	default:
		return fmt.Errorf("unknown body type %q", b.Type)
	}
	return nil
}

func (out *Resolved) applyAuth(a *model.Auth, res *vars.Resolver, q url.Values) error {
	if a == nil || a.Type == model.AuthNone || a.Type == "" {
		return nil
	}
	expand := func(field, s string) (string, error) {
		v, err := res.Expand(s)
		if err != nil {
			return "", fmt.Errorf("auth %s: %w", field, err)
		}
		return v, nil
	}
	switch a.Type {
	case model.AuthBasic:
		user, err := expand("username", a.Username)
		if err != nil {
			return err
		}
		pass, err := expand("password", a.Password)
		if err != nil {
			return err
		}
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		out.setHeader("Authorization", "Basic "+cred)
	case model.AuthBearer:
		token, err := expand("token", a.Token)
		if err != nil {
			return err
		}
		out.setHeader("Authorization", "Bearer "+token)
	case model.AuthAPIKey:
		key, err := expand("key", a.Key)
		if err != nil {
			return err
		}
		value, err := expand("value", a.Value)
		if err != nil {
			return err
		}
		if a.In == model.APIKeyInQuery {
			q.Set(key, value)
		} else {
			out.setHeader(key, value)
		}
	default:
		return fmt.Errorf("unknown auth type %q", a.Type)
	}
	return nil
}

// setHeader sets a header, replacing any existing value (case-insensitive).
func (out *Resolved) setHeader(name, value string) {
	for i, h := range out.Headers {
		if strings.EqualFold(h.Name, name) {
			out.Headers[i] = model.Param{Name: name, Value: value}
			return
		}
	}
	out.Headers = append(out.Headers, model.Param{Name: name, Value: value})
}

// setDefaultHeader sets a header only if not already present.
func (out *Resolved) setDefaultHeader(name, value string) {
	for _, h := range out.Headers {
		if strings.EqualFold(h.Name, name) {
			return
		}
	}
	out.Headers = append(out.Headers, model.Param{Name: name, Value: value})
}
