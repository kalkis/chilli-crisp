// Package openapi imports OpenAPI 3.x and Swagger 2.0 specs (JSON or YAML)
// into the chilli-crisp model, generating one request per operation with a
// sample body derived from the schema.
package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// methodOrder fixes request ordering per path for deterministic imports.
// It has no QUERY entry on purpose: OpenAPI has no way to declare one, and
// PathItem.Operations only ever returns the spec's fixed operation fields.
var methodOrder = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// Import parses an OpenAPI 3.x or Swagger 2.0 spec.
func Import(data []byte) (*model.Collection, error) {
	jsonData, err := toJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}

	var probe struct {
		Swagger string `json:"swagger"`
		OpenAPI string `json:"openapi"`
	}
	if err := json.Unmarshal(jsonData, &probe); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}

	var doc *openapi3.T
	switch {
	case strings.HasPrefix(probe.Swagger, "2"):
		var v2 openapi2.T
		if err := json.Unmarshal(jsonData, &v2); err != nil {
			return nil, fmt.Errorf("parse swagger 2.0 spec: %w", err)
		}
		doc, err = openapi2conv.ToV3(&v2)
		if err != nil {
			return nil, fmt.Errorf("convert swagger 2.0 to openapi 3: %w", err)
		}
	case probe.OpenAPI != "":
		loader := openapi3.NewLoader()
		doc, err = loader.LoadFromData(jsonData)
		if err != nil {
			return nil, fmt.Errorf("parse openapi 3 spec: %w", err)
		}
	default:
		return nil, fmt.Errorf("not an OpenAPI/Swagger spec (no \"openapi\" or \"swagger\" version field)")
	}

	return convert(doc), nil
}

