package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// twoCollections builds a fixture with folders, mirroring the shape the
// Postman importer produces.
func twoCollections() []*model.Collection {
	a := &model.Collection{
		Name: "alpha",
		Requests: []model.Request{
			{Name: "first", Method: "GET", Folder: "public"},
			{Name: "second", Method: "POST", Folder: "public"},
			{Name: "loose", Method: "GET"},
		},
	}
	b := &model.Collection{
		Name: "beta",
		Requests: []model.Request{
			{Name: "only", Method: "GET", Folder: "admin"},
		},
	}
	a.EnsureIDs()
	b.EnsureIDs()
	return []*model.Collection{a, b}
}

// rowFor reports the index of the row holding a given request.
func rowFor(s *sidebar, col *model.Collection, reqID model.ID) int {
	for i, it := range s.items {
		if it.col == col && it.reqID == reqID {
			return i
		}
	}
	return -1
}

func TestFoldStateFollowsCollectionAcrossReorder(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)

	// Collapse the folder in the second collection.
	s.cursor = rowFor(&s, cols[1], cols[1].Requests[0].ID) - 1 // the folder header
	if !s.toggleFold(cols) {
		t.Fatal("expected the cursor to be on a folder header")
	}
	if rowFor(&s, cols[1], cols[1].Requests[0].ID) != -1 {
		t.Fatal("collapsing should hide the folder's request")
	}

	// Reorder the collections. Fold state is keyed by ID, so it must stay
	// with beta rather than jumping to whatever is now second.
	cols[0], cols[1] = cols[1], cols[0]
	s.rebuild(cols)

	beta := cols[0]
	alpha := cols[1]
	if rowFor(&s, beta, beta.Requests[0].ID) != -1 {
		t.Error("beta's folder should still be collapsed after the reorder")
	}
	for _, r := range alpha.Requests {
		if rowFor(&s, alpha, r.ID) == -1 {
			t.Errorf("alpha's request %q should be visible after the reorder", r.Name)
		}
	}
}

func TestCursorFollowsItsRowAcrossReorder(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)

	target := cols[1].Requests[0]
	s.cursor = rowFor(&s, cols[1], target.ID)
	if s.cursor < 0 {
		t.Fatal("fixture request not found")
	}

	cols[0], cols[1] = cols[1], cols[0]
	s.rebuild(cols)

	it, ok := s.current()
	if !ok {
		t.Fatal("expected a row under the cursor")
	}
	if it.reqID != target.ID {
		t.Errorf("cursor should still be on %q, got row %+v", target.Name, it)
	}
}

func TestSelectionSurvivesEarlierRequestDeletion(t *testing.T) {
	cols := twoCollections()
	col := cols[0]
	target := col.Requests[2] // "loose", after the two we delete around

	e := newEditor()
	e.load(col, target.ID)

	// Delete the first request, exactly as the D key does.
	col.Requests = append(col.Requests[:0], col.Requests[1:]...)

	got := e.request()
	if got == nil {
		t.Fatal("editor lost its request after an earlier one was deleted")
	}
	if got.Name != "loose" {
		t.Errorf("editor now points at %q, want loose", got.Name)
	}
}

func TestDeletedRequestLeavesEditorEmpty(t *testing.T) {
	cols := twoCollections()
	col := cols[0]
	doomed := col.Requests[0]

	e := newEditor()
	e.load(col, doomed.ID)
	col.Requests = append(col.Requests[:0], col.Requests[1:]...)

	if e.request() != nil {
		t.Error("editor should resolve to nil once its request is deleted")
	}
}

func TestCollapsedCollectionHidesRows(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	total := len(s.items)

	s.cursor = 0 // alpha's collection header
	if !s.toggleFold(cols) {
		t.Fatal("expected the cursor to be on a collection header")
	}
	if len(s.items) >= total {
		t.Errorf("collapsing a collection should hide rows: %d -> %d", total, len(s.items))
	}
	for _, r := range cols[0].Requests {
		if rowFor(&s, cols[0], r.ID) != -1 {
			t.Errorf("request %q should be hidden", r.Name)
		}
	}
	// The other collection is untouched.
	if rowFor(&s, cols[1], cols[1].Requests[0].ID) == -1 {
		t.Error("collapsing alpha should not hide beta's requests")
	}
}

