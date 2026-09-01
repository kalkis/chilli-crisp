package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/store"
)

// overlayList is a minimal modal chooser used for the environment picker,
// the history browser, the method picker and the key reference.
type overlayList struct {
	title string
	lines []string
	// footer replaces the default key hint when set. It holds bindings
	// rather than a rendered line because only view knows the box width the
	// hint has to fit.
	footer []key.Binding
	cursor int
}

func (o *overlayList) update(msg tea.KeyPressMsg, km *keyMap) {
	switch {
	case key.Matches(msg, km.Up):
		if o.cursor > 0 {
			o.cursor--
		}
	case key.Matches(msg, km.Down):
		if o.cursor < len(o.lines)-1 {
			o.cursor++
		}
	case key.Matches(msg, km.Top):
		o.cursor = 0
	case key.Matches(msg, km.Bottom):
		o.cursor = max(0, len(o.lines)-1)
	}
}

func (o *overlayList) view(width, height int) string {
	inner := max(20, min(width-8, 90))
	// The box's Width includes its border (2) and Padding(1, 2) sides (4);
	// longer lines would wrap and break the one-line-per-entry cursor math.
	textW := inner - 6
	maxRows := max(3, height-8)
	start := 0
	if o.cursor >= maxRows {
		start = o.cursor - maxRows + 1
	}
	body := ""
	if len(o.lines) == 0 {
		body = dimStyle.Render("(empty)")
	}
	for i := start; i < len(o.lines) && i-start < maxRows; i++ {
		line := truncate(o.lines[i], textW)
		if i == o.cursor {
			line = cursorStyle.Render(line)
		}
		body += line + "\n"
	}
	footer := o.footer
	if footer == nil {
		footer = []key.Binding{defaultKeys.Select, defaultKeys.Close}
	}
	box := overlayStyle.Width(inner).Render(
		titleStyle.Render(o.title) + "\n\n" + body + "\n" + helpLine(textW, footer...),
	)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// prompt is a one-line text input modal (new collection / request names).
type prompt struct {
	title  string
	action string // consumed by the root model on submit
	input  textinput.Model
}

func newPrompt(title, action, initial string) prompt {
	in := textinput.New()
	in.Prompt = "> "
	in.SetValue(initial)
	in.CursorEnd()
	in.Focus()
	return prompt{title: title, action: action, input: in}
}

func (p *prompt) view(width, height int) string {
	inner := max(20, min(width-8, 60))
	box := overlayStyle.Width(inner).Render(
		titleStyle.Render(p.title) + "\n\n" + p.input.View() + "\n\n" +
			helpLine(inner-6, defaultKeys.Submit, defaultKeys.CancelField),
	)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func historyLine(e store.HistoryEntry) string {
	when := e.Time.Format("Jan 02 15:04:05")
	status := "ERR"
	if e.Error == "" {
		status = fmt.Sprintf("%d", e.Status)
	}
	return fmt.Sprintf("%s  %-4s %-7s %s  (%s, %dms)",
		when, status, e.Request.Method, e.Request.URL, e.Request.QualifiedName(), e.DurationMS)
}

// methodPicker chooses one of the fixed HTTP methods, either for the request
// in the editor or for one that has been named but not yet created.
type methodPicker struct {
	list overlayList
	// pendingName is the name of a request waiting to be created; empty means
	// the picker is changing the method of the request already in the editor.
	pendingName string
}

// newMethodPicker builds the picker with the cursor on current. The rows are
// numbered because the digit keys take a method in one keystroke, which the
// list's own j/k/g/G navigation leaves free.
func newMethodPicker(current, pendingName string) methodPicker {
	title := "Method"
	if pendingName != "" {
		title = "Method for " + pendingName
	}
	lines := make([]string, len(methods))
	cursor := 0
	for i, name := range methods {
		lines[i] = fmt.Sprintf("%d  %s", i+1, name)
		if strings.EqualFold(name, current) {
			cursor = i
		}
	}
	return methodPicker{
		// The footer's digit range comes from the same binding the digits
		// dispatch through, so adding a method cannot leave the hint behind.
		list: overlayList{
			title:  title,
			lines:  lines,
			cursor: cursor,
			footer: []key.Binding{defaultKeys.PickMethod, defaultKeys.Select, defaultKeys.Close},
		},
		pendingName: pendingName,
	}
}

// choose reports the method a key selects. A digit picks its row outright;
// enter takes the row under the cursor. Anything else is navigation, and
// returns "".
func (p *methodPicker) choose(msg tea.KeyPressMsg, km *keyMap) string {
	if key.Matches(msg, km.PickMethod) {
		if i := digitIndex(msg.String()); i >= 0 {
			p.list.cursor = i
			return methods[i]
		}
		return ""
	}
	if key.Matches(msg, km.Select) && p.list.cursor < len(methods) {
		return methods[p.list.cursor]
	}
	return ""
}

// confirm is a modal for an action that cannot be undone. Unlike every other
// modal here, enter does not accept it — only the explicit key does, so a
// stray keystroke after the one that opened it cannot trigger the action.
type confirm struct {
	title  string
	detail []string // what is about to happen
	// accept is Accept rebound so the footer names this action: "y delete".
	accept  key.Binding
	action  string // consumed by the root model, like prompt.action
	targets []sel  // requests to act on, by ID
	// col is the collection a whole-collection action removes. Separate from
	// targets because an empty collection is still a thing to delete, so an
	// empty targets cannot mean "nothing to do".
	col model.ID
	// folder names the folder a folder action removes, for the status line.
	folder string
}

func (c *confirm) view(width, height int) string {
	inner := max(20, min(width-8, 60))
	// Width includes the border (2) and Padding(1, 2) sides (4); a long name
	// would otherwise wrap and push the footer out of the box.
	textW := inner - 6

	body := ""
	for _, line := range c.detail {
		body += truncate(line, textW) + "\n"
	}
	box := overlayStyle.Width(inner).Render(
		titleStyle.Render(c.title) + "\n\n" + body + "\n" +
			helpLine(textW, c.accept, defaultKeys.Deny),
	)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
