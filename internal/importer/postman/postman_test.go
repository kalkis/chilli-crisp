package postman

import (
	"os"
	"testing"

	"github.com/kalkis/chilli-crisp/internal/model"
)

func TestImportFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/collection.json")
	if err != nil {
		t.Fatal(err)
	}
	col, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}

	if col.Name != "My Service" {
		t.Errorf("name: %q", col.Name)
	}
	if col.Vars["baseUrl"] != "https://api.example.com" {
		t.Errorf("vars: %v", col.Vars)
	}
	if col.Auth == nil || col.Auth.Type != model.AuthBearer || col.Auth.Token != "{{token}}" {
		t.Errorf("collection auth: %+v", col.Auth)
	}
	if len(col.Requests) != 3 {
		t.Fatalf("got %d requests, want 3", len(col.Requests))
	}

	list := col.Requests[0]
	if list.Name != "list users" || list.Folder != "users" {
		t.Errorf("folder should be split from name: name=%q folder=%q", list.Name, list.Folder)
	}
	if list.URL != "{{baseUrl}}/v1/users" {
		t.Errorf("query string should be stripped from URL: %q", list.URL)
	}
	if len(list.Query) != 2 || !list.Query[1].Disabled {
		t.Errorf("query params: %+v", list.Query)
	}

	create := col.Requests[1]
	if create.Auth == nil || create.Auth.Type != model.AuthAPIKey || create.Auth.Key != "X-Api-Key" || create.Auth.In != model.APIKeyInHeader {
		t.Errorf("request auth: %+v", create.Auth)
	}
	if create.Body == nil || create.Body.Type != model.BodyJSON || create.Body.Content != `{"name": "Ada"}` {
		t.Errorf("body: %+v", create.Body)
	}

	login := col.Requests[2]
	if login.Body == nil || login.Body.Type != model.BodyForm || len(login.Body.Form) != 2 {
		t.Errorf("urlencoded body: %+v", login.Body)
	}
	if login.Body.Form[1].Value != "{{password}}" {
		t.Errorf("variable reference lost: %+v", login.Body.Form[1])
	}
}

func TestImportFolderAuthInheritance(t *testing.T) {
	data := []byte(`{
		"info": {"name": "folders", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
		"item": [
			{"name": "open", "item": [
				{"name": "ping", "request": {"method": "GET", "url": "http://x/ping"}}
			]},
			{"name": "secure", "auth": {"type": "bearer", "bearer": [{"key": "token", "value": "tok-folder"}]}, "item": [
				{"name": "inherits", "request": {"method": "GET", "url": "http://x/a"}},
				{"name": "overrides", "request": {"method": "GET", "url": "http://x/b",
					"auth": {"type": "bearer", "bearer": [{"key": "token", "value": "tok-own"}]}}},
				{"name": "explicit none", "request": {"method": "GET", "url": "http://x/c", "auth": {"type": "noauth"}}},
				{"name": "nested", "item": [
					{"name": "deep", "request": {"method": "GET", "url": "http://x/d"}}
				]}
			]}
		]
	}`)
	col, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]model.Request{}
	for _, r := range col.Requests {
		byName[r.Name] = r
	}

	if r := byName["ping"]; r.Auth != nil || r.Folder != "open" {
		t.Errorf("request in auth-less folder: %+v", r)
	}
	if r := byName["inherits"]; r.Auth == nil || r.Auth.Type != model.AuthBearer || r.Auth.Token != "tok-folder" {
		t.Errorf("folder auth should be inherited: %+v", byName["inherits"].Auth)
	}
	if r := byName["overrides"]; r.Auth == nil || r.Auth.Token != "tok-own" {
		t.Errorf("request auth should beat folder auth: %+v", byName["overrides"].Auth)
	}
	if r := byName["explicit none"]; r.Auth == nil || r.Auth.Type != model.AuthNone {
		t.Errorf("explicit noauth should block inheritance: %+v", byName["explicit none"].Auth)
	}
	if r := byName["deep"]; r.Folder != "secure / nested" || r.Auth == nil || r.Auth.Token != "tok-folder" {
		t.Errorf("nested folder should join path and inherit auth: folder=%q auth=%+v", r.Folder, r.Auth)
	}
}

// TestNestedSiblingFoldersKeepDistinctPaths pins the folder-path joining that
// collectItems does while recursing. Sibling folders at depth are the shape
// that would expose a path slice shared between recursion levels.
func TestNestedSiblingFoldersKeepDistinctPaths(t *testing.T) {
	data := []byte(`{
		"info": {"name": "deep", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
		"item": [
			{"name": "top", "item": [
				{"name": "left", "item": [
					{"name": "leftmost", "item": [
						{"name": "a", "request": {"method": "GET", "url": "http://x/a"}}
					]},
					{"name": "b", "request": {"method": "GET", "url": "http://x/b"}}
				]},
				{"name": "right", "item": [
					{"name": "rightmost", "item": [
						{"name": "c", "request": {"method": "GET", "url": "http://x/c"}}
					]},
					{"name": "d", "request": {"method": "GET", "url": "http://x/d"}}
				]}
			]}
		]
	}`)
	col, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"a": "top / left / leftmost",
		"b": "top / left",
		"c": "top / right / rightmost",
		"d": "top / right",
	}
	if len(col.Requests) != len(want) {
		t.Fatalf("got %d requests, want %d", len(col.Requests), len(want))
	}
	for _, r := range col.Requests {
		if got := r.Folder; got != want[r.Name] {
			t.Errorf("request %q has folder %q, want %q", r.Name, got, want[r.Name])
		}
	}
}

func TestImportRejectsGarbage(t *testing.T) {
	if _, err := Import([]byte(`{"foo": 1}`)); err == nil {
		t.Error("expected error for non-Postman JSON")
	}
	if _, err := Import([]byte(`not json`)); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestImportV20AuthObjectForm(t *testing.T) {
	// Postman v2.0 uses an object for auth params instead of a key/value list.
	data := []byte(`{
		"info": {"name": "v2.0"},
		"item": [{"name": "r", "request": {
			"method": "GET",
			"url": "http://x",
			"auth": {"type": "basic", "basic": {"username": "ada", "password": "pw"}}
		}}]
	}`)
	col, err := Import(data)
	if err != nil {
		t.Fatal(err)
	}
	a := col.Requests[0].Auth
	if a == nil || a.Type != model.AuthBasic || a.Username != "ada" || a.Password != "pw" {
		t.Errorf("auth: %+v", a)
	}
}
