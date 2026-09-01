package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/store"
)

func escKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEscape} }

// plain drops styling so an assertion can look at the text a user reads.
// Key hints are styled per token, so "y" and "delete" are no longer adjacent
// bytes in the rendered output even though they are adjacent on screen.
func plain(s string) string { return ansi.Strip(s) }

func testModel(t *testing.T) *appModel {
	t.Helper()
	// Sends in these tests append history, and the workspace picker looks for
	// a home workspace; keep both out of the user's real directories.
	t.Setenv(store.StateDirEnv, t.TempDir())
	t.Setenv(store.ConfigDirEnv, t.TempDir())
	w, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newAppModel(w)
}

// key builds a printable key press; Text is what the text inputs insert.
func press(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func enterKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

// typeKeys feeds a string to the model one key press at a time.
func typeKeys(m *appModel, s string) {
	for _, r := range s {
		m.handleKey(press(string(r)))
	}
}

func TestEscCancelsInFlightSend(t *testing.T) {
	m := testModel(t)
	if got := escKey().String(); got != "esc" {
		t.Fatalf("test fixture builds the wrong key: %q", got)
	}

	calls := 0
	m.cancel = func() { calls++ }

	// Idle: esc belongs to the focused pane, not to a send that is not
	// happening.
	m.handleKey(escKey())
	if calls != 0 {
		t.Errorf("esc while idle must not cancel anything (called %d times)", calls)
	}

	m.sending = true
	m.handleKey(escKey())
	if calls != 1 {
		t.Fatalf("esc should cancel the send in flight (called %d times)", calls)
	}
	if !m.sending {
		t.Error("sending stays true until the goroutine reports back")
	}
	if m.status != "cancelling…" {
		t.Errorf("status = %q, want cancelling…", m.status)
	}

	// The cancelled send still delivers its message, which is what clears the
	// in-flight state.
	m.Update(responseMsg{ws: m.ws, err: context.Canceled, req: model.Request{Name: "aborted", Method: "GET", URL: "http://example.invalid"}})
	if m.sending {
		t.Error("responseMsg should clear the sending flag")
	}
	if m.cancel != nil {
		t.Error("cancel func should be released once the send is done")
	}
	if m.status != "send cancelled" {
		t.Errorf("status = %q, want send cancelled", m.status)
	}

	entries, err := m.ws.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("an aborted send is not a result; got %d history entries", len(entries))
	}
}

// TestFailedSendIsStillRecorded is the contrast case: the history skip must be
// specific to cancellation, not to errors in general.
func TestFailedSendIsStillRecorded(t *testing.T) {
	m := testModel(t)
	m.sending = true
	m.Update(responseMsg{
		ws:      m.ws,
		err:     errors.New("dial tcp: connection refused"),
		req:     model.Request{Name: "unreachable", Method: "GET", URL: "http://example.invalid"},
		colName: "smoke",
	})

	entries, err := m.ws.History(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("a failed send should still be logged; got %d entries", len(entries))
	}
	if entries[0].Error == "" || entries[0].Request.Name != "unreachable" {
		t.Errorf("unexpected history entry: %+v", entries[0])
	}
}

// pathVarModel builds a model over one request whose URL is a template, the
// shape the OpenAPI importer produces for a path parameter.
func pathVarModel(t *testing.T, folder string) (*appModel, *model.Collection) {
	t.Helper()
	m := testModel(t)
	col := &model.Collection{
		Name: "todo",
		Vars: map[string]string{"baseUrl": "http://localhost:3000", "id": "1"},
		Requests: []model.Request{
			{Name: "getTodo", Folder: folder, Method: "GET", URL: "{{baseUrl}}/todos/{{id}}"},
		},
	}
	col.EnsureIDs()
	m.cols = []*model.Collection{col}
	m.sidebar.rebuild(m.cols)
	m.loadRequest(col, col.Requests[0].ID)
	return m, col
}

// TestPathTabEditsTheRequestNotTheQuery is the regression test for the
// reported bug: editing the id a request's URL references must change the
// path, not append a query parameter.
func TestPathTabEditsTheRequestNotTheQuery(t *testing.T) {
	m, col := pathVarModel(t, "")
	m.focus = focusEditor
	if m.editor.tab != tabPath {
		t.Fatalf("the editor should open on the Path tab, got tab %d", m.editor.tab)
	}

	// Rows come from the URL, sorted: baseUrl, id.
	rows := pathRows(&col.Requests[0], m.editor.layers(&col.Requests[0]))
	if len(rows) != 2 || rows[1].name != "id" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if rows[1].value != "1" || rows[1].source != "col" {
		t.Errorf("id should start out inherited from the collection, got %+v", rows[1])
	}

	m.handleKey(press("j")) // cursor to id
	m.handleKey(press("v"))
	if !m.editor.Editing() {
		t.Fatal("'v' should open the value editor")
	}
	typeKeys(m, "0") // seeded with "1", so this makes "10"
	m.handleKey(enterKey())

	r := &col.Requests[0]
	if got := r.Vars["id"]; got != "10" {
		t.Fatalf("request vars = %v, want id=10", r.Vars)
	}
	if len(r.Query) != 0 {
		t.Errorf("editing a path variable must not touch the query: %+v", r.Query)
	}
	if col.Vars["id"] != "1" {
		t.Errorf("the collection default must be left alone, got %q", col.Vars["id"])
	}

	resolved, err := httpclient.Resolve(r, col, m.resolverFor(r, col))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.URL != "http://localhost:3000/todos/10" {
		t.Errorf("resolved URL = %q, want the overridden path", resolved.URL)
	}

	// 'r' hands the name back to the collection, and drops the map entirely
	// so the request serializes without a vars: key.
	m.handleKey(press("r"))
	if r.Vars != nil {
		t.Errorf("reset should clear the override, got %v", r.Vars)
	}
}

func TestPathTabResetOnlyAppliesToAnOverride(t *testing.T) {
	m, col := pathVarModel(t, "")
	m.focus = focusEditor
	m.handleKey(press("j"))
	m.handleKey(press("r")) // id is inherited, not overridden
	if col.Vars["id"] != "1" {
		t.Errorf("reset must never reach a wider layer, got %q", col.Vars["id"])
	}
}

