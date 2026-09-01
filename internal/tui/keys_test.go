package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"

	reflowansi "github.com/muesli/reflow/ansi"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// TestEveryBindingHasHelp is the guard that makes the status bar trustworthy:
// a binding without help text renders as nothing, so it would vanish from the
// hint and the '?' reference without any other test noticing.
func TestEveryBindingHasHelp(t *testing.T) {
	v := reflect.ValueOf(defaultKeys)
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		b, ok := v.Field(i).Interface().(key.Binding)
		if !ok {
			t.Fatalf("%s is not a key.Binding", name)
		}
		if len(b.Keys()) == 0 {
			t.Errorf("%s binds no keys", name)
		}
		if b.Help().Key == "" || b.Help().Desc == "" {
			t.Errorf("%s has no help text: %+v", name, b.Help())
		}
	}
}

// helpModel builds a model whose sidebar holds a collection header, a request,
// a folder header and the folder's request, in that order.
func helpModel(t *testing.T) *appModel {
	t.Helper()
	m := testModel(t)
	col := &model.Collection{
		Name: "api",
		Requests: []model.Request{
			{Name: "listTodos", Method: "GET", URL: "http://x/todos"},
			{Name: "deleteTodo", Method: "DELETE", Folder: "admin", URL: "http://x/todos/{{id}}"},
		},
	}
	col.EnsureIDs()
	m.cols = []*model.Collection{col}
	m.sidebar.rebuild(m.cols)
	m.width, m.height = 130, 36
	return m
}

// hints renders the status bar hint for the model's current state.
func hints(m *appModel) string {
	m.refreshKeys()
	return plain(helpLine(200, m.shortHelp()...))
}

func TestSidebarHintFollowsTheCursor(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar

	m.sidebar.cursor = 0 // collection header
	got := hints(m)
	for _, want := range []string{"space fold", "enter vars"} {
		if !strings.Contains(got, want) {
			t.Errorf("header row hint should offer %q, got %q", want, got)
		}
	}
	if !strings.Contains(got, "D delete collection") {
		t.Errorf("a collection header offers to delete the collection, got %q", got)
	}

	m.sidebar.cursor = 1 // a request
	got = hints(m)
	for _, want := range []string{"enter open", "D delete"} {
		if !strings.Contains(got, want) {
			t.Errorf("request row hint should offer %q, got %q", want, got)
		}
	}
	if strings.Contains(got, "fold") {
		t.Errorf("a request row does not fold, got %q", got)
	}
}

// TestHiddenKeysAreInert is the invariant the whole change exists for: a key
// the status bar is not advertising must do nothing, in the same frame.
func TestHiddenKeysAreInert(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar

	m.sidebar.cursor = 1 // a request: space is hidden here
	before := len(m.sidebar.items)
	m.handleKey(press("space"))
	if len(m.sidebar.items) != before {
		t.Errorf("space on a request row should not fold anything")
	}
	if len(m.sidebar.collapsed) != 0 {
		t.Errorf("space on a request row recorded a fold: %v", m.sidebar.collapsed)
	}

	// enter on a header is OpenVars, not OpenRequest: the two share the
	// keystroke and exactly one is ever live, so opening a request must not
	// happen here.
	m.sidebar.cursor = 0
	m.handleKey(enterKey())
	if m.mode != modeVars {
		t.Fatalf("enter on a header row opens the vars modal, mode=%v", m.mode)
	}
	if m.active != (sel{}) {
		t.Errorf("enter on a header row must not load a request, active=%+v", m.active)
	}
}

func TestEditorHintFollowsTheTab(t *testing.T) {
	m := helpModel(t)
	m.loadRequest(m.cols[0], m.cols[0].Requests[1].ID)
	m.focus = focusEditor

	for _, tc := range []struct {
		tab  int
		want []string
	}{
		{tabPath, []string{"enter override", "u url", "m cycle method"}},
		{tabParams, []string{"enter name", "v value", "a add", "space on/off"}},
		{tabAuth, []string{"t auth type", "enter edit"}},
		{tabBody, []string{"enter edit", "E $EDITOR", "t body type"}},
	} {
		m.editor.tab = tc.tab
		got := hints(m)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("tab %s should offer %q, got %q", tabNames[tc.tab], want, got)
			}
		}
	}
}

// TestResetHintNeedsAnOverride keeps 'r' off the bar until it would do
// something — the Path tab's per-row rule.
func TestResetHintNeedsAnOverride(t *testing.T) {
	m := helpModel(t)
	r := &m.cols[0].Requests[1]
	m.loadRequest(m.cols[0], r.ID)
	m.focus = focusEditor
	m.editor.tab = tabPath

	if got := hints(m); strings.Contains(got, "r reset") {
		t.Errorf("nothing is overridden yet, got %q", got)
	}
	r.Vars = map[string]string{"id": "10"}
	if got := hints(m); !strings.Contains(got, "r reset") {
		t.Errorf("an overridden row should offer a reset, got %q", got)
	}
}

