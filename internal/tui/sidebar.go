package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// sbItem addresses one sidebar row: a collection header (reqID == 0,
// folder == ""), a folder header (reqID == 0, folder set), or a request.
// Rows identify their request by ID so that state kept across a rebuild
// survives requests and collections being reordered or removed.
type sbItem struct {
	col    *model.Collection
	reqID  model.ID
	folder string
}

// sidebar lists collections with their requests.
type sidebar struct {
	items     []sbItem
	cursor    int
	scroll    int
	collapsed map[string]bool // foldKey -> hidden
	// anchor is the request a shift-extended range started from. It is an ID
	// rather than a row index for the same reason the cursor is restored by
	// row identity: the range has to mean the same thing after a rebuild
	// reorders, hides or removes rows. Zero means no range — the selection is
	// whatever single row the cursor is on.
	anchor model.ID
}

// foldKey identifies a collapsible row: a folder, or (with folder == "")
// the collection header itself.
func foldKey(colID model.ID, folder string) string {
	return fmt.Sprintf("%d/%s", colID, folder)
}

// sameRow reports whether two rows address the same thing.
func sameRow(a, b sbItem) bool {
	return a.col == b.col && a.reqID == b.reqID && a.folder == b.folder
}

func (s *sidebar) rebuild(cols []*model.Collection) {
	previous, hadCursor := s.current()

	s.items = nil
	for _, c := range cols {
		s.items = append(s.items, sbItem{col: c})
		if s.collapsed[foldKey(c.ID, "")] {
			continue
		}
		lastFolder := ""
		for ri := range c.Requests {
			f := c.Requests[ri].Folder
			if f != lastFolder {
				if f != "" {
					s.items = append(s.items, sbItem{col: c, folder: f})
				}
				lastFolder = f
			}
			if f != "" && s.collapsed[foldKey(c.ID, f)] {
				continue
			}
			s.items = append(s.items, sbItem{col: c, reqID: c.Requests[ri].ID})
		}
	}

	// Keep the cursor on whatever it was pointing at; if that row is gone,
	// stay at the same depth in the list.
	if hadCursor {
		for i, it := range s.items {
			if sameRow(it, previous) {
				s.cursor = i
				break
			}
		}
	}
	if s.cursor >= len(s.items) {
		s.cursor = max(0, len(s.items)-1)
	}
}

// toggleFold collapses or expands the collection or folder row under the
// cursor, reporting whether the cursor was on a collapsible row.
func (s *sidebar) toggleFold(cols []*model.Collection) bool {
	it, ok := s.current()
	if !ok || it.reqID != 0 {
		return false
	}
	if s.collapsed == nil {
		s.collapsed = map[string]bool{}
	}
	key := foldKey(it.col.ID, it.folder)
	s.collapsed[key] = !s.collapsed[key]
	// Rows before the toggled row are unaffected, so the cursor stays put.
	s.rebuild(cols)
	return true
}

func (s *sidebar) current() (sbItem, bool) {
	if s.cursor < 0 || s.cursor >= len(s.items) {
		return sbItem{}, false
	}
	return s.items[s.cursor], true
}

// selectRequest moves the cursor to a specific request row if present.
func (s *sidebar) selectRequest(active sel) {
	for i, it := range s.items {
		if it.reqID != 0 && it.col.ID == active.col && it.reqID == active.req {
			s.cursor = i
			return
		}
	}
}

// anchorRow returns the row index the range started from, or -1 when no range
// is active or the anchor is no longer on screen — a collapsed folder can hide
// it, and then the selection is just the cursor's row again.
func (s *sidebar) anchorRow() int {
	if s.anchor == 0 {
		return -1
	}
	for i, it := range s.items {
		if it.reqID == s.anchor {
			return i
		}
	}
	return -1
}

// selectionSpan returns the inclusive row range the selection covers.
func (s *sidebar) selectionSpan() (int, int) {
	a := s.anchorRow()
	if a < 0 {
		return s.cursor, s.cursor
	}
	return min(a, s.cursor), max(a, s.cursor)
}

// inSelection reports whether row i is part of the current selection. Only
// request rows count, so the highlight matches what an action would touch.
func (s *sidebar) inSelection(i int) bool {
	lo, hi := s.selectionSpan()
	return i >= lo && i <= hi && i < len(s.items) && s.items[i].reqID != 0
}

// selected returns the request rows the selection covers, in row order — the
// single row under the cursor when no range is active.
//
// Header rows inside a range are skipped rather than rejected, so extending a
// selection across a folder boundary selects the requests on both sides. That
// is also the seam for copying folders: a header would then contribute its
// folder instead of being passed over.
func (s *sidebar) selected() []sbItem {
	lo, hi := s.selectionSpan()
	var out []sbItem
	for i := lo; i <= hi && i < len(s.items); i++ {
		if i >= 0 && s.items[i].reqID != 0 {
			out = append(out, s.items[i])
		}
	}
	return out
}