// TestPathTabBlankValueResetsRatherThanOverriding guards against sending an
// empty path segment when the user clears a field.
func TestPathTabBlankValueResetsRatherThanOverriding(t *testing.T) {
	m, col := pathVarModel(t, "")
	m.focus = focusEditor
	r := &col.Requests[0]
	r.Vars = map[string]string{"id": "10"}

	m.handleKey(press("j"))
	m.handleKey(press("v"))
	m.editor.path.input.SetValue("")
	m.handleKey(enterKey())

	if r.Vars != nil {
		t.Errorf("blanking an inherited name should reset it, got %v", r.Vars)
	}
}

// TestPathRowsFollowTheURL covers the derive-don't-cache rule: retitling the
// URL must retitle the table.
func TestPathRowsFollowTheURL(t *testing.T) {
	m, col := pathVarModel(t, "")
	r := &col.Requests[0]
	r.URL = "{{baseUrl}}/todos"
	rows := pathRows(r, m.editor.layers(r))
	if len(rows) != 1 || rows[0].name != "baseUrl" {
		t.Fatalf("rows should follow the URL, got %+v", rows)
	}
}

func TestEnterOnCollectionEditsItsVars(t *testing.T) {
	m, col := pathVarModel(t, "")
	m.focus = focusSidebar
	m.sidebar.cursor = 0 // the collection header

	m.handleKey(enterKey())
	if m.mode != modeVars || m.varsEd.folder != "" {
		t.Fatalf("enter on a collection row should open its variables, mode=%v folder=%q", m.mode, m.varsEd.folder)
	}

	m.handleKey(press("a"))
	typeKeys(m, "token")
	m.handleKey(enterKey()) // name committed, cursor moves to the value
	typeKeys(m, "abc")
	m.handleKey(enterKey())

	if col.Vars["token"] != "abc" {
		t.Fatalf("collection vars = %v, want token=abc", col.Vars)
	}
	if col.Vars["id"] != "1" {
		t.Errorf("existing vars must survive the rebuild, got %v", col.Vars)
	}

	m.handleKey(escKey())
	if m.mode != modeNormal {
		t.Errorf("esc should close the modal, mode=%v", m.mode)
	}
}

func TestEnterOnFolderEditsFolderVars(t *testing.T) {
	m, col := pathVarModel(t, "admin")
	m.focus = focusSidebar
	m.sidebar.cursor = 1 // collection header, then the folder header

	m.handleKey(enterKey())
	if m.mode != modeVars || m.varsEd.folder != "admin" {
		t.Fatalf("enter on a folder row should open folder variables, folder=%q", m.varsEd.folder)
	}

	m.handleKey(press("a"))
	typeKeys(m, "id")
	m.handleKey(enterKey())
	typeKeys(m, "7")
	m.handleKey(enterKey())

	if got := col.FolderVars("admin"); got["id"] != "7" {
		t.Fatalf("folder vars = %v, want id=7", got)
	}

	// The folder layer beats the collection default.
	r := &col.Requests[0]
	rows := pathRows(r, m.editor.layers(r))
	if rows[1].value != "7" || rows[1].source != "folder" {
		t.Errorf("id should now resolve from the folder, got %+v", rows[1])
	}

	// Deleting the last variable takes the folder entry with it: the folder
	// itself is implied by its requests, so an empty entry is only noise.
	m.handleKey(press("d"))
	if len(col.Folders) != 0 {
		t.Errorf("an empty folder entry should not be kept, got %+v", col.Folders)
	}
}

// TestVarsEditorIgnoresAnUnnamedRow: pressing 'a' creates a blank row that
// must not become a "" variable in the file.
func TestVarsEditorIgnoresAnUnnamedRow(t *testing.T) {
	m, col := pathVarModel(t, "")
	m.focus = focusSidebar
	m.sidebar.cursor = 0
	m.handleKey(enterKey())
	m.handleKey(press("a"))

	if _, ok := col.Vars[""]; ok {
		t.Errorf("a blank row must not create an empty variable: %v", col.Vars)
	}
	if len(col.Vars) != 2 {
		t.Errorf("collection vars = %v, want the original two", col.Vars)
	}
}

// methodModel builds a model over one request with the given method.
func methodModel(t *testing.T, method string) (*appModel, *model.Collection) {
	t.Helper()
	m := testModel(t)
	col := &model.Collection{
		Name:     "api",
		Requests: []model.Request{{Name: "search", Method: method, URL: "http://example.test/search"}},
	}
	col.EnsureIDs()
	m.cols = []*model.Collection{col}
	m.sidebar.rebuild(m.cols)
	m.loadRequest(col, col.Requests[0].ID)
	m.focus = focusEditor
	return m, col
}

func TestMethodPickerOpensOnTheCurrentMethod(t *testing.T) {
	m, _ := methodModel(t, "POST")
	m.handleKey(press("M"))
	if m.mode != modeMethod {
		t.Fatalf("'M' should open the method picker, mode=%v", m.mode)
	}
	if got := methods[m.methodPick.list.cursor]; got != "POST" {
		t.Errorf("cursor starts on %q, want POST", got)
	}
	m.handleKey(enterKey())
	if m.mode != modeNormal {
		t.Errorf("enter should close the picker, mode=%v", m.mode)
	}
}

func TestMethodPickerSelectsAndPersists(t *testing.T) {
	m, col := methodModel(t, "POST")
	m.handleKey(press("M"))
	m.handleKey(press("j"))
	m.handleKey(enterKey())

	if col.Requests[0].Method != "PUT" {
		t.Fatalf("method = %q, want PUT", col.Requests[0].Method)
	}
	saved, err := m.ws.Collection("api")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Requests[0].Method != "PUT" {
		t.Errorf("the change should be on disk, got %q", saved.Requests[0].Method)
	}
}

func TestMethodPickerDigitSelectsOutright(t *testing.T) {
	m, col := methodModel(t, "GET")
	m.handleKey(press("M"))
	m.handleKey(press("5"))
	if col.Requests[0].Method != "DELETE" {
		t.Errorf("method = %q, want DELETE", col.Requests[0].Method)
	}
	if m.mode != modeNormal {
		t.Errorf("a digit should also close the picker, mode=%v", m.mode)
	}
}

func TestMethodPickerNeedsARequest(t *testing.T) {
	m := testModel(t)
	m.handleKey(press("M"))
	if m.mode != modeNormal {
		t.Errorf("the picker should not open with nothing loaded, mode=%v", m.mode)
	}
	if m.status != "no request selected" {
		t.Errorf("status = %q", m.status)
	}
}

