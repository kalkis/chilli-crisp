package model

import (
	"slices"
	"testing"
)

func TestRequestClone(t *testing.T) {
	orig := Request{
		Name:    "r",
		Method:  "POST",
		URL:     "http://x",
		Headers: []Param{{Name: "H", Value: "1"}},
		Query:   []Param{{Name: "q", Value: "1"}},
		Auth:    &Auth{Type: AuthBearer, Token: "t1"},
		Body:    &Body{Type: BodyForm, Content: "c1", Form: []Param{{Name: "f", Value: "1"}}},
	}
	clone := orig.Clone()

	orig.Headers[0].Value = "2"
	orig.Query[0].Value = "2"
	orig.Auth.Token = "t2"
	orig.Body.Content = "c2"
	orig.Body.Form[0].Value = "2"

	if clone.Headers[0].Value != "1" || clone.Query[0].Value != "1" {
		t.Errorf("clone shares param backing arrays: %+v", clone)
	}
	if clone.Auth.Token != "t1" {
		t.Errorf("clone shares Auth pointer: %+v", clone.Auth)
	}
	if clone.Body.Content != "c1" || clone.Body.Form[0].Value != "1" {
		t.Errorf("clone shares Body: %+v", clone.Body)
	}
}

func TestEnsureIDs(t *testing.T) {
	c := &Collection{
		Name:     "c",
		Requests: []Request{{Name: "a"}, {Name: "b"}},
	}
	c.EnsureIDs()
	if c.ID == 0 || c.Requests[0].ID == 0 || c.Requests[1].ID == 0 {
		t.Fatalf("every ID should be assigned: %+v", c)
	}
	if c.Requests[0].ID == c.Requests[1].ID {
		t.Error("request IDs must be unique")
	}

	// Idempotent: a second call keeps existing IDs and fills only new ones.
	colID, firstID := c.ID, c.Requests[0].ID
	c.Requests = append(c.Requests, Request{Name: "c"})
	c.EnsureIDs()
	if c.ID != colID || c.Requests[0].ID != firstID {
		t.Error("EnsureIDs must not renumber existing entries")
	}
	if c.Requests[2].ID == 0 {
		t.Error("the appended request should have been given an ID")
	}
}

func TestFindRequestAndAppend(t *testing.T) {
	c := &Collection{Name: "c"}
	c.EnsureIDs()
	id := c.Append(Request{Name: "added", Method: "GET"})

	i := c.FindRequest(id)
	if i != 0 || c.Requests[i].Name != "added" {
		t.Fatalf("FindRequest(%d) = %d", id, i)
	}
	if got := c.FindRequest(id + 999); got != -1 {
		t.Errorf("unknown ID should miss, got %d", got)
	}
	if got := c.FindRequest(0); got != -1 {
		t.Errorf("the zero ID selects nothing, got %d", got)
	}
}

func TestCloneKeepsID(t *testing.T) {
	r := Request{Name: "r", ID: NewID()}
	if r.Clone().ID != r.ID {
		t.Error("a clone stands for the same logical request")
	}
}

func TestEnvironmentAllVars(t *testing.T) {
	e := Environment{
		Name:      "staging",
		Vars:      map[string]string{"baseUrl": "https://x", "token": "placeholder"},
		LocalVars: map[string]string{"token": "secret"},
	}
	all := e.AllVars()
	if all["token"] != "secret" {
		t.Errorf("local should win, got %q", all["token"])
	}
	if all["baseUrl"] != "https://x" {
		t.Errorf("shared-only var lost, got %q", all["baseUrl"])
	}
	all["token"] = "mutated"
	if e.Vars["token"] != "placeholder" || e.LocalVars["token"] != "secret" {
		t.Error("AllVars must return a copy, not the underlying maps")
	}

	empty := Environment{Name: "bare"}
	if got := empty.AllVars(); len(got) != 0 {
		t.Errorf("nil maps should merge to empty, got %v", got)
	}
	localOnly := Environment{LocalVars: map[string]string{"k": "v"}}
	if got := localOnly.AllVars(); got["k"] != "v" {
		t.Errorf("nil shared vars with locals, got %v", got)
	}
}

func TestRequestCloneNilFields(t *testing.T) {
	clone := (Request{Name: "bare"}).Clone()
	if clone.Auth != nil || clone.Body != nil || clone.Headers != nil {
		t.Errorf("nil fields should stay nil: %+v", clone)
	}
}

func TestCloneDeepCopiesVars(t *testing.T) {
	r := Request{Name: "getTodo", URL: "{{baseUrl}}/todos/{{id}}", Vars: map[string]string{"id": "1"}}
	c := r.Clone()
	c.Vars["id"] = "99"
	if r.Vars["id"] != "1" {
		t.Errorf("the clone shares its vars map with the original: %v", r.Vars)
	}
	var bare Request
	if bare.Clone().Vars != nil {
		t.Error("cloning a request without vars should not invent a map")
	}
}

