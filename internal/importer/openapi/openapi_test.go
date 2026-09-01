package openapi

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kalkis/chilli-crisp/internal/model"
)

func TestImportOpenAPI3YAML(t *testing.T) {
	data, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	col, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}

	if col.Name != "Petstore" {
		t.Errorf("name: %q", col.Name)
	}
	if col.Vars["baseUrl"] != "https://petstore.example.com/api" {
		t.Errorf("baseUrl should come from servers (trailing slash trimmed): %v", col.Vars)
	}
	if col.Auth == nil || col.Auth.Type != model.AuthBearer || col.Auth.Token != "{{token}}" {
		t.Errorf("security should map to bearer auth: %+v", col.Auth)
	}

	byName := map[string]model.Request{}
	for _, r := range col.Requests {
		byName[r.Name] = r
	}
	if len(col.Requests) != 4 {
		t.Fatalf("got %d requests: %v", len(col.Requests), byName)
	}

	list := byName["listPets"]
	if list.Method != "GET" || list.URL != "{{baseUrl}}/pets" {
		t.Errorf("listPets: %+v", list)
	}
	var limit, tag *model.Param
	for i := range list.Query {
		switch list.Query[i].Name {
		case "limit":
			limit = &list.Query[i]
		case "tag":
			tag = &list.Query[i]
		}
	}
	if limit == nil || limit.Disabled || limit.Value != "20" {
		t.Errorf("required param with default: %+v", limit)
	}
	if tag == nil || !tag.Disabled {
		t.Errorf("optional param should be disabled: %+v", tag)
	}

	get := byName["getPet"]
	if get.URL != "{{baseUrl}}/pets/{{petId}}" {
		t.Errorf("path template not rewritten: %q", get.URL)
	}
	if col.Vars["petId"] != "7" {
		t.Errorf("path param example should seed vars: %v", col.Vars)
	}

	create := byName["createPet"]
	if create.Body == nil || create.Body.Type != model.BodyJSON {
		t.Fatalf("createPet body: %+v", create.Body)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(create.Body.Content), &payload); err != nil {
		t.Fatalf("sample body is not valid JSON: %v\n%s", err, create.Body.Content)
	}
	if payload["name"] != "Rex" {
		t.Errorf("schema example not used: %v", payload)
	}
	if payload["status"] != "available" {
		t.Errorf("first enum value not used: %v", payload)
	}
	if _, ok := payload["tags"].([]any); !ok {
		t.Errorf("array sample missing: %v", payload)
	}
}

func TestImportSwagger2(t *testing.T) {
	data, err := os.ReadFile("testdata/swagger2.json")
	if err != nil {
		t.Fatal(err)
	}
	col, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}
	if col.Name != "Legacy API" {
		t.Errorf("name: %q", col.Name)
	}
	if col.Vars["baseUrl"] != "https://legacy.example.com/v2" {
		t.Errorf("host/basePath should become baseUrl: %v", col.Vars)
	}
	if len(col.Requests) != 1 || col.Requests[0].Name != "listThings" {
		t.Errorf("requests: %+v", col.Requests)
	}
}

func TestImportSampleTokenExtension(t *testing.T) {
	spec := `
openapi: "3.0.3"
info: {title: Demo, version: "1"}
servers: [{url: http://localhost:3000}]
security: [{bearerAuth: []}]
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
      x-sample-token: letmein
paths:
  /things:
    get:
      operationId: listThings
      responses: {"200": {description: ok}}
`
	col, err := Import([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	if col.Auth == nil || col.Auth.Token != "{{token}}" {
		t.Fatalf("auth: %+v", col.Auth)
	}
	if col.Vars["token"] != "letmein" {
		t.Errorf("x-sample-token should seed the token var: %v", col.Vars)
	}
}

func TestImportNoSampleTokenLeavesVarUndefined(t *testing.T) {
	data, err := os.ReadFile("testdata/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	col, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := col.Vars["token"]; ok {
		t.Errorf("without x-sample-token the var must stay undefined: %v", col.Vars)
	}
}

func TestImportRejectsGarbage(t *testing.T) {
	if _, err := Import([]byte(`{"foo": "bar"}`)); err == nil {
		t.Error("expected error for spec without version field")
	}
	if _, err := Import([]byte("::: not yaml or json :::")); err == nil {
		t.Error("expected error for unparseable input")
	}
}