// TestQuerySeedsABody covers RFC 10008's requirement that a QUERY carry
// content with a Content-Type: choosing QUERY gives it somewhere to put one.
func TestQuerySeedsABody(t *testing.T) {
	m, col := methodModel(t, "GET")
	m.handleKey(press("M"))
	m.handleKey(press("8"))

	r := &col.Requests[0]
	if r.Method != httpclient.MethodQuery {
		t.Fatalf("method = %q, want QUERY", r.Method)
	}
	if r.Body == nil || r.Body.Type != model.BodyJSON {
		t.Fatalf("QUERY should be given a body to carry its query, got %+v", r.Body)
	}

	resolved, err := httpclient.Resolve(r, col, m.resolverFor(r, col))
	if err != nil {
		t.Fatal(err)
	}
	var contentType string
	for _, h := range resolved.Headers {
		if h.Name == "Content-Type" {
			contentType = h.Value
		}
	}
	if contentType != "application/json" {
		t.Errorf("a QUERY must carry a Content-Type, got %q", contentType)
	}
}

func TestQueryKeepsAnExistingBody(t *testing.T) {
	m, col := methodModel(t, "POST")
	col.Requests[0].Body = &model.Body{Type: model.BodyText, Content: "already here"}
	m.handleKey(press("M"))
	m.handleKey(press("8"))

	if body := col.Requests[0].Body; body.Type != model.BodyText || body.Content != "already here" {
		t.Errorf("an existing body must be left alone, got %+v", body)
	}
}

func TestNewRequestAsksForTheMethod(t *testing.T) {
	m, col := methodModel(t, "GET")
	m.focus = focusSidebar
	m.handleKey(press("n"))
	typeKeys(m, "createTodo")
	m.handleKey(enterKey())

	if m.mode != modeMethod {
		t.Fatalf("the name prompt should hand over to the method picker, mode=%v", m.mode)
	}
	if len(col.Requests) != 1 {
		t.Fatalf("nothing may be created before the method is chosen, got %d requests", len(col.Requests))
	}

	m.handleKey(press("2"))
	if len(col.Requests) != 2 {
		t.Fatalf("expected the request to be created, got %d", len(col.Requests))
	}
	created := col.Requests[1]
	if created.Name != "createTodo" || created.Method != "POST" {
		t.Errorf("created %+v, want createTodo/POST", created)
	}
	if m.active.req != created.ID || m.focus != focusEditor {
		t.Error("the new request should be loaded in the focused editor")
	}
}

func TestNewRequestAbandonedAtTheMethodStep(t *testing.T) {
	m, col := methodModel(t, "GET")
	m.focus = focusSidebar
	m.handleKey(press("n"))
	typeKeys(m, "throwaway")
	m.handleKey(enterKey())
	m.handleKey(escKey())

	if m.mode != modeNormal {
		t.Errorf("esc should close the picker, mode=%v", m.mode)
	}
	if len(col.Requests) != 1 {
		t.Errorf("esc at the method step must create nothing, got %d requests", len(col.Requests))
	}
}

// TestMethodCycleStillWorks guards the key that already worked before the
// picker was added.
func TestMethodCycleStillWorks(t *testing.T) {
	m, col := methodModel(t, "GET")
	m.handleKey(press("m"))
	if col.Requests[0].Method != "POST" {
		t.Errorf("'m' should still cycle GET->POST in one press, got %q", col.Requests[0].Method)
	}
}

func TestShortMethodFitsTheSidebarColumn(t *testing.T) {
	for _, method := range methods {
		if got := shortMethod(method); len(got) > 4 {
			t.Errorf("shortMethod(%q) = %q, too wide for the column", method, got)
		}
	}
	if got := shortMethod(httpclient.MethodQuery); got != "QRY" {
		t.Errorf("shortMethod(QUERY) = %q, want QRY", got)
	}
}

func TestHistoryOpensOnUppercaseH(t *testing.T) {
	m, _ := methodModel(t, "GET")
	m.handleKey(press("H"))
	if m.mode != modeHistory {
		t.Fatalf("'H' should open history, mode=%v", m.mode)
	}
	m.handleKey(escKey())
	if m.mode != modeNormal {
		t.Errorf("esc should close it, mode=%v", m.mode)
	}
}

// TestLowercaseHFallsThroughToThePane guards the invariant the rebinding
// bought: no global key shadows a pane key, so 'h' reaches the focused pane
// (where the viewport binds it) instead of being swallowed.
func TestLowercaseHFallsThroughToThePane(t *testing.T) {
	m, _ := methodModel(t, "GET")
	for _, focus := range []focusArea{focusSidebar, focusEditor, focusResponse} {
		m.focus = focus
		m.handleKey(press("h"))
		if m.mode != modeNormal {
			t.Errorf("'h' opened a modal with focus %v (mode=%v)", focus, m.mode)
		}
	}
}

// deleteModel builds a model whose collection holds two requests, the second
// of which sits in a folder.
func deleteModel(t *testing.T) (*appModel, *model.Collection) {
	t.Helper()
	m := testModel(t)
	col := &model.Collection{
		Name: "api",
		Requests: []model.Request{
			{Name: "health", Method: "GET", URL: "http://example.test/health"},
			{Name: "deleteTodo", Folder: "admin", Method: "DELETE", URL: "http://example.test/todos/1"},
		},
	}
	col.EnsureIDs()
	m.cols = []*model.Collection{col}
	m.sidebar.rebuild(m.cols)
	m.loadRequest(col, col.Requests[0].ID)
	m.focus = focusSidebar
	// Rows: collection header, health, folder header, deleteTodo.
	m.sidebar.cursor = 1
	return m, col
}

func TestDeleteAsksFirst(t *testing.T) {
	m, col := deleteModel(t)
	m.handleKey(press("D"))
	if m.mode != modeConfirm {
		t.Fatalf("'D' should open the confirmation, mode=%v", m.mode)
	}
	if len(col.Requests) != 2 {
		t.Fatalf("nothing may be deleted before the answer, got %d requests", len(col.Requests))
	}
	if len(m.confirm.targets) != 1 || m.confirm.targets[0].req != col.Requests[0].ID {
		t.Errorf("the confirmation targets the wrong request: %+v", m.confirm.targets)
	}
}

// TestDeleteConfirmIgnoresEnter is the guarantee the whole change exists for:
// the key that accepts every other modal must not accept this one.
func TestDeleteConfirmIgnoresEnter(t *testing.T) {
	m, col := deleteModel(t)
	m.handleKey(press("D"))
	for _, k := range []tea.KeyPressMsg{enterKey(), press(" "), press("x"), press("Y")} {
		m.handleKey(k)
		if m.mode != modeConfirm {
			t.Fatalf("%q should leave the modal open, mode=%v", k.String(), m.mode)
		}
	}
	if len(col.Requests) != 2 {
		t.Errorf("no stray key may delete, got %d requests", len(col.Requests))
	}
}