func TestFolderVarsAndEnsureFolder(t *testing.T) {
	c := &Collection{Name: "api"}
	if got := c.FolderVars("admin"); got != nil {
		t.Errorf("an unknown folder has no vars, got %v", got)
	}
	if got := c.FolderVars(""); got != nil {
		t.Errorf("the empty folder name addresses nothing, got %v", got)
	}
	if c.EnsureFolder("") != nil {
		t.Error("the empty folder name must not create an entry")
	}

	c.EnsureFolder("admin").Vars = map[string]string{"id": "7"}
	c.EnsureFolder("admin").Vars["extra"] = "x"
	if len(c.Folders) != 1 {
		t.Fatalf("EnsureFolder should be idempotent, got %+v", c.Folders)
	}
	if got := c.FolderVars("admin"); got["id"] != "7" || got["extra"] != "x" {
		t.Errorf("folder vars = %v", got)
	}
}

func TestInsertAtPlacesRequestAndAssignsID(t *testing.T) {
	c := &Collection{Requests: []Request{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}}
	c.EnsureIDs()

	id := c.InsertAt(1, Request{Name: "new"})

	got := []string{}
	for _, r := range c.Requests {
		got = append(got, r.Name)
	}
	want := []string{"a", "new", "b", "c"}
	if !slices.Equal(got, want) {
		t.Fatalf("InsertAt(1) put the request in the wrong place: got %v, want %v", got, want)
	}
	if id == 0 {
		t.Error("an inserted request must get an ID")
	}
	if c.Requests[1].ID != id {
		t.Errorf("InsertAt returned %d but stored %d", id, c.Requests[1].ID)
	}
	if c.FindRequest(id) != 1 {
		t.Errorf("the inserted request must be findable by its ID")
	}
}

// TestInsertAtCarriesTheOriginalID is the invariant a duplicate depends on: a
// clone keeps its source's ID, so InsertAt has to overwrite it or two rows
// would answer to one ID.
func TestInsertAtCarriesTheOriginalID(t *testing.T) {
	c := &Collection{Requests: []Request{{Name: "a"}}}
	c.EnsureIDs()
	src := c.Requests[0]

	id := c.InsertAt(1, src.Clone())

	if id == src.ID {
		t.Fatalf("an inserted clone must get a fresh ID, kept %d", id)
	}
	if c.FindRequest(src.ID) != 0 {
		t.Errorf("the original must still resolve to its own row")
	}
}

func TestInsertAtClampsOutOfRange(t *testing.T) {
	c := &Collection{Requests: []Request{{Name: "a"}}}
	c.EnsureIDs()

	c.InsertAt(-5, Request{Name: "first"})
	c.InsertAt(99, Request{Name: "last"})

	if c.Requests[0].Name != "first" {
		t.Errorf("a negative index must clamp to the front, got %q", c.Requests[0].Name)
	}
	if c.Requests[len(c.Requests)-1].Name != "last" {
		t.Errorf("an index past the end must clamp to the back, got %q", c.Requests[len(c.Requests)-1].Name)
	}
}

func TestPruneFoldersDropsUnusedEntries(t *testing.T) {
	c := &Collection{
		Folders: []Folder{
			{Name: "todos", Vars: map[string]string{"base": "http://x"}},
			{Name: "admin", Vars: map[string]string{"key": "secret"}},
		},
		Requests: []Request{{Name: "listTodos", Folder: "todos"}},
	}

	c.PruneFolders()

	if len(c.Folders) != 1 || c.Folders[0].Name != "todos" {
		t.Fatalf("only a folder its requests still name survives, got %+v", c.Folders)
	}
	if c.Folders[0].Vars["base"] != "http://x" {
		t.Error("a surviving folder keeps its variables")
	}
}

// TestPruneFoldersNilsAnEmptyList keeps the file clean: Folders is omitempty,
// and a non-nil empty slice still marshals as "folders: []".
func TestPruneFoldersNilsAnEmptyList(t *testing.T) {
	c := &Collection{
		Folders:  []Folder{{Name: "gone", Vars: map[string]string{"a": "b"}}},
		Requests: []Request{{Name: "loose"}},
	}

	c.PruneFolders()

	if c.Folders != nil {
		t.Errorf("an emptied folder list must be nil, got %+v", c.Folders)
	}
}

func TestPruneFoldersLeavesAUsedFolderAlone(t *testing.T) {
	c := &Collection{
		Folders:  []Folder{{Name: "todos", Vars: map[string]string{"a": "b"}}},
		Requests: []Request{{Name: "listTodos", Folder: "todos"}},
	}

	c.PruneFolders()

	if len(c.Folders) != 1 {
		t.Errorf("a folder with requests must survive, got %+v", c.Folders)
	}
}