// selectedCount is len(selected()) without the allocation, because refreshKeys
// asks on every key press and every frame.
func (s *sidebar) selectedCount() int {
	lo, hi := s.selectionSpan()
	n := 0
	for i := max(lo, 0); i <= hi && i < len(s.items); i++ {
		if s.items[i].reqID != 0 {
			n++
		}
	}
	return n
}

// extend moves the cursor by delta, starting a range from the current row if
// one is not already open. Anchoring on the row being left (not the one being
// entered) is what makes the first shift+down select two requests.
func (s *sidebar) extend(delta int) {
	if s.anchor == 0 {
		if it, ok := s.current(); ok && it.reqID != 0 {
			s.anchor = it.reqID
		}
	}
	s.cursor = min(max(s.cursor+delta, 0), max(0, len(s.items)-1))
}

// clearSelection collapses a range back to the cursor row.
func (s *sidebar) clearSelection() { s.anchor = 0 }

func (s *sidebar) update(msg tea.KeyPressMsg, km *keyMap) {
	switch {
	case key.Matches(msg, km.ExtendUp):
		s.extend(-1)
		return
	case key.Matches(msg, km.ExtendDown):
		s.extend(1)
		return
	case key.Matches(msg, km.Up):
		if s.cursor > 0 {
			s.cursor--
		}
	case key.Matches(msg, km.Down):
		if s.cursor < len(s.items)-1 {
			s.cursor++
		}
	case key.Matches(msg, km.Top):
		s.cursor = 0
	case key.Matches(msg, km.Bottom):
		s.cursor = max(0, len(s.items)-1)
	default:
		return
	}
	// Any plain motion ends the range: a selection the user can no longer see
	// the extent of must not survive to be acted on.
	s.clearSelection()
}

func (s *sidebar) view(cols []*model.Collection, width, height int, focused bool, active sel) string {
	if len(s.items) == 0 {
		return dimStyle.Render(" (no collections)\n\n 'N' new collection\n or import one:\n chilli-crisp import\n   openapi spec.yaml")
	}

	// Keep the cursor visible.
	if s.cursor < s.scroll {
		s.scroll = s.cursor
	}
	if s.cursor >= s.scroll+height {
		s.scroll = s.cursor - height + 1
	}

	var lines []string
	for i := s.scroll; i < len(s.items) && i-s.scroll < height; i++ {
		it := s.items[i]
		var line string
		r := s.requestFor(it)
		switch {
		case i == s.cursor && focused:
			// Uncoloured so the cursor highlight is uniform.
			line = cursorStyle.Render(truncate(s.rowText(it, active), width))
		case focused && s.inSelection(i):
			// Dimmer than the cursor so the two read as one range with a
			// position in it, rather than as two cursors.
			line = selectedStyle.Render(truncate(s.rowText(it, active), width))
		case it.reqID == 0 || r == nil:
			line = collectionStyle.Render(truncate(s.rowText(it, active), width))
		default:
			indent := ""
			if r.Folder != "" {
				indent = "  "
			}
			line = fmt.Sprintf("%s%s %s %s", indent, activeMark(it, active), methodStyle(r.Method).Render(fmt.Sprintf("%-4s", shortMethod(r.Method))), truncate(r.Name, width-8-len(indent)))
		}
		lines = append(lines, line)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// requestFor resolves a request row's request, or nil for a header row.
func (s *sidebar) requestFor(it sbItem) *model.Request {
	if it.reqID == 0 || it.col == nil {
		return nil
	}
	i := it.col.FindRequest(it.reqID)
	if i < 0 {
		return nil
	}
	return &it.col.Requests[i]
}

// activeMark dots the request currently loaded in the editor.
func activeMark(it sbItem, active sel) string {
	if it.col != nil && it.col.ID == active.col && it.reqID == active.req {
		return "•"
	}
	return " "
}

// foldMark shows a row's expand state: ▾ open, ▸ collapsed.
func (s *sidebar) foldMark(it sbItem) string {
	if s.collapsed[foldKey(it.col.ID, it.folder)] {
		return "▸"
	}
	return "▾"
}

// rowText is a row's uncolored text.
func (s *sidebar) rowText(it sbItem, active sel) string {
	r := s.requestFor(it)
	if r == nil {
		if it.folder != "" {
			return "  " + s.foldMark(it) + " " + it.folder
		}
		return s.foldMark(it) + " " + it.col.Name
	}
	indent := ""
	if r.Folder != "" {
		indent = "  "
	}
	return fmt.Sprintf("%s%s %-4s %s", indent, activeMark(it, active), shortMethod(r.Method), r.Name)
}

func shortMethod(m string) string {
	switch m {
	case "DELETE":
		return "DEL"
	case "OPTIONS":
		return "OPT"
	case "PATCH":
		return "PAT"
	case "QUERY":
		return "QRY"
	default:
		if len(m) > 4 {
			return m[:4]
		}
		return m
	}
}