func TestDeleteConfirmed(t *testing.T) {
	m, col := deleteModel(t)
	m.handleKey(press("D"))
	m.handleKey(press("y"))

	if m.mode != modeNormal {
		t.Fatalf("'y' should close the modal, mode=%v", m.mode)
	}
	if len(col.Requests) != 1 || col.Requests[0].Name != "deleteTodo" {
		t.Fatalf("expected health to be gone, got %+v", col.Requests)
	}
	saved, err := m.ws.Collection("api")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Requests) != 1 {
		t.Errorf("the delete should be on disk, got %d requests", len(saved.Requests))
	}
	// health was the loaded request, so the editor must let go of it.
	if m.active != (sel{}) {
		t.Errorf("the deleted request should not stay selected, got %+v", m.active)
	}
}

func TestDeleteCancelled(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{press("n"), escKey()} {
		m, col := deleteModel(t)
		m.handleKey(press("D"))
		m.handleKey(k)
		if m.mode != modeNormal {
			t.Errorf("%q should close the modal, mode=%v", k.String(), m.mode)
		}
		if len(col.Requests) != 2 {
			t.Errorf("%q must not delete, got %d requests", k.String(), len(col.Requests))
		}
	}
}

// TestDeleteOnAHeaderRowTargetsTheContainer: 'D' on a header asks about the
// folder or collection under the cursor, and still writes nothing until the
// question is answered.
func TestDeleteOnAHeaderRowTargetsTheContainer(t *testing.T) {
	m, col := deleteModel(t)
	for _, tc := range []struct {
		cursor int
		want   string
	}{
		{0, "Delete collection?"}, // collection header
		{2, "Delete folder?"},     // folder header
	} {
		m.mode = modeNormal
		m.sidebar.cursor = tc.cursor
		m.handleKey(press("D"))
		if m.mode != modeConfirm {
			t.Fatalf("row %d: 'D' should open a confirmation, mode=%v", tc.cursor, m.mode)
		}
		if m.confirm.title != tc.want {
			t.Errorf("row %d: title = %q, want %q", tc.cursor, m.confirm.title, tc.want)
		}
	}
	if len(col.Requests) != 2 {
		t.Errorf("requests = %d, want both left alone until answered", len(col.Requests))
	}
}

// TestDeleteConfirmNamesTheRequest guards the detail lines: with two requests
// that could be confused, the box has to say which one and where it lives.
func TestDeleteConfirmNamesTheRequest(t *testing.T) {
	m, _ := deleteModel(t)
	m.sidebar.cursor = 3 // the folder's request
	m.handleKey(press("D"))
	m.width, m.height = 80, 24

	view := plain(m.confirm.view(m.width, m.height))
	for _, want := range []string{"DELETE", "admin / deleteTodo", "api", "y delete"} {
		if !strings.Contains(view, want) {
			t.Errorf("the confirmation should mention %q:\n%s", want, view)
		}
	}
}

// clipModel builds a model whose collection holds a loose request and two
// folders, so a paste has somewhere to land both inside and outside a run.
//
// Rows: 0 "api", 1 health, 2 "todos", 3 listTodos, 4 createTodo, 5 "admin",
// 6 purge.
func clipModel(t *testing.T) (*appModel, *model.Collection) {
	t.Helper()
	m := testModel(t)
	col := &model.Collection{
		Name: "api",
		Requests: []model.Request{
			{Name: "health", Method: "GET", URL: "http://example.test/health"},
			{Name: "listTodos", Folder: "todos", Method: "GET", URL: "http://example.test/todos",
				Headers: []model.Param{{Name: "Accept", Value: "application/json"}}},
			{Name: "createTodo", Folder: "todos", Method: "POST", URL: "http://example.test/todos"},
			{Name: "purge", Folder: "admin", Method: "DELETE", URL: "http://example.test/todos"},
		},
	}
	col.EnsureIDs()
	if err := m.ws.SaveCollection(col); err != nil {
		t.Fatal(err)
	}
	m.cols = []*model.Collection{col}
	m.sidebar.rebuild(m.cols)
	m.focus = focusSidebar
	m.sidebar.cursor = 3 // listTodos
	return m, col
}

// names lists a collection's request names in file order.
func names(col *model.Collection) []string {
	out := make([]string, 0, len(col.Requests))
	for i := range col.Requests {
		out = append(out, col.Requests[i].Name)
	}
	return out
}

// folderHeaders counts the sidebar rows opening a given folder.
func folderHeaders(s *sidebar, folder string) int {
	n := 0
	for _, it := range s.items {
		if it.reqID == 0 && it.folder == folder {
			n++
		}
	}
	return n
}

func TestCopyPasteDuplicatesIntoTheSameFolder(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("c"))
	m.handleKey(press("p"))

	got := names(col)
	want := []string{"health", "listTodos", "listTodos (copy)", "createTodo", "purge"}
	if !slices.Equal(got, want) {
		t.Fatalf("the copy must land directly after its source: got %v, want %v", got, want)
	}
	if f := col.Requests[2].Folder; f != "todos" {
		t.Errorf("the copy must stay in its source's folder, got %q", f)
	}
	if col.Requests[2].ID == col.Requests[1].ID || col.Requests[2].ID == 0 {
		t.Errorf("the copy needs an ID of its own, got %d", col.Requests[2].ID)
	}

	// And it is on disk, not just in memory.
	reloaded, err := m.ws.Collection("api")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(reloaded); !slices.Equal(got, want) {
		t.Errorf("the paste must be saved: got %v, want %v", got, want)
	}
}

func TestCopyReportsTheCount(t *testing.T) {
	m, _ := clipModel(t)

	m.handleKey(press("c"))
	if m.status != "copied 1 request" {
		t.Errorf("copying one request should say so, got %q", m.status)
	}

	m.handleKey(press("J"))
	m.handleKey(press("c"))
	if m.status != "copied 2 requests" {
		t.Errorf("copying a range should report its size, got %q", m.status)
	}
}

