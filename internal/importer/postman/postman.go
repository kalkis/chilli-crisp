// Package postman imports Postman Collection v2.x JSON exports into the
// chilli-crisp model. Postman's {{variable}} syntax is identical to ours,
// so variable references survive the import unchanged.
package postman

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/kalkis/chilli-crisp/internal/model"
)

type pmCollection struct {
	Info struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	} `json:"info"`
	Item     []pmItem     `json:"item"`
	Auth     *pmAuth      `json:"auth"`
	Variable []pmKeyValue `json:"variable"`
}

// pmItem is either a folder (Item set) or a request (Request set). Folders
// may carry their own auth, inherited by the requests inside them.
type pmItem struct {
	Name    string     `json:"name"`
	Item    []pmItem   `json:"item"`
	Request *pmRequest `json:"request"`
	Auth    *pmAuth    `json:"auth"`
}

type pmRequest struct {
	Method string       `json:"method"`
	Header []pmKeyValue `json:"header"`
	URL    pmURL        `json:"url"`
	Body   *pmBody      `json:"body"`
	Auth   *pmAuth      `json:"auth"`
}

type pmKeyValue struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled"`
}

// pmURL is either a bare string or an object with raw + query parts.
type pmURL struct {
	Raw   string
	Query []pmKeyValue
}

func (u *pmURL) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		u.Raw = s
		return nil
	}
	var obj struct {
		Raw   string       `json:"raw"`
		Query []pmKeyValue `json:"query"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	u.Raw = obj.Raw
	u.Query = obj.Query
	return nil
}

type pmBody struct {
	Mode       string       `json:"mode"`
	Raw        string       `json:"raw"`
	URLEncoded []pmKeyValue `json:"urlencoded"`
	FormData   []pmKeyValue `json:"formdata"`
	Options    struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

// pmAuth params are arrays of {key,value} in v2.1 but plain objects in
// v2.0; pmParams accepts both.
type pmAuth struct {
	Type   string   `json:"type"`
	Basic  pmParams `json:"basic"`
	Bearer pmParams `json:"bearer"`
	APIKey pmParams `json:"apikey"`
}

type pmParams map[string]string

func (p *pmParams) UnmarshalJSON(data []byte) error {
	var list []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	m := pmParams{}
	if err := json.Unmarshal(data, &list); err == nil {
		for _, kv := range list {
			var s string
			if json.Unmarshal(kv.Value, &s) == nil {
				m[kv.Key] = s
			}
		}
		*p = m
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	for k, v := range obj {
		if s, ok := v.(string); ok {
			m[k] = s
		}
	}
	*p = m
	return nil
}

// Import parses a Postman Collection v2.x export.
func Import(data []byte) (*model.Collection, error) {
	var pc pmCollection
	if err := json.Unmarshal(data, &pc); err != nil {
		return nil, fmt.Errorf("parse postman collection: %w", err)
	}
	if pc.Info.Name == "" && len(pc.Item) == 0 {
		return nil, fmt.Errorf("not a Postman collection export (missing info.name and item)")
	}

	col := &model.Collection{Name: pc.Info.Name, Auth: convertAuth(pc.Auth)}
	if col.Name == "" {
		col.Name = "imported"
	}
	for _, v := range pc.Variable {
		if col.Vars == nil {
			col.Vars = map[string]string{}
		}
		col.Vars[v.Key] = v.Value
	}
	collectItems(col, pc.Item, nil, nil)
	return col, nil
}

// collectItems walks Postman's folder tree. Nested folder names join into
// the request's Folder ("a / b"); folder-level auth is inherited by
// requests that don't declare their own, materialized onto each request
// since the model has no folder auth level.
func collectItems(col *model.Collection, items []pmItem, path []string, inherited *pmAuth) {
	for _, it := range items {
		if len(it.Item) > 0 || it.Request == nil {
			folderAuth := inherited
			if it.Auth != nil {
				folderAuth = it.Auth
			}
			// Concat, not append: appending onto path would hand the callee
			// a slice sharing this one's backing array, so a later sibling
			// could overwrite an element a deeper level is still holding.
			collectItems(col, it.Item, slices.Concat(path, []string{it.Name}), folderAuth)
			continue
		}
		col.Requests = append(col.Requests, convertRequest(it.Name, strings.Join(path, " / "), it.Request, inherited))
	}
}

func convertRequest(name, folder string, pr *pmRequest, inherited *pmAuth) model.Request {
	auth := convertAuth(pr.Auth)
	if auth == nil {
		auth = convertAuth(inherited)
	}
	r := model.Request{
		Name:   name,
		Folder: folder,
		Method: strings.ToUpper(pr.Method),
		URL:    stripQuery(pr.URL.Raw),
		Auth:   auth,
	}
	if r.Method == "" {
		r.Method = "GET"
	}
	for _, h := range pr.Header {
		r.Headers = append(r.Headers, model.Param{Name: h.Key, Value: h.Value, Disabled: h.Disabled})
	}
	for _, q := range pr.URL.Query {
		r.Query = append(r.Query, model.Param{Name: q.Key, Value: q.Value, Disabled: q.Disabled})
	}
	r.Body = convertBody(pr.Body)
	return r
}

// stripQuery removes the query string from Postman's raw URL, since query
// parts are imported individually from url.query.
func stripQuery(raw string) string {
	before, _, _ := strings.Cut(raw, "?")
	return before
}

func convertBody(b *pmBody) *model.Body {
	if b == nil {
		return nil
	}
	switch b.Mode {
	case "raw":
		bodyType := model.BodyText
		if b.Options.Raw.Language == "json" || looksLikeJSON(b.Raw) {
			bodyType = model.BodyJSON
		}
		return &model.Body{Type: bodyType, Content: b.Raw}
	case "urlencoded":
		return &model.Body{Type: model.BodyForm, Form: convertParams(b.URLEncoded)}
	case "formdata":
		// Multipart file uploads aren't supported; keep the text fields as
		// a url-encoded form so the data isn't lost.
		return &model.Body{Type: model.BodyForm, Form: convertParams(b.FormData)}
	default:
		return nil
	}
}

func convertParams(kvs []pmKeyValue) []model.Param {
	var out []model.Param
	for _, kv := range kvs {
		out = append(out, model.Param{Name: kv.Key, Value: kv.Value, Disabled: kv.Disabled})
	}
	return out
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

func convertAuth(a *pmAuth) *model.Auth {
	if a == nil {
		return nil
	}
	switch a.Type {
	case "basic":
		return &model.Auth{Type: model.AuthBasic, Username: a.Basic["username"], Password: a.Basic["password"]}
	case "bearer":
		return &model.Auth{Type: model.AuthBearer, Token: a.Bearer["token"]}
	case "apikey":
		in := model.APIKeyInHeader
		if a.APIKey["in"] == "query" {
			in = model.APIKeyInQuery
		}
		return &model.Auth{Type: model.AuthAPIKey, Key: a.APIKey["key"], Value: a.APIKey["value"], In: in}
	case "noauth":
		return &model.Auth{Type: model.AuthNone}
	default:
		// Unsupported scheme (oauth2, digest, ...): drop rather than guess.
		return nil
	}
}
