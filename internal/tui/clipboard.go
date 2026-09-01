package tui

import (
	"fmt"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// clipKind names what a clipped item stands for. Only clipRequest is produced
// today; the kind is the seam for folders and collections, which will paste
// into the same target through the same loop rather than needing a second
// paste path.
type clipKind int

const (
	clipRequest clipKind = iota
)

// clipItem is one copied thing. A request is held as a deep Clone, so the
// buffer is a snapshot: editing or deleting the source afterwards does not
// change what pastes.
type clipItem struct {
	kind clipKind
	req  model.Request
}

// clipboard is this process's own copy buffer.
//
// It deliberately never touches the system clipboard. Nothing in this TUI does
// — even 'y' renders curl into the response pane rather than copying it — so
// copying a request here cannot clobber whatever the user has yanked in their
// editor or shell. The cost is that the buffer does not outlive the process,
// which is the right trade for a buffer whose contents are only meaningful to
// this program anyway.
type clipboard struct {
	items []clipItem
}

func (c *clipboard) empty() bool { return len(c.items) == 0 }

func (c *clipboard) len() int { return len(c.items) }

// copyRequests replaces the buffer with deep copies of reqs.
func (c *clipboard) copyRequests(reqs []*model.Request) {
	c.items = make([]clipItem, 0, len(reqs))
	for _, r := range reqs {
		c.items = append(c.items, clipItem{kind: clipRequest, req: r.Clone()})
	}
}

// requests returns a fresh deep copy of every clipped request, ready to be
// inserted. Cloning again on the way out is not redundant with the clone on
// the way in: without it, pasting the same buffer twice would hand out two
// requests sharing one Headers array, and editing a header on one would
// silently change the other.
func (c *clipboard) requests() []model.Request {
	out := make([]model.Request, 0, len(c.items))
	for _, it := range c.items {
		if it.kind == clipRequest {
			out = append(out, it.req.Clone())
		}
	}
	return out
}

// pasteTarget is where a paste lands: the collection, the folder the cursor is
// in, and the index in col.Requests to insert at.
//
// The index must fall inside the target folder's contiguous run. A folder is
// not a container — sidebar.rebuild opens one every time Request.Folder
// differs from the previous request's — so a request inserted outside the run
// starts a second one, and the folder is drawn twice with a single fold state
// shared between the halves.
type pasteTarget struct {
	col    *model.Collection
	folder string
	at     int
}

// countRequests pluralises a request count for status lines, hints and the
// delete modal's title, so the three cannot disagree.
func countRequests(n int) string {
	if n == 1 {
		return "1 request"
	}
	return fmt.Sprintf("%d requests", n)
}

// folderRunEnd returns the index just past the first contiguous run of
// requests in folder, or -1 when the folder has no requests. A folder header
// row exists only because some request named it, so the run is always found
// for a header the cursor can actually sit on.
func folderRunEnd(col *model.Collection, folder string) int {
	start := -1
	for i := range col.Requests {
		if col.Requests[i].Folder == folder {
			start = i
			break
		}
	}
	if start < 0 {
		return -1
	}
	end := start
	for end < len(col.Requests) && col.Requests[end].Folder == folder {
		end++
	}
	return end
}