func TestCopyPasteHandlesARange(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("J")) // listTodos + createTodo
	m.handleKey(press("c"))
	m.handleKey(press("p"))

	got := names(col)
	want := []string{"health", "listTodos", "createTodo", "listTodos (copy)", "createTodo (copy)", "purge"}
	if !slices.Equal(got, want) {
		t.Fatalf("a range pastes in order after the cursor: got %v, want %v", got, want)
	}
	if m.status != "pasted 2 requests" {
		t.Errorf("paste should report its size, got %q", m.status)
	}
}

// TestPastedCopyKeepsOneFolderHeader is the regression guard for the reason
// paste cannot use Collection.Append: a folder is a contiguous run, so a copy
// placed outside it would open a second run and draw the folder twice.
func TestPastedCopyKeepsOneFolderHeader(t *testing.T) {
	m, _ := clipModel(t)

	m.handleKey(press("c"))
	m.handleKey(press("p"))

	if n := folderHeaders(&m.sidebar, "todos"); n != 1 {
		t.Errorf("the folder must still be drawn once, got %d headers", n)
	}
}

func TestPasteLandsInTheCursorsFolder(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("c")) // listTodos, from "todos"
	m.sidebar.cursor = 6    // purge, in "admin"
	m.handleKey(press("p"))

	got := names(col)
	want := []string{"health", "listTodos", "createTodo", "purge", "listTodos (copy)"}
	if !slices.Equal(got, want) {
		t.Fatalf("paste follows the cursor: got %v, want %v", got, want)
	}
	if f := col.Requests[4].Folder; f != "admin" {
		t.Errorf("the copy must join the folder it was pasted into, got %q", f)
	}
}

func TestPasteOnAFolderHeaderAppendsToThatRun(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("c")) // listTodos
	m.sidebar.cursor = 5    // the "admin" folder header
	m.handleKey(press("p"))

	if got, want := names(col), []string{"health", "listTodos", "createTodo", "purge", "listTodos (copy)"}; !slices.Equal(got, want) {
		t.Fatalf("pasting on a folder header appends to its run: got %v, want %v", got, want)
	}
	if n := folderHeaders(&m.sidebar, "admin"); n != 1 {
		t.Errorf("the folder must still be drawn once, got %d headers", n)
	}
}

func TestPasteOnACollectionHeaderLandsOutsideAnyFolder(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("c")) // listTodos, from "todos"
	m.sidebar.cursor = 0    // the "api" collection header
	m.handleKey(press("p"))

	if col.Requests[0].Name != "listTodos (copy)" {
		t.Fatalf("a paste on the collection header goes to the top, got %q", col.Requests[0].Name)
	}
	if f := col.Requests[0].Folder; f != "" {
		t.Errorf("it must leave the folder behind, got %q", f)
	}
}

// TestPasteTwiceDoesNotAliasTheSource is why the clipboard clones on the way
// out as well as the way in: without it two pastes would share one Headers
// array and editing a header on one would change the other.
func TestPasteTwiceDoesNotAliasTheSource(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("c"))
	m.handleKey(press("p"))
	m.handleKey(press("p"))

	var copies []*model.Request
	for i := range col.Requests {
		if strings.HasPrefix(col.Requests[i].Name, "listTodos (copy") {
			copies = append(copies, &col.Requests[i])
		}
	}
	if len(copies) != 2 {
		t.Fatalf("expected two copies, got %d: %v", len(copies), names(col))
	}
	copies[0].Headers[0].Value = "text/plain"

	if v := copies[1].Headers[0].Value; v != "application/json" {
		t.Errorf("copies must not share a Headers array, sibling became %q", v)
	}
	src := col.Requests[col.FindRequest(col.Requests[1].ID)]
	if v := src.Headers[0].Value; v != "application/json" {
		t.Errorf("the source must not share a Headers array either, became %q", v)
	}
}

// TestClipboardSurvivesDeletingTheSource: the buffer holds a snapshot, so a
// copy is still pasteable after the request it came from is gone.
func TestClipboardSurvivesDeletingTheSource(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("c"))
	m.handleKey(press("D"))
	m.handleKey(press("y"))
	if slices.Contains(names(col), "listTodos") {
		t.Fatalf("the source should be gone, got %v", names(col))
	}

	m.handleKey(press("p"))
	if !slices.Contains(names(col), "listTodos") {
		t.Errorf("the clipboard must outlive its source, got %v", names(col))
	}
}

func TestCopyIsInertOnAHeaderRow(t *testing.T) {
	m, _ := clipModel(t)
	m.sidebar.cursor = 2 // the "todos" folder header

	m.handleKey(press("c"))

	if !m.clip.empty() {
		t.Errorf("a header row has no request to copy, buffer holds %d", m.clip.len())
	}
}

func TestPasteIsInertWithAnEmptyClipboard(t *testing.T) {
	m, col := clipModel(t)
	before := names(col)

	m.handleKey(press("p"))

	if got := names(col); !slices.Equal(got, before) {
		t.Errorf("pasting an empty buffer must change nothing: got %v, want %v", got, before)
	}
}

// TestShiftArrowExtendsLikeTheLetter: the letter form is the one guaranteed to
// arrive, but the arrow is what most users will reach for.
func TestShiftArrowExtendsLikeTheLetter(t *testing.T) {
	m, _ := clipModel(t)

	m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	m.handleKey(press("c"))

	if m.clip.len() != 2 {
		t.Errorf("shift+down must extend the range, copied %d", m.clip.len())
	}
}

func TestEscClearsTheSelection(t *testing.T) {
	m, _ := clipModel(t)
	m.handleKey(press("J"))
	if m.sidebar.anchor == 0 {
		t.Fatal("the range should be open")
	}

	m.handleKey(escKey())

	if m.sidebar.anchor != 0 {
		t.Errorf("esc must end the range, anchor=%d", m.sidebar.anchor)
	}
}

func TestMultiDeleteConfirmCountsTheRequests(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("J"))
	m.handleKey(press("D"))

	if m.mode != modeConfirm {
		t.Fatalf("'D' should open the confirmation, mode=%v", m.mode)
	}
	if len(m.confirm.targets) != 2 {
		t.Fatalf("the confirmation should target both requests, got %d", len(m.confirm.targets))
	}
	if m.confirm.title != "Delete 2 requests?" {
		t.Errorf("the title should name the count, got %q", m.confirm.title)
	}
	if len(col.Requests) != 4 {
		t.Errorf("nothing may be deleted before the answer, got %d", len(col.Requests))
	}
}