func TestViewMarksActiveRequest(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	active := sel{col: cols[0].ID, req: cols[0].Requests[1].ID}

	out := s.view(cols, 28, 20, false, active)
	var marked []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "•") {
			marked = append(marked, strings.TrimSpace(line))
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "second") {
		t.Errorf("exactly the active request should carry the dot, got %v", marked)
	}
}

// selectedNames is the request names the selection covers, in row order.
func selectedNames(s *sidebar) []string {
	var out []string
	for _, it := range s.selected() {
		if r := s.requestFor(it); r != nil {
			out = append(out, r.Name)
		}
	}
	return out
}

func TestSelectionIsJustTheCursorByDefault(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	s.cursor = rowFor(&s, cols[0], cols[0].Requests[0].ID)

	if got := selectedNames(&s); !slices.Equal(got, []string{"first"}) {
		t.Errorf("with no range open the selection is the cursor's row, got %v", got)
	}
}

func TestShiftExtendsTheSelection(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	s.cursor = rowFor(&s, cols[0], cols[0].Requests[0].ID)
	km := defaultKeys

	s.update(press("J"), &km)

	if got := selectedNames(&s); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("shift+down must select both rows, got %v", got)
	}
}

// TestSelectionSkipsHeaderRows is what lets a range cross a folder boundary:
// the header between two runs is passed over rather than rejecting the range.
func TestSelectionSkipsHeaderRows(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	s.cursor = rowFor(&s, cols[0], cols[0].Requests[1].ID) // "second", in public
	km := defaultKeys

	// Down past "loose", the collection header for beta, the admin header,
	// and on to "only".
	for range 4 {
		s.update(press("J"), &km)
	}

	got := selectedNames(&s)
	want := []string{"second", "loose", "only"}
	if !slices.Equal(got, want) {
		t.Errorf("a range must yield only its request rows: got %v, want %v", got, want)
	}
}

func TestPlainMotionClearsTheSelection(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	s.cursor = rowFor(&s, cols[0], cols[0].Requests[0].ID)
	km := defaultKeys

	s.update(press("J"), &km)
	s.update(press("j"), &km)

	if s.anchor != 0 {
		t.Errorf("plain motion must end the range, anchor=%d", s.anchor)
	}
	if got := selectedNames(&s); len(got) != 1 {
		t.Errorf("after clearing, the selection is one row, got %v", got)
	}
}

// TestSelectionSurvivesReorder is why the anchor is an ID: the range has to
// still mean the same two requests after the collection is rebuilt around it.
func TestSelectionSurvivesReorder(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	s.cursor = rowFor(&s, cols[0], cols[0].Requests[0].ID)
	km := defaultKeys
	s.update(press("J"), &km)

	cols = []*model.Collection{cols[1], cols[0]}
	s.rebuild(cols)

	if got := selectedNames(&s); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("the range must survive a reorder, got %v", got)
	}
}

// TestSelectionFallsBackWhenTheAnchorIsHidden: collapsing a folder can take
// the anchor off screen, and a range whose extent the user cannot see must not
// stay live to be acted on.
func TestSelectionFallsBackWhenTheAnchorIsHidden(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	s.cursor = rowFor(&s, cols[0], cols[0].Requests[0].ID)
	km := defaultKeys
	s.update(press("J"), &km) // anchor on "first", cursor on "second"

	s.collapsed = map[string]bool{foldKey(cols[0].ID, "public"): true}
	s.rebuild(cols)

	for _, name := range selectedNames(&s) {
		if name == "first" {
			t.Fatal("a hidden anchor must not keep contributing to the selection")
		}
	}
}

// TestExtendFromAHeaderJustMoves: a range anchors only on a request, so from a
// header the extend keys step onto the row a range can start from rather than
// doing nothing at all.
func TestExtendFromAHeaderJustMoves(t *testing.T) {
	cols := twoCollections()
	var s sidebar
	s.rebuild(cols)
	s.cursor = 0 // the "alpha" collection header
	km := defaultKeys

	// Rows 0 and 1 are the collection header and the "public" folder header;
	// neither can anchor, so both presses only move.
	s.update(press("J"), &km)
	s.update(press("J"), &km)

	if s.anchor != 0 {
		t.Errorf("a header cannot anchor a range, anchor=%d", s.anchor)
	}
	if got := selectedNames(&s); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("the cursor should have walked onto a request, got %v", got)
	}

	// And from the request it landed on, the next press does open a range.
	s.update(press("J"), &km)
	if got := selectedNames(&s); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("the next press starts the range, got %v", got)
	}
}
