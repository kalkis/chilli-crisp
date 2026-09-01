package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	// Aliased because the package names collide with the truncate helper
	// below. reflow is already how this package measures text (see
	// response.go), so it stays the one measuring stick — and the width check
	// and the cut must use the same one or they disagree at the boundary.
	reflowansi "github.com/muesli/reflow/ansi"
	trunc "github.com/muesli/reflow/truncate"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// kvEditor edits a list of name/value params (query params, headers, form
// fields) with per-row enable toggling.
type kvEditor struct {
	rows    []model.Param
	cursor  int
	editing bool
	editCol int // 0 = name, 1 = value
	input   textinput.Model
	// hideToggle drops the enabled checkbox for callers whose rows have no
	// disabled state — variables, where a name either has a value or does
	// not exist.
	hideToggle bool
}

func newKVEditor() kvEditor {
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 0
	return kvEditor{input: in}
}

func (k *kvEditor) load(rows []model.Param) {
	k.rows = append([]model.Param(nil), rows...)
	if k.cursor >= len(k.rows) {
		k.cursor = max(0, len(k.rows)-1)
	}
	k.editing = false
}

func (k *kvEditor) Editing() bool { return k.editing }

func (k *kvEditor) startEdit(col int) {
	if len(k.rows) == 0 {
		return
	}
	k.editing = true
	k.editCol = col
	if col == 0 {
		k.input.SetValue(k.rows[k.cursor].Name)
	} else {
		k.input.SetValue(k.rows[k.cursor].Value)
	}
	k.input.CursorEnd()
	k.input.Focus()
}

func (k *kvEditor) commitEdit() {
	if k.editCol == 0 {
		k.rows[k.cursor].Name = k.input.Value()
	} else {
		k.rows[k.cursor].Value = k.input.Value()
	}
	k.editing = false
	k.input.Blur()
}

// update handles a key. changed reports that rows were modified and should
// be synced back and saved.
func (k *kvEditor) update(msg tea.KeyPressMsg, km *keyMap) (tea.Cmd, bool) {
	if k.editing {
		switch {
		case key.Matches(msg, km.SaveField):
			k.commitEdit()
			if k.editCol == 0 { // convenience: name -> value in one flow
				k.startEdit(1)
			}
			return nil, true
		case key.Matches(msg, km.NextField):
			k.commitEdit()
			k.startEdit(1 - k.editCol)
			return nil, true
		case key.Matches(msg, km.CancelField):
			k.editing = false
			k.input.Blur()
			return nil, false
		default:
			var cmd tea.Cmd
			k.input, cmd = k.input.Update(msg)
			return cmd, false
		}
	}

	switch {
	case key.Matches(msg, km.Up):
		if k.cursor > 0 {
			k.cursor--
		}
	case key.Matches(msg, km.Down):
		if k.cursor < len(k.rows)-1 {
			k.cursor++
		}
	case key.Matches(msg, km.EditName):
		k.startEdit(0)
	case key.Matches(msg, km.EditValue):
		k.startEdit(1)
	case key.Matches(msg, km.AddRow):
		k.rows = append(k.rows, model.Param{})
		k.cursor = len(k.rows) - 1
		k.startEdit(0)
		return nil, true
	case key.Matches(msg, km.DeleteRow):
		if len(k.rows) > 0 {
			k.rows = append(k.rows[:k.cursor], k.rows[k.cursor+1:]...)
			if k.cursor >= len(k.rows) {
				k.cursor = max(0, len(k.rows)-1)
			}
			return nil, true
		}
	case key.Matches(msg, km.ToggleRow):
		if !k.hideToggle && len(k.rows) > 0 {
			k.rows[k.cursor].Disabled = !k.rows[k.cursor].Disabled
			return nil, true
		}
	}
	return nil, false
}

func (k *kvEditor) view(width int, focused bool) string {
	if len(k.rows) == 0 {
		return dimStyle.Render("  (empty — ") + helpLine(width, defaultKeys.AddRow) + dimStyle.Render(")")
	}
	nameW := max(10, width/3)
	var lines []string
	for i, row := range k.rows {
		mark := "[x]"
		if row.Disabled {
			mark = "[ ]"
		}
		if k.hideToggle {
			mark = ""
		}
		name, value := row.Name, row.Value
		if k.editing && i == k.cursor {
			if k.editCol == 0 {
				name = k.input.View()
			} else {
				value = k.input.View()
			}
		}
		line := strings.TrimRight(fmt.Sprintf(" %s %-*s %s", mark, nameW, truncate(name, nameW), value), " ")
		line = truncate(line, width)
		if i == k.cursor && focused {
			line = cursorStyle.Render(line)
		} else if row.Disabled {
			line = dimStyle.Render(line)
		}
		lines = append(lines, line)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// truncate shortens s to at most w display cells, marking a cut with an
// ellipsis. It measures printable width and leaves ANSI sequences intact,
// which matters twice over: wide runes take two cells, and the rows this
// truncates may contain a styled textinput view whose escape bytes must
// neither count toward the budget nor be cut in half.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	// reflow subtracts the tail's width from the budget before it starts
	// counting, so ask first: content that already fits must come through
	// untouched rather than losing a cell to an ellipsis it does not need.
	if reflowansi.PrintableRuneWidth(s) <= w {
		return s
	}
	return trunc.StringWithTail(s, uint(w), "…") // #nosec G115 -- w > 0
}
