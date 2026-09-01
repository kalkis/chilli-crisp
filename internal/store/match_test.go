package store

import (
	"testing"

	"github.com/kalkis/chilli-crisp/internal/model"
)

func matchCol() *model.Collection {
	return &model.Collection{
		Name: "c",
		Requests: []model.Request{
			{Name: "/drinks", Folder: "public", Method: "GET"},
			{Name: "/drinks", Folder: "public", Method: "POST"},
			{Name: "/drinks", Folder: "manager", Method: "GET"},
			{Name: "/drinks", Folder: "manager", Method: "POST"},
			{Name: "health", Method: "GET"},
			{Name: "run", Folder: "post deploy", Method: "POST"},
			{Name: "verify", Folder: "post deploy", Method: "GET"},
		},
	}
}

func TestMatchRequestsByName(t *testing.T) {
	col := matchCol()
	if got := MatchRequests(col, "health"); len(got) != 1 || got[0] != 4 {
		t.Errorf("bare name: %v", got)
	}
	if got := MatchRequests(col, "/drinks"); len(got) != 4 {
		t.Errorf("shared name should match all: %v", got)
	}
}

func TestMatchRequestsQualified(t *testing.T) {
	col := matchCol()
	if got := MatchRequests(col, "manager / /drinks"); len(got) != 2 || got[0] != 2 {
		t.Errorf("folder-qualified: %v", got)
	}
	if got := MatchRequests(col, "POST manager / /drinks"); len(got) != 1 || got[0] != 3 {
		t.Errorf("method+folder should be unique: %v", got)
	}
	if got := MatchRequests(col, "post /drinks"); len(got) != 2 {
		t.Errorf("lower-case method prefix: %v", got)
	}
}

func TestMatchRequestsByFolder(t *testing.T) {
	col := matchCol()
	if got := MatchRequests(col, "manager"); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("bare folder should match its requests: %v", got)
	}
}

func TestMatchRequestsFolderNamedLikeMethod(t *testing.T) {
	col := matchCol()
	// The folder is named "post deploy"; it must win over reading "post" as
	// a method filter.
	if got := MatchRequests(col, "post deploy"); len(got) != 2 || got[0] != 5 {
		t.Errorf("folder starting with a method word: %v", got)
	}
	// The method-prefix reading still applies when nothing matches literally.
	if got := MatchRequests(col, "POST post deploy / run"); len(got) != 1 || got[0] != 5 {
		t.Errorf("method prefix on a method-named folder: %v", got)
	}
}

func TestMatchRequestsNoHit(t *testing.T) {
	col := matchCol()
	if got := MatchRequests(col, "nope"); got != nil {
		t.Errorf("expected no matches: %v", got)
	}
	// A method-looking prefix with no space is just a name.
	if got := MatchRequests(col, "get"); got != nil {
		t.Errorf("bare method is a name, not a filter: %v", got)
	}
}

func TestMatchRequestsByQueryMethod(t *testing.T) {
	col := &model.Collection{
		Name: "c",
		Requests: []model.Request{
			{Name: "search", Method: "GET"},
			{Name: "search", Method: "QUERY"},
			{Name: "query users", Method: "POST"},
		},
	}
	got := MatchRequests(col, "QUERY search")
	if len(got) != 1 || col.Requests[got[0]].Method != "QUERY" {
		t.Fatalf("a QUERY method prefix should disambiguate, got %v", got)
	}

	// The whole selector still wins, so a request genuinely named "query
	// users" is not read as a QUERY filter for "users".
	got = MatchRequests(col, "query users")
	if len(got) != 1 || col.Requests[got[0]].Name != "query users" {
		t.Errorf("the whole-selector match should win, got %v", got)
	}
}

func TestUniqueRequestNameKeepsAFreeName(t *testing.T) {
	col := &model.Collection{Requests: []model.Request{{Name: "listTodos"}}}
	if got := UniqueRequestName(col, "createTodo"); got != "createTodo" {
		t.Errorf("a name nothing answers to must be kept as-is, got %q", got)
	}
}

func TestUniqueRequestNameWalksTheCopyLadder(t *testing.T) {
	col := &model.Collection{Requests: []model.Request{{Name: "listTodos"}}}

	want := []string{"listTodos (copy)", "listTodos (copy 2)", "listTodos (copy 3)"}
	for _, w := range want {
		got := UniqueRequestName(col, "listTodos")
		if got != w {
			t.Fatalf("next free copy name: got %q, want %q", got, w)
		}
		col.Requests = append(col.Requests, model.Request{Name: got})
	}
}

// TestUniqueRequestNameDoesNotStackCopySuffixes pins the reason the suffix is
// stripped before laddering: copying a copy is a normal thing to do, and
// "x (copy) (copy)" is not a name anyone wants.
func TestUniqueRequestNameDoesNotStackCopySuffixes(t *testing.T) {
	col := &model.Collection{Requests: []model.Request{
		{Name: "listTodos"},
		{Name: "listTodos (copy)"},
	}}
	if got := UniqueRequestName(col, "listTodos (copy)"); got != "listTodos (copy 2)" {
		t.Errorf("copying a copy must ladder from the base, got %q", got)
	}
}

// TestUniqueRequestNameTreatsSlugAlikeNamesAsTaken is why this compares with
// MatchRequests rather than with ==: --request resolves by slug, so a name
// that merely slugs alike is just as ambiguous as an identical one.
func TestUniqueRequestNameTreatsSlugAlikeNamesAsTaken(t *testing.T) {
	col := &model.Collection{Requests: []model.Request{{Name: "List Todos"}}}
	if got := UniqueRequestName(col, "list-todos"); got != "list-todos (copy)" {
		t.Errorf("a slug-alike name must count as taken, got %q", got)
	}
}

// TestUniqueRequestNameAvoidsAFolderName guards the other half of the same
// rule: a bare folder name is a valid --request selector, so a request may not
// take one.
func TestUniqueRequestNameAvoidsAFolderName(t *testing.T) {
	col := matchCol()
	if got := UniqueRequestName(col, "manager"); got != "manager (copy)" {
		t.Errorf("a name that collides with a folder must be laddered, got %q", got)
	}
}