// TestPrintableGlobalKeysReachAnOpenField guards the one genuinely new rule:
// '?' and 'W' are printable characters, so while a text field is open they
// must be typed rather than intercepted.
func TestPrintableGlobalKeysReachAnOpenField(t *testing.T) {
	m := helpModel(t)
	m.loadRequest(m.cols[0], m.cols[0].Requests[0].ID)
	m.focus = focusEditor

	m.handleKey(press("?"))
	if m.mode != modeHelp {
		t.Fatalf("'?' should open the reference, mode=%v", m.mode)
	}
	m.handleKey(escKey())

	m.handleKey(press("u")) // open the URL field
	if !m.editor.editingURL {
		t.Fatal("'u' should open the URL editor")
	}
	m.handleKey(press("?"))
	if m.mode == modeHelp {
		t.Fatal("'?' should reach the URL field, not open the reference")
	}
	m.handleKey(press("W"))
	if m.mode == modeWorkspace {
		t.Fatal("'W' should reach the URL field, not open the workspace picker")
	}
	if !strings.Contains(m.editor.urlIn.Value(), "?") {
		t.Errorf("'?' should have been typed, url = %q", m.editor.urlIn.Value())
	}
}

// TestStatusBarFitsItsWidth is the regression the old fixed string would fail:
// it was 108 cells wide with no truncation at any terminal size.
func TestStatusBarFitsItsWidth(t *testing.T) {
	m := helpModel(t)
	m.loadRequest(m.cols[0], m.cols[0].Requests[0].ID)

	m.status = "created collection api"
	for _, width := range []int{40, 60, 80, 130} {
		for _, focus := range []focusArea{focusSidebar, focusEditor, focusResponse} {
			for tab := range tabCount {
				m.width, m.height, m.focus, m.editor.tab = width, 24, focus, tab
				m.refreshKeys()
				bar := m.statusBar()
				if w := reflowansi.PrintableRuneWidth(bar); w > width {
					t.Errorf("width %d focus %d tab %d: the bar is %d cells wide:\n%s",
						width, focus, tab, w, plain(bar))
				}
				if strings.Contains(bar, "\n") {
					t.Errorf("width %d focus %d tab %d: the bar wrapped", width, focus, tab)
				}
			}
		}
	}
}

// TestHelpHintSurvivesTruncation: the bar shows only what the cursor can do,
// so '?' is the route to everything else and must not be the thing that gets
// cut when the terminal is narrow.
func TestHelpHintSurvivesTruncation(t *testing.T) {
	m := helpModel(t)
	m.loadRequest(m.cols[0], m.cols[0].Requests[0].ID)
	m.focus = focusEditor
	m.editor.tab = tabParams // the widest hint

	for _, width := range []int{60, 80, 100, 130} {
		m.width, m.height = width, 24
		m.refreshKeys()
		if bar := plain(m.statusBar()); !strings.Contains(bar, "? help") {
			t.Errorf("width %d dropped the help hint:\n%s", width, bar)
		}
	}
}

// TestNoHelpHintWhileEditing is the other half of the same rule: '?' has to
// reach an open text field, so the bar must not offer it there.
func TestNoHelpHintWhileEditing(t *testing.T) {
	m := helpModel(t)
	m.loadRequest(m.cols[0], m.cols[0].Requests[0].ID)
	m.focus = focusEditor
	m.width, m.height = 130, 24
	m.handleKey(press("u"))

	m.refreshKeys() // render does this before drawing the bar
	bar := plain(m.statusBar())
	if strings.Contains(bar, "? help") {
		t.Errorf("the bar should not offer '?' while a field is open:\n%s", bar)
	}
	for _, want := range []string{"enter save", "esc cancel"} {
		if !strings.Contains(bar, want) {
			t.Errorf("an open field should offer %q:\n%s", want, bar)
		}
	}
}

// TestMethodPickerFooterTracksTheMethodList keeps the hint and the keys the
// picker accepts derived from the same place.
func TestMethodPickerFooterTracksTheMethodList(t *testing.T) {
	want := fmt.Sprintf("1-%d pick", len(methods))
	picker := newMethodPicker("GET", "")
	got := plain(picker.list.view(90, 24))
	if !strings.Contains(got, want) {
		t.Errorf("the picker footer should say %q:\n%s", want, got)
	}
	last := fmt.Sprintf("%d", len(methods))
	if !key.Matches(press(last), defaultKeys.PickMethod) {
		t.Errorf("%q should select the last method", last)
	}
	if key.Matches(press("9"), defaultKeys.PickMethod) && len(methods) < 9 {
		t.Error("the picker binds a digit past the end of the method list")
	}
}

// TestHelpOverlayListsEveryKey guards the reference against the context flags:
// it renders from defaultKeys, so a key the cursor has disabled still appears.
func TestHelpOverlayListsEveryKey(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar
	m.sidebar.cursor = 1 // a request row, so Fold and OpenVars are disabled
	m.handleKey(press("?"))
	if m.mode != modeHelp {
		t.Fatalf("'?' should open the reference, mode=%v", m.mode)
	}

	got := plain(m.render())
	for _, want := range []string{"s send", "q quit", "space fold", "enter vars", "H history", "E $EDITOR"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reference should list %q:\n%s", want, got)
		}
	}
}