func TestMultiDeleteConfirmed(t *testing.T) {
	m, col := clipModel(t)

	m.handleKey(press("J"))
	m.handleKey(press("D"))
	m.handleKey(press("y"))

	got := names(col)
	want := []string{"health", "purge"}
	if !slices.Equal(got, want) {
		t.Fatalf("both selected requests should go: got %v, want %v", got, want)
	}
	if m.status != "deleted 2 requests" {
		t.Errorf("the status should report the count, got %q", m.status)
	}
	reloaded, err := m.ws.Collection("api")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(reloaded); !slices.Equal(got, want) {
		t.Errorf("the deletion must be saved: got %v, want %v", got, want)
	}
}

// TestMultiDeleteSpansCollections: a range may cross a collection boundary, so
// the delete has to save every file it touched.
func TestMultiDeleteSpansCollections(t *testing.T) {
	m := testModel(t)
	a := &model.Collection{Name: "alpha", Requests: []model.Request{{Name: "one", Method: "GET"}}}
	b := &model.Collection{Name: "beta", Requests: []model.Request{{Name: "two", Method: "GET"}}}
	for _, c := range []*model.Collection{a, b} {
		c.EnsureIDs()
		if err := m.ws.SaveCollection(c); err != nil {
			t.Fatal(err)
		}
	}
	m.cols = []*model.Collection{a, b}
	m.sidebar.rebuild(m.cols)
	m.focus = focusSidebar
	m.sidebar.cursor = 1 // alpha/one

	m.handleKey(press("J")) // onto beta's header
	m.handleKey(press("J")) // and on to beta/two
	m.handleKey(press("D"))
	m.handleKey(press("y"))

	for _, c := range []*model.Collection{a, b} {
		if len(c.Requests) != 0 {
			t.Errorf("%s should be empty, holds %v", c.Name, names(c))
		}
		reloaded, err := m.ws.Collection(c.Name)
		if err != nil {
			t.Fatal(err)
		}
		if len(reloaded.Requests) != 0 {
			t.Errorf("%s must be saved too, on disk holds %v", c.Name, names(reloaded))
		}
	}
}

// folderModel builds a collection whose folders carry variables, so a delete
// can be checked against the Folders: sidecar as well as the requests.
//
// Rows: 0 "api", 1 health, 2 "todos", 3 listTodos, 4 createTodo, 5 "admin",
// 6 purge, 7 audit.
func folderModel(t *testing.T) (*appModel, *model.Collection) {
	t.Helper()
	m := testModel(t)
	col := &model.Collection{
		Name: "api",
		Folders: []model.Folder{
			{Name: "todos", Vars: map[string]string{"base": "http://example.test"}},
			{Name: "admin", Vars: map[string]string{"key": "shh"}},
		},
		Requests: []model.Request{
			{Name: "health", Method: "GET", URL: "http://example.test/health"},
			{Name: "listTodos", Folder: "todos", Method: "GET", URL: "{{base}}/todos"},
			{Name: "createTodo", Folder: "todos", Method: "POST", URL: "{{base}}/todos"},
			{Name: "purge", Folder: "admin", Method: "DELETE", URL: "http://example.test/todos"},
			{Name: "audit", Folder: "admin", Method: "GET", URL: "http://example.test/audit"},
		},
	}
	col.EnsureIDs()
	if err := m.ws.SaveCollection(col); err != nil {
		t.Fatal(err)
	}
	m.cols = []*model.Collection{col}
	m.sidebar.rebuild(m.cols)
	m.focus = focusSidebar
	return m, col
}

// folderNames lists a collection's Folders: entries.
func folderNames(col *model.Collection) []string {
	out := make([]string, 0, len(col.Folders))
	for _, f := range col.Folders {
		out = append(out, f.Name)
	}
	return out
}

// TestRangeAcrossAFolderHeaderDeletesOnlyRequests is the guard for the whole
// design. A range extended across a folder boundary sweeps up the header
// between the two runs; if that header counted, 'D' would take the entire
// folder — including requests below the range that were never highlighted and
// may be off screen. A destructive action must not escalate past what the
// highlight shows.
func TestRangeAcrossAFolderHeaderDeletesOnlyRequests(t *testing.T) {
	m, col := folderModel(t)
	m.sidebar.cursor = 4 // createTodo, last of "todos"

	m.handleKey(press("J")) // onto the "admin" header
	m.handleKey(press("J")) // and on to purge
	m.handleKey(press("D"))

	if m.confirm.title != "Delete 2 requests?" {
		t.Fatalf("the range is two requests, not a folder: title = %q", m.confirm.title)
	}
	m.handleKey(press("y"))

	got := names(col)
	want := []string{"health", "listTodos", "audit"}
	if !slices.Equal(got, want) {
		t.Fatalf("only the highlighted requests may go: got %v, want %v", got, want)
	}
	if !slices.Contains(folderNames(col), "admin") {
		t.Errorf("the swept-up folder must survive, folders = %v", folderNames(col))
	}
}

func TestDeleteFolderRemovesItsRequests(t *testing.T) {
	m, col := folderModel(t)
	m.sidebar.cursor = 2 // the "todos" header

	m.handleKey(press("D"))
	m.handleKey(press("y"))

	got := names(col)
	want := []string{"health", "purge", "audit"}
	if !slices.Equal(got, want) {
		t.Fatalf("the folder's requests should go: got %v, want %v", got, want)
	}
	if m.status != "deleted folder todos (2 requests)" {
		t.Errorf("the status should name the folder and its size, got %q", m.status)
	}
	reloaded, err := m.ws.Collection("api")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(reloaded); !slices.Equal(got, want) {
		t.Errorf("the delete must be saved: got %v, want %v", got, want)
	}
}

// TestDeleteFolderDropsItsVarsEntry: a folder exists because its requests do,
// so its entry must not outlive them — it would be unreachable state in a
// committed file, and EnsureFolder would silently revive it.
func TestDeleteFolderDropsItsVarsEntry(t *testing.T) {
	m, col := folderModel(t)
	m.sidebar.cursor = 2 // "todos"

	m.handleKey(press("D"))
	m.handleKey(press("y"))

	if slices.Contains(folderNames(col), "todos") {
		t.Errorf("the folder entry must go with its requests, folders = %v", folderNames(col))
	}
	if !slices.Contains(folderNames(col), "admin") {
		t.Errorf("other folders are untouched, folders = %v", folderNames(col))
	}
	reloaded, err := m.ws.Collection("api")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(folderNames(reloaded), "todos") {
		t.Errorf("on disk too, folders = %v", folderNames(reloaded))
	}
}