// toJSON accepts JSON as-is and converts YAML input to JSON.
func toJSON(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return data, nil
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

var pathParam = regexp.MustCompile(`\{([^{}]+)\}`)

func convert(doc *openapi3.T) *model.Collection {
	col := &model.Collection{
		Name: "api",
		Vars: map[string]string{"baseUrl": "http://localhost:8080"},
	}
	if doc.Info != nil && doc.Info.Title != "" {
		col.Name = doc.Info.Title
	}
	if len(doc.Servers) > 0 && doc.Servers[0].URL != "" {
		col.Vars["baseUrl"] = strings.TrimSuffix(doc.Servers[0].URL, "/")
	}
	col.Auth = convertSecurity(doc, col)

	if doc.Paths == nil {
		return col
	}
	paths := make([]string, 0)
	for p := range doc.Paths.Map() {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		item := doc.Paths.Value(path)
		ops := item.Operations()
		for _, method := range methodOrder {
			op, ok := ops[method]
			if !ok {
				continue
			}
			col.Requests = append(col.Requests, convertOperation(col, method, path, item, op))
		}
	}
	return col
}

func convertOperation(col *model.Collection, method, path string, item *openapi3.PathItem, op *openapi3.Operation) model.Request {
	name := op.OperationID
	if name == "" {
		name = method + " " + path
	}

	// Rewrite OpenAPI path templating ({id}) to our {{id}} variables.
	url := "{{baseUrl}}" + pathParam.ReplaceAllString(path, "{{$1}}")

	r := model.Request{Name: name, Method: method, URL: url}

	params := append(append([]*openapi3.ParameterRef{}, item.Parameters...), op.Parameters...)
	for _, pref := range params {
		p := pref.Value
		if p == nil {
			continue
		}
		value := paramSample(p)
		switch p.In {
		case openapi3.ParameterInQuery:
			r.Query = append(r.Query, model.Param{Name: p.Name, Value: value, Disabled: !p.Required})
		case openapi3.ParameterInHeader:
			r.Headers = append(r.Headers, model.Param{Name: p.Name, Value: value, Disabled: !p.Required})
		case openapi3.ParameterInPath:
			if _, exists := col.Vars[p.Name]; !exists {
				if value == "" {
					value = "1"
				}
				col.Vars[p.Name] = value
			}
		}
	}

	r.Body = convertRequestBody(op.RequestBody)
	return r
}

func paramSample(p *openapi3.Parameter) string {
	if p.Example != nil {
		return fmt.Sprintf("%v", p.Example)
	}
	if p.Schema != nil && p.Schema.Value != nil {
		if v := sample(p.Schema, 0); v != nil {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func convertRequestBody(ref *openapi3.RequestBodyRef) *model.Body {
	if ref == nil || ref.Value == nil {
		return nil
	}
	content := ref.Value.Content
	if mt := content.Get("application/json"); mt != nil {
		v := any(map[string]any{})
		if mt.Example != nil {
			v = mt.Example
		} else if mt.Schema != nil {
			if s := sample(mt.Schema, 0); s != nil {
				v = s
			}
		}
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			data = []byte("{}")
		}
		return &model.Body{Type: model.BodyJSON, Content: string(data)}
	}
	if mt := content.Get("application/x-www-form-urlencoded"); mt != nil && mt.Schema != nil && mt.Schema.Value != nil {
		var form []model.Param
		for _, name := range sortedKeys(mt.Schema.Value.Properties) {
			v := sample(mt.Schema.Value.Properties[name], 0)
			form = append(form, model.Param{Name: name, Value: fmt.Sprintf("%v", v)})
		}
		return &model.Body{Type: model.BodyForm, Form: form}
	}
	return nil
}

const maxSampleDepth = 8

// sample builds an example value for a schema, preferring declared
// examples and defaults over type-derived placeholders.
func sample(ref *openapi3.SchemaRef, depth int) any {
	if ref == nil || ref.Value == nil || depth > maxSampleDepth {
		return nil
	}
	s := ref.Value
	if s.Example != nil {
		return s.Example
	}
	if s.Default != nil {
		return s.Default
	}
	if len(s.Enum) > 0 {
		return s.Enum[0]
	}
	if len(s.AllOf) > 0 {
		merged := map[string]any{}
		for _, sub := range s.AllOf {
			if m, ok := sample(sub, depth+1).(map[string]any); ok {
				for k, v := range m {
					merged[k] = v
				}
			}
		}
		return merged
	}
	if len(s.OneOf) > 0 {
		return sample(s.OneOf[0], depth+1)
	}
	if len(s.AnyOf) > 0 {
		return sample(s.AnyOf[0], depth+1)
	}

	switch {
	case s.Type.Is(openapi3.TypeObject) || len(s.Properties) > 0:
		obj := map[string]any{}
		for _, name := range sortedKeys(s.Properties) {
			obj[name] = sample(s.Properties[name], depth+1)
		}
		return obj
	case s.Type.Is(openapi3.TypeArray):
		item := sample(s.Items, depth+1)
		if item == nil {
			return []any{}
		}
		return []any{item}
	case s.Type.Is(openapi3.TypeString):
		switch s.Format {
		case "date-time":
			return "2026-01-01T00:00:00Z"
		case "date":
			return "2026-01-01"
		case "email":
			return "user@example.com"
		case "uuid":
			return "00000000-0000-0000-0000-000000000000"
		default:
			return "string"
		}
	case s.Type.Is(openapi3.TypeInteger):
		return 1
	case s.Type.Is(openapi3.TypeNumber):
		return 1.0
	case s.Type.Is(openapi3.TypeBoolean):
		return true
	default:
		return nil
	}
}

// convertSecurity maps the spec's security schemes to a collection-level
// auth default. Credential values are left as {{variables}} for the user to
// define in an environment — except when the scheme carries an x-sample-*
// extension (x-sample-token, x-sample-apikey, x-sample-username,
// x-sample-password), whose value seeds the matching collection var so demo
// APIs import ready to send.
func convertSecurity(doc *openapi3.T, col *model.Collection) *model.Auth {
	if doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return nil
	}
	// Prefer the scheme named by the first global security requirement;
	// otherwise fall back to the first defined scheme.
	var chosen *openapi3.SecurityScheme
	for _, req := range doc.Security {
		for name := range req {
			if ref, ok := doc.Components.SecuritySchemes[name]; ok && ref.Value != nil {
				chosen = ref.Value
				break
			}
		}
		if chosen != nil {
			break
		}
	}
	if chosen == nil {
		for _, name := range sortedKeys(doc.Components.SecuritySchemes) {
			if ref := doc.Components.SecuritySchemes[name]; ref.Value != nil {
				chosen = ref.Value
				break
			}
		}
	}
	if chosen == nil {
		return nil
	}

	switch {
	case chosen.Type == "http" && chosen.Scheme == "basic":
		seedSampleVar(col, "username", chosen.Extensions, "x-sample-username")
		seedSampleVar(col, "password", chosen.Extensions, "x-sample-password")
		return &model.Auth{Type: model.AuthBasic, Username: "{{username}}", Password: "{{password}}"}
	case chosen.Type == "apiKey":
		in := model.APIKeyInHeader
		if chosen.In == "query" {
			in = model.APIKeyInQuery
		}
		seedSampleVar(col, "apiKey", chosen.Extensions, "x-sample-apikey")
		return &model.Auth{Type: model.AuthAPIKey, Key: chosen.Name, Value: "{{apiKey}}", In: in}
	default:
		// http bearer — and oauth2/openIdConnect, where a static bearer token
		// is the practical stand-in for manual API testing.
		seedSampleVar(col, "token", chosen.Extensions, "x-sample-token")
		return &model.Auth{Type: model.AuthBearer, Token: "{{token}}"}
	}
}

// seedSampleVar copies a string-valued x-sample-* extension into the
// collection vars; environments still override it.
func seedSampleVar(col *model.Collection, varName string, ext map[string]any, key string) {
	if _, exists := col.Vars[varName]; exists {
		return
	}
	if v, ok := ext[key].(string); ok && v != "" {
		col.Vars[varName] = v
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