func TestHelpOverlayCloses(t *testing.T) {
	m := helpModel(t)
	for _, k := range []string{"?", "q"} {
		m.handleKey(press("?"))
		if m.mode != modeHelp {
			t.Fatalf("'?' should open the reference, mode=%v", m.mode)
		}
		m.handleKey(press(k))
		if m.mode != modeNormal {
			t.Errorf("%q should close the reference, mode=%v", k, m.mode)
		}
	}
	m.handleKey(press("?"))
	m.handleKey(escKey())
	if m.mode != modeNormal {
		t.Errorf("esc should close the reference, mode=%v", m.mode)
	}
}

func TestPasteHintCountsTheClipboard(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar
	m.sidebar.cursor = 1

	if got := hints(m); strings.Contains(got, "paste") {
		t.Errorf("an empty clipboard offers no paste, got %q", got)
	}

	m.handleKey(press("c"))
	if got := hints(m); !strings.Contains(got, "p paste 1 request") {
		t.Errorf("the hint should count the clipboard, got %q", got)
	}
}

// TestClearSelectHintOnlyWithARange keeps 'esc' out of the hint until it does
// something, so the common case does not spend width on it.
func TestClearSelectHintOnlyWithARange(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar
	m.sidebar.cursor = 1

	if got := hints(m); strings.Contains(got, "clear selection") {
		t.Errorf("no range is open, got %q", got)
	}

	m.sidebar.anchor = m.cols[0].Requests[0].ID
	if got := hints(m); !strings.Contains(got, "esc clear selection") {
		t.Errorf("an open range should offer to clear itself, got %q", got)
	}
}

// TestCopyStaysLiveWhenTheCursorRestsOnAHeader: extending a range past the end
// of a folder parks the cursor on the next header, and the highlighted
// requests behind it are still what 'c' and 'D' would act on.
func TestCopyStaysLiveWhenTheCursorRestsOnAHeader(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar
	m.sidebar.cursor = 1 // listTodos
	m.handleKey(press("J"))

	if it, _ := m.sidebar.current(); it.reqID != 0 {
		t.Fatalf("this test needs the cursor on a header, got reqID=%d", it.reqID)
	}
	for _, want := range []string{"c copy", "D delete"} {
		if got := hints(m); !strings.Contains(got, want) {
			t.Errorf("a live range should keep %q offered, got %q", want, got)
		}
	}

	m.handleKey(press("c"))
	if m.clip.len() != 1 {
		t.Errorf("the range's request should still copy, got %d", m.clip.len())
	}
}

// TestDeleteHintNamesWhatItWouldDelete: 'D' on a folder header takes the whole
// folder, so the hint has to say so before the key is pressed, not after.
func TestDeleteHintNamesWhatItWouldDelete(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar

	for _, tc := range []struct {
		cursor int
		want   string
	}{
		{0, "D delete collection"},
		{1, "D delete"},
		{2, "D delete folder"},
	} {
		m.sidebar.cursor = tc.cursor
		if got := hints(m); !strings.Contains(got, tc.want) {
			t.Errorf("row %d should offer %q, got %q", tc.cursor, tc.want, got)
		}
	}

	// A range names its size instead. Two presses: the first lands on the
	// folder header between the two requests, which the span skips.
	m.sidebar.cursor = 1
	m.handleKey(press("J"))
	m.handleKey(press("J"))
	if got := hints(m); !strings.Contains(got, "D delete 2") {
		t.Errorf("a range should name its size, got %q", got)
	}
}

// TestDeleteHintFollowsTheSelectionNotTheCursor: with a range open and the
// cursor resting on a folder header, 'D' still means the selected requests —
// and the hint has to say "delete", not "delete folder", or it would advertise
// an escalation that cannot happen.
func TestDeleteHintFollowsTheSelectionNotTheCursor(t *testing.T) {
	m := helpModel(t)
	m.focus = focusSidebar
	m.sidebar.cursor = 1

	m.handleKey(press("J")) // onto the "admin" header, range still one request

	if it, _ := m.sidebar.current(); it.folder != "admin" {
		t.Fatalf("this test needs the cursor on the folder header, got %+v", it)
	}
	got := hints(m)
	if strings.Contains(got, "delete folder") {
		t.Errorf("an open range must not offer the folder, got %q", got)
	}
	if !strings.Contains(got, "D delete") {
		t.Errorf("the selected request is still deletable, got %q", got)
	}
}

// TestDeleteHintIsAbsentWithNoRows keeps the enable flag and the hint text
// agreeing: deleteLabel is both, so an empty sidebar must offer neither.
func TestDeleteHintIsAbsentWithNoRows(t *testing.T) {
	m := testModel(t)
	m.focus = focusSidebar

	if got := hints(m); strings.Contains(got, "delete") {
		t.Errorf("an empty sidebar has nothing to delete, got %q", got)
	}
}