// TestEmptiedFolderLosesItsVarsEntry: deleting the last request in a folder by
// hand reaches the same file as deleting the folder outright.
func TestEmptiedFolderLosesItsVarsEntry(t *testing.T) {
	m, col := folderModel(t)
	m.sidebar.cursor = 3 // listTodos

	m.handleKey(press("J")) // and createTodo
	m.handleKey(press("D"))
	m.handleKey(press("y"))

	if slices.Contains(folderNames(col), "todos") {
		t.Errorf("an emptied folder loses its entry too, folders = %v", folderNames(col))
	}
}

func TestDeleteFolderConfirmNamesTheFolder(t *testing.T) {
	m, _ := folderModel(t)
	m.sidebar.cursor = 5 // "admin"

	m.handleKey(press("D"))

	body := plain(strings.Join(m.confirm.detail, "\n"))
	for _, want := range []string{"admin", "2 requests", "api"} {
		if !strings.Contains(body, want) {
			t.Errorf("the folder confirmation should say %q, got %q", want, body)
		}
	}
	if got := plain(helpLine(60, m.confirm.accept)); !strings.Contains(got, "delete folder") {
		t.Errorf("the footer should name the verb, got %q", got)
	}
}

func TestDeleteCollectionRemovesTheFileAndTheRow(t *testing.T) {
	m, col := folderModel(t)
	path := col.Path
	m.sidebar.cursor = 0 // the "api" header

	m.handleKey(press("D"))
	if m.confirm.title != "Delete collection?" {
		t.Fatalf("title = %q", m.confirm.title)
	}
	m.handleKey(press("y"))

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file should be gone, stat err = %v", err)
	}
	if len(m.cols) != 0 {
		t.Errorf("the collection should be out of the model, got %d", len(m.cols))
	}
	if m.status != "deleted collection api" {
		t.Errorf("status = %q", m.status)
	}
}

// TestDeleteCollectionConfirmShowsThePath: this is the only action that
// removes a file, and the modal is the one place the user sees which one.
func TestDeleteCollectionConfirmShowsThePath(t *testing.T) {
	m, _ := folderModel(t)
	m.sidebar.cursor = 0

	m.handleKey(press("D"))

	body := plain(strings.Join(m.confirm.detail, "\n"))
	for _, want := range []string{"api", "5 requests, 2 folders", ".crisp/collections/api.yaml"} {
		if !strings.Contains(body, want) {
			t.Errorf("the collection confirmation should say %q, got %q", want, body)
		}
	}
}

func TestDeleteCollectionClearsTheEditor(t *testing.T) {
	m, col := folderModel(t)
	m.loadRequest(col, col.Requests[1].ID)
	m.sidebar.cursor = 0

	m.handleKey(press("D"))
	m.handleKey(press("y"))

	if m.active != (sel{}) {
		t.Errorf("the loaded request went with its collection, active = %+v", m.active)
	}
	if m.editor.request() != nil {
		t.Error("the editor must not hold a request from a deleted collection")
	}
	if got := plain(m.sidebar.view(m.cols, 30, 10, true, m.active)); !strings.Contains(got, "no collections") {
		t.Errorf("the sidebar should fall back to its empty state, got %q", got)
	}
}

// TestDeleteCollectionKeepsItWhenTheFileCannotGo: the file goes first, so a
// failed removal leaves the collection visible rather than making it vanish
// from the sidebar while still on disk.
func TestDeleteCollectionKeepsItWhenTheFileCannotGo(t *testing.T) {
	m, col := folderModel(t)
	col.Path = filepath.Join(t.TempDir(), "outside.yaml") // refused by the store
	m.sidebar.cursor = 0

	m.handleKey(press("D"))
	m.handleKey(press("y"))

	if len(m.cols) != 1 {
		t.Errorf("a refused delete must leave the collection in place, got %d", len(m.cols))
	}
	if !strings.HasPrefix(m.status, "delete failed:") {
		t.Errorf("the failure should reach the status bar, got %q", m.status)
	}
}

