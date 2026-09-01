package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/vars"
)

// varLayers is the precedence stack as separate maps, so a row can report
// which layer its value came from. The order here must match
// httpclient.ResolverFor.
type varLayers struct {
	req, env, folder, col map[string]string
}

// lookup returns a name's effective value and the layer that supplied it.
func (l varLayers) lookup(name string) (value, source string, ok bool) {
	for _, layer := range []struct {
		vars map[string]string
		name string
	}{
		{l.req, "req"},
		{l.env, "env"},
		{l.folder, "folder"},
		{l.col, "col"},
	} {
		if v, found := layer.vars[name]; found {
			return v, layer.name, true
		}
	}
	return "", "", false
}

// withoutRequest drops the request layer, leaving what a name would resolve
// to if this request declared no override of its own.
func (l varLayers) withoutRequest() varLayers {
	l.req = nil
	return l
}

// pathRow is one {{variable}} referenced by the request URL.
type pathRow struct {
	name   string
	value  string
	source string // "" when no layer defines it
}

// pathEditor is the Path tab: the variables a request's URL references, with
// the value each currently resolves to and an override that applies to this
// request alone.
//
// Its rows are derived from the URL on every keystroke and every frame rather
// than cached, so editing the URL with 'u' cannot leave a stale table behind.
// Only the cursor and the field editor are state.
type pathEditor struct {
	cursor  int
	editing bool
	input   textinput.Model
}

func newPathEditor() pathEditor {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 0
	return pathEditor{input: in}
}

func (p *pathEditor) Editing() bool { return p.editing }

// rows builds the table for r. layers supplies the effective values.
func pathRows(r *model.Request, layers varLayers) []pathRow {
	names := vars.Names(r.URL)
	rows := make([]pathRow, 0, len(names))
	for _, name := range names {
		value, source, _ := layers.lookup(name)
		rows = append(rows, pathRow{name: name, value: value, source: source})
	}
	return rows
}

// clamp keeps the cursor on a real row after the URL changes.
func (p *pathEditor) clamp(n int) {
	if p.cursor >= n {
		p.cursor = max(0, n-1)
	}
	if n == 0 {
		p.editing = false
	}
}

func (p *pathEditor) startEdit(row pathRow) {
	p.editing = true
	p.input.SetValue(row.value)
	p.input.CursorEnd()
	p.input.Focus()
}

// update handles a key. changed reports that the request was modified and
// must be saved.
func (p *pathEditor) update(msg tea.KeyPressMsg, r *model.Request, layers varLayers, km *keyMap) (tea.Cmd, bool) {
	rows := pathRows(r, layers)
	p.clamp(len(rows))

	if p.editing {
		switch {
		case key.Matches(msg, km.SaveField):
			p.editing = false
			p.input.Blur()
			return nil, setRequestVar(r, rows[p.cursor].name, p.input.Value(), layers)
		case key.Matches(msg, km.CancelField):
			p.editing = false
			p.input.Blur()
			return nil, false
		default:
			var cmd tea.Cmd
			p.input, cmd = p.input.Update(msg)
			return cmd, false
		}
	}

	switch {
	case key.Matches(msg, km.Up):
		if p.cursor > 0 {
			p.cursor--
		}
	case key.Matches(msg, km.Down):
		if p.cursor < len(rows)-1 {
			p.cursor++
		}
	case key.Matches(msg, km.EditPath):
		if len(rows) > 0 {
			p.startEdit(rows[p.cursor])
			return textinput.Blink, false
		}
	// ResetPath is only enabled on a row this request actually overrides,
	// so the source check the guard used to make lives in refreshKeys now.
	case key.Matches(msg, km.ResetPath):
		if len(rows) > 0 {
			return nil, clearRequestVar(r, rows[p.cursor].name)
		}
	}
	return nil, false
}

// setRequestVar writes a request-level override. Blanking a value whose name
// a wider layer still defines is a reset rather than an override to "": the
// user is asking for the inherited value back, and an empty override would
// silently send an empty path segment instead.
func setRequestVar(r *model.Request, name, value string, layers varLayers) bool {
	if value == "" {
		if _, _, inherited := layers.withoutRequest().lookup(name); inherited {
			return clearRequestVar(r, name)
		}
	}
	if existing, ok := r.Vars[name]; ok && existing == value {
		return false
	}
	if r.Vars == nil {
		r.Vars = map[string]string{}
	}
	r.Vars[name] = value
	return true
}

// clearRequestVar drops an override so the name falls back to a wider layer.
// The map is dropped once empty so the request serializes without a `vars:`
// key at all.
func clearRequestVar(r *model.Request, name string) bool {
	if _, ok := r.Vars[name]; !ok {
		return false
	}
	delete(r.Vars, name)
	if len(r.Vars) == 0 {
		r.Vars = nil
	}
	return true
}

func (p *pathEditor) view(width int, focused bool, r *model.Request, layers varLayers) string {
	rows := pathRows(r, layers)
	p.clamp(len(rows))
	if len(rows) == 0 {
		return dimStyle.Render("  (the URL references no {{variables}})")
	}

	nameW := max(10, width/4)
	var lines []string
	for i, row := range rows {
		value := row.value
		if p.editing && i == p.cursor {
			value = p.input.View()
		}
		source := "(undefined)"
		if row.source != "" {
			source = "(" + row.source + ")"
		}
		if value == "" && !(p.editing && i == p.cursor) {
			value = dimStyle.Render("—")
		}
		line := truncate(fmt.Sprintf(" %-*s %-9s %s", nameW, truncate(row.name, nameW), source, value), width)
		switch {
		case i == p.cursor && focused:
			line = cursorStyle.Render(line)
		case row.source == "":
			line = dimStyle.Render(line)
		}
		lines = append(lines, line)
	}
	// No key legend here: the status bar names this tab's keys, and does it
	// per row — 'r' only appears when the cursor is on an override.
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