// otherWorkspace builds a second workspace on disk holding one collection, so
// a switch has somewhere to go and something to show once it arrives.
func otherWorkspace(t *testing.T, name string) *store.Workspace {
	t.Helper()
	w, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	col := &model.Collection{
		Name:     name,
		Requests: []model.Request{{Name: name + "-req", Method: "GET", URL: "http://x"}},
	}
	col.EnsureIDs()
	if err := w.SaveCollection(col); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestSwitchWorkspaceReplacesWhatBelongsToIt(t *testing.T) {
	m := testModel(t)
	col := &model.Collection{Name: "first", Requests: []model.Request{{Name: "one", Method: "GET"}}}
	col.EnsureIDs()
	m.cols = []*model.Collection{col}
	m.envs = []*model.Environment{{Name: "staging"}}
	m.envIdx = 0
	m.sidebar.rebuild(m.cols)
	m.sidebar.anchor = col.Requests[0].ID
	m.focus = focusResponse

	other := otherWorkspace(t, "second")
	if err := m.loadWorkspace(other); err != nil {
		t.Fatal(err)
	}

	if m.ws != other {
		t.Error("the model still points at the previous workspace")
	}
	if len(m.cols) != 1 || m.cols[0].Name != "second" {
		t.Errorf("collections were not replaced: %v", names(m.cols[0]))
	}
	// An environment index means nothing in another workspace, and neither
	// does a selection anchor holding an ID from a collection that is gone.
	if m.envIdx != -1 {
		t.Errorf("environment index survived the switch as %d", m.envIdx)
	}
	if m.sidebar.anchor != 0 {
		t.Errorf("selection anchor survived the switch as %d", m.sidebar.anchor)
	}
	if m.focus != focusSidebar {
		t.Errorf("focus is %v, want the sidebar", m.focus)
	}
	// The editor is rebuilt on a switch, so its binding back to the model has
	// to be remade or the Path tab silently loses every environment value.
	if m.editor.envVars == nil {
		t.Error("the editor lost its envVars binding")
	}
}

func TestClipboardSurvivesAWorkspaceSwitch(t *testing.T) {
	m := testModel(t)
	col := &model.Collection{Name: "first", Requests: []model.Request{{Name: "one", Method: "GET"}}}
	col.EnsureIDs()
	m.cols = []*model.Collection{col}
	m.sidebar.rebuild(m.cols)
	m.focus = focusSidebar
	m.sidebar.cursor = 1

	m.handleKey(press("c"))
	if m.clip.empty() {
		t.Fatal("'c' did not fill the clipboard")
	}

	other := otherWorkspace(t, "second")
	if err := m.loadWorkspace(other); err != nil {
		t.Fatal(err)
	}
	// The clipboard is process-local, and pasting a request into another
	// workspace is the point of that, not an oversight.
	if m.clip.empty() {
		t.Fatal("the clipboard was cleared by the switch")
	}
	m.sidebar.cursor = 1
	m.handleKey(press("p"))
	if got := len(m.cols[0].Requests); got != 2 {
		t.Errorf("the pasted request did not arrive: %v", names(m.cols[0]))
	}
}

func TestFailedSwitchLeavesTheWorkspaceAlone(t *testing.T) {
	m := testModel(t)
	col := &model.Collection{Name: "first", Requests: []model.Request{{Name: "one", Method: "GET"}}}
	col.EnsureIDs()
	m.cols = []*model.Collection{col}
	before := m.ws

	broken, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(broken.Root, "collections", "bad.yaml")
	if err := os.WriteFile(bad, []byte("name: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.loadWorkspace(broken); err == nil {
		t.Fatal("a collection that will not parse should fail the load")
	}
	// Everything fallible runs before any state changes, so a workspace that
	// cannot be read leaves the TUI where it was rather than half-moved.
	if m.ws != before {
		t.Error("a failed switch moved the workspace anyway")
	}
	if len(m.cols) != 1 || m.cols[0].Name != "first" {
		t.Error("a failed switch cleared the collections")
	}
}

func TestSwitchRereadsTheRequestTimeout(t *testing.T) {
	m := testModel(t)
	if err := m.loadWorkspace(m.ws); err != nil {
		t.Fatal(err)
	}
	if got := m.client.Timeout(); got != store.DefaultTimeout {
		t.Fatalf("client timeout is %s, want the default %s", got, store.DefaultTimeout)
	}

	other, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(other.Root, store.ConfigName)
	if err := os.WriteFile(cfg, []byte("timeout: 5s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.loadWorkspace(other); err != nil {
		t.Fatal(err)
	}
	// Each workspace configures its own timeout, and the TUI and the runner
	// are meant to agree on which one is in force.
	if got := m.client.Timeout(); got != 5*time.Second {
		t.Errorf("client timeout is %s, want the new workspace's 5s", got)
	}
}

func TestWorkspacePickerOffersTheHomeWorkspace(t *testing.T) {
	m := testModel(t)
	// Somewhere with no .crisp above it, so the list is exactly the current
	// workspace and the home one.
	t.Chdir(t.TempDir())
	home, err := store.HomeWorkspacePath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InitAt(home); err != nil {
		t.Fatal(err)
	}

	m.handleKey(press("W"))
	if m.mode != modeWorkspace {
		t.Fatalf("'W' did not open the picker, mode=%v", m.mode)
	}
	if !slices.Contains(m.wsPaths, home) {
		t.Errorf("the home workspace is missing from %v", m.wsPaths)
	}
	if !slices.Contains(m.wsPaths, m.ws.Root) {
		t.Errorf("the current workspace is missing from %v", m.wsPaths)
	}
	// The last row is the escape hatch rather than a workspace, which is why
	// it has no entry in wsPaths.
	if len(m.wsList.lines) != len(m.wsPaths)+1 {
		t.Fatalf("%d rows for %d workspaces", len(m.wsList.lines), len(m.wsPaths))
	}
	if last := m.wsList.lines[len(m.wsList.lines)-1]; last != openPathEntry {
		t.Errorf("last row is %q, want %q", last, openPathEntry)
	}
}

func TestWorkspacePickerOpensAPathByName(t *testing.T) {
	m := testModel(t)
	other := otherWorkspace(t, "second")

	m.handleKey(press("W"))
	m.wsList.cursor = len(m.wsPaths) // the "Open a path…" row
	m.handleKey(enterKey())
	if m.mode != modePrompt {
		t.Fatalf("selecting the last row should open the path prompt, mode=%v", m.mode)
	}
	m.prompt.input.SetValue(other.Root)
	m.handleKey(enterKey())

	if m.ws.Root != other.Root {
		t.Errorf("opened %s, want %s", m.ws.Root, other.Root)
	}
	if len(m.cols) != 1 || m.cols[0].Name != "second" {
		t.Error("the named workspace's collections were not loaded")
	}
}

func TestSwitchToANonWorkspaceKeepsTheCurrentOne(t *testing.T) {
	m := testModel(t)
	before := m.ws

	m.switchWorkspace(t.TempDir())
	if m.ws != before {
		t.Error("a path that is not a workspace replaced the current one")
	}
	if !strings.Contains(m.status, "workspace") {
		t.Errorf("the failure should be reported, status=%q", m.status)
	}
}

// TestHistoryOverlayNamesTheRecordingLevel covers the only place the setting
// is visible from inside the TUI. A log that never fills would otherwise look
// like a bug, and the level is per workspace, so switching has to pick up the
// new one.
func TestHistoryOverlayNamesTheRecordingLevel(t *testing.T) {
	m := testModel(t)
	if err := m.loadWorkspace(m.ws); err != nil {
		t.Fatal(err)
	}
	m.handleKey(press("H"))
	if m.mode != modeHistory {
		t.Fatalf("mode = %v, want the history overlay", m.mode)
	}
	if m.histList.title != "History" {
		t.Errorf("title = %q, want the plain one at the default level", m.histList.title)
	}

	quiet := otherWorkspace(t, "quiet")
	cfg := filepath.Join(quiet.Root, store.ConfigName)
	if err := os.WriteFile(cfg, []byte("history: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.mode = modeNormal
	if err := m.loadWorkspace(quiet); err != nil {
		t.Fatal(err)
	}
	if m.historyMode != store.HistoryOff {
		t.Errorf("historyMode = %q, want off after the switch", m.historyMode)
	}
	m.handleKey(press("H"))
	if !strings.Contains(m.histList.title, "off") {
		t.Errorf("title = %q, should say recording is off", m.histList.title)
	}
}

func TestHistoryTitle(t *testing.T) {
	cases := []struct {
		mode store.HistoryMode
		want string
	}{
		{store.HistoryFull, "History"},
		{store.HistoryMetadata, "History (response bodies are not recorded)"},
		{store.HistoryOff, "History (recording is off)"},
		// The zero value is what a model has before a workspace is loaded.
		{"", "History"},
	}
	for _, c := range cases {
		if got := historyTitle(c.mode); got != c.want {
			t.Errorf("historyTitle(%q) = %q, want %q", c.mode, got, c.want)
		}
	}
}
