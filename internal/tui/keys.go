package tui

import (
	"fmt"
	"strconv"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"

	reflowansi "github.com/muesli/reflow/ansi"
)

// keyMap is every key this TUI binds, and the single source of truth for both
// dispatch and the hints the user sees. Handlers match against it with
// key.Matches rather than comparing msg.String(), and the status bar and every
// modal footer render from the same bindings — so a key cannot be renamed,
// rebound or removed without its help text following.
//
// Keys that mean different things in different places get separate fields that
// share a keystroke: OpenRequest and OpenVars are both enter, as are EditName,
// EditPath, EditAuth and EditBody. Exactly one of an ambiguous pair is enabled
// at a time (see refreshKeys), which is what keeps the switches unambiguous
// and lets each context name its own action instead of hedging.
type keyMap struct {
	// Global — available in normal mode wherever focus sits.
	Quit      key.Binding
	NextPane  key.Binding
	PrevPane  key.Binding
	Send      key.Binding
	Env       key.Binding
	Workspace key.Binding
	History   key.Binding
	Curl      key.Binding
	Method    key.Binding
	NewCol    key.Binding
	NewReq    key.Binding
	Help      key.Binding

	// Cursor motion, shared by the sidebar, the lists and the row editors.
	Up     key.Binding
	Down   key.Binding
	Top    key.Binding
	Bottom key.Binding

	// Sidebar.
	Fold        key.Binding
	OpenRequest key.Binding
	OpenVars    key.Binding
	Delete      key.Binding
	Copy        key.Binding
	Paste       key.Binding
	ExtendUp    key.Binding
	ExtendDown  key.Binding
	ClearSelect key.Binding

	// Editor pane chrome, live on every tab.
	PrevTab     key.Binding
	NextTab     key.Binding
	EditURL     key.Binding
	CycleMethod key.Binding

	// Params and Headers rows.
	EditName  key.Binding
	EditValue key.Binding
	AddRow    key.Binding
	DeleteRow key.Binding
	ToggleRow key.Binding

	// Path tab.
	EditPath  key.Binding
	ResetPath key.Binding

	// Auth tab.
	CycleAuth key.Binding
	EditAuth  key.Binding

	// Body tab.
	CycleBody      key.Binding
	EditBody       key.Binding
	ExternalEditor key.Binding

	// Response pane.
	PrevRespTab key.Binding
	NextRespTab key.Binding
	Scroll      key.Binding

	// Open field editors.
	SaveField    key.Binding
	CancelField  key.Binding
	NextField    key.Binding
	FinishBody   key.Binding
	AbortSending key.Binding

	// Modals.
	Select     key.Binding
	Replay     key.Binding
	Submit     key.Binding
	Close      key.Binding
	PickMethod key.Binding
	Deny       key.Binding
	// Accept is rebuilt per confirmation so its help names that action's
	// verb; see confirm.
	Accept key.Binding
}

// defaultKeys is the canonical binding set. appModel copies it by value so a
// model's context flags stay its own — key.Binding copies its disabled flag
// and shares its immutable keys slice.
//
// It is also what the '?' overlay renders, rather than a model's live copy:
// the reference must list every key, including ones the cursor's current
// position has disabled.
var defaultKeys = keyMap{
	Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	NextPane:  key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next pane")),
	PrevPane:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev pane")),
	Send:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "send")),
	Env:       key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "environment")),
	Workspace: key.NewBinding(key.WithKeys("W"), key.WithHelp("W", "workspace")),
	History:   key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "history")),
	Curl:      key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy as curl")),
	Method:    key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "method")),
	NewCol:    key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "new collection")),
	NewReq:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new request")),
	Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),

	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Top:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "top")),
	Bottom: key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),

	Fold:        key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "fold")),
	OpenRequest: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	OpenVars:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "vars")),
	Delete:      key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete")),
	Copy:        key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
	// Paste's help gains the clipboard count in refreshKeys; this is the text
	// the '?' overlay shows, where there is no clipboard to count.
	//
	// 'c'/'p' rather than ctrl+c/ctrl+v: ctrl+c is this TUI's quit key, and
	// ctrl+v never reaches the program in terminals that bind it to paste
	// (Windows Terminal, VS Code) — and with bracketed paste on, a terminal
	// paste arrives as tea.PasteMsg anyway.
	Paste: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "paste")),
	// Both the arrow and the letter form: a shifted letter stringifies as the
	// capital ("K", never "shift+k"), and not every terminal emits CSI 1;2A
	// for shift+arrow. Binding both means neither has to be the one that works.
	ExtendUp:    key.NewBinding(key.WithKeys("shift+up", "K"), key.WithHelp("shift+↑/K", "select up")),
	ExtendDown:  key.NewBinding(key.WithKeys("shift+down", "J"), key.WithHelp("shift+↓/J", "select down")),
	ClearSelect: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear selection")),

	PrevTab:     key.NewBinding(key.WithKeys("left", "["), key.WithHelp("←/[", "prev tab")),
	NextTab:     key.NewBinding(key.WithKeys("right", "]"), key.WithHelp("→/]", "next tab")),
	EditURL:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "url")),
	CycleMethod: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "cycle method")),

	EditName:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "name")),
	EditValue: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "value")),
	AddRow:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
	DeleteRow: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete row")),
	ToggleRow: key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "on/off")),

	EditPath:  key.NewBinding(key.WithKeys("enter", "v"), key.WithHelp("enter", "override")),
	ResetPath: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reset")),

	CycleAuth: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "auth type")),
	EditAuth:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "edit")),

	CycleBody:      key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "body type")),
	EditBody:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "edit")),
	ExternalEditor: key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "$EDITOR")),

	PrevRespTab: key.NewBinding(key.WithKeys("left", "["), key.WithHelp("←/[", "prev tab")),
	NextRespTab: key.NewBinding(key.WithKeys("right", "]"), key.WithHelp("→/]", "next tab")),
	// Scroll never dispatches — the viewport owns its own keymap. It exists
	// so the hint can say the pane scrolls.
	Scroll: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "scroll")),

	SaveField:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save")),
	CancelField:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	NextField:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
	FinishBody:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "stop editing")),
	AbortSending: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel send")),

	Select:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	Replay:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send again")),
	Submit:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
	Close:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	PickMethod: methodDigits(),
	Deny:       key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "cancel")),
	Accept:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm")),
}

// methodDigits binds one digit per method, so the picker's hint and the keys
// it accepts are both derived from methods and cannot drift apart when a
// method is added.
func methodDigits() key.Binding {
	keys := make([]string, len(methods))
	for i := range methods {
		keys[i] = strconv.Itoa(i + 1)
	}
	return key.NewBinding(
		key.WithKeys(keys...),
		key.WithHelp(fmt.Sprintf("1-%d", len(methods)), "pick"),
	)
}

// digitIndex reports which method a digit key selects, or -1.
func digitIndex(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil || i < 1 || i > len(methods) {
		return -1
	}
	return i - 1
}

// helpLine renders bindings as one dim key hint, ellipsized to w cells.
//
// The help.Model is built per call rather than shared: it is four strings and
// a Styles, so this costs less than threading mutable width state through the
// status bar and every modal footer. Its separator is overridden because the
// library defaults to a bullet and this package has always used a middle dot.
//
// The truncate is not redundant. ShortHelpView drops whole items to fit, but
// only while its ellipsis still has room: once the running width is within a
// couple of cells of the budget the tail no longer fits, and bubbles v2.2.0
// then appends every remaining item rather than stopping (help.go,
// shouldAddItem). So the library gives the tidy item-boundary cut when it can,
// and truncate — the package's own ANSI-aware one — enforces the budget.
func helpLine(w int, bindings ...key.Binding) string {
	if w <= 0 {
		return ""
	}
	return truncate(helpRender(w, bindings), w)
}

// helpRender is helpLine without the hard clamp. A w of 0 renders every
// binding at its natural width, which is how helpRows measures a candidate
// line before committing to it.
func helpRender(w int, bindings []key.Binding) string {
	h := help.New()
	h.ShortSeparator = " · "
	h.Styles.ShortKey = dimStyle
	h.Styles.ShortDesc = dimStyle
	h.Styles.ShortSeparator = dimStyle
	h.Styles.Ellipsis = dimStyle
	h.SetWidth(w)
	return h.ShortHelpView(bindings)
}

// helpRows packs bindings into as many lines of w cells as they need. The
// reference wraps a long group rather than ellipsizing it, because a key that
// is cut from the reference has nowhere else to be documented.
func helpRows(w int, bindings []key.Binding) []string {
	var rows []string
	for i := 0; i < len(bindings); {
		n := 1
		for i+n < len(bindings) && helpFits(w, bindings[i:i+n+1]) {
			n++
		}
		rows = append(rows, helpLine(w, bindings[i:i+n]...))
		i += n
	}
	return rows
}

func helpFits(w int, bindings []key.Binding) bool {
	return reflowansi.PrintableRuneWidth(helpRender(0, bindings)) <= w
}

// refreshKeys enables exactly the bindings the cursor can act on right now.
//
// It runs before dispatch as well as before drawing, so a key the status bar
// is hiding is inert in the same frame rather than the next one — every flag
// below stands in for a guard the handlers used to make inline.
func (m *appModel) refreshKeys() {
	onHeader, onRequest := false, false
	if it, ok := m.sidebar.current(); ok {
		onHeader = it.reqID == 0
		onRequest = it.reqID != 0
	}
	// Copy follows the selection rather than the cursor row: extending a range
	// past the end of a folder parks the cursor on the next header, and the
	// requests highlighted behind it are still what it would copy. With no
	// range open the selection is the cursor's row, so this is onRequest
	// again. Fold/OpenVars and OpenRequest/Delete never both answer one
	// keystroke, so it cannot make a pair ambiguous.
	hasSelection := m.sidebar.selectedCount() > 0

	m.keys.Fold.SetEnabled(onHeader)
	m.keys.OpenVars.SetEnabled(onHeader)
	m.keys.OpenRequest.SetEnabled(onRequest)
	m.keys.Copy.SetEnabled(hasSelection)
	// Enabled on any row, not just a request. extend anchors only on a
	// request, so from a header these merely move the cursor — and a key that
	// does nothing at all where the user first presses it is worse than one
	// that steps onto the row a range can start from. They are not in the
	// sidebar hint, so the wider enable costs no width.
	m.keys.ExtendUp.SetEnabled(len(m.sidebar.items) > 0)
	m.keys.ExtendDown.SetEnabled(len(m.sidebar.items) > 0)

	// Delete names its target in the hint, because 'D' on a folder header
	// takes the whole folder and the user has to know that before pressing
	// it, not after. Rebuilding the binding to say so follows Paste's count
	// and confirm's accept key.
	label := m.deleteLabel()
	m.keys.Delete = key.NewBinding(key.WithKeys("D"), key.WithHelp("D", label))
	m.keys.Delete.SetEnabled(label != "")
	// Only offered once a range exists, so it costs the hint nothing the rest
	// of the time.
	m.keys.ClearSelect.SetEnabled(m.sidebar.anchor != 0)

	// The paste hint carries the clipboard's size, which is what tells the
	// user what they copied without spending a pane on a clipboard indicator.
	// Rebuilding the binding to say so follows confirm's accept key, which is
	// rebuilt per confirmation to name that action's verb.
	m.keys.Paste = key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "paste "+countRequests(m.clip.len())),
	)
	m.keys.Paste.SetEnabled(!m.clip.empty() && len(m.sidebar.items) > 0)

	m.keys.ResetPath.SetEnabled(m.pathRowOverridden())

	m.keys.AbortSending.SetEnabled(m.sending)

	// Both are printable characters, so while a text field is open they have
	// to reach the field rather than opening an overlay.
	m.keys.Help.SetEnabled(!m.editor.Editing())
	m.keys.Workspace.SetEnabled(!m.editor.Editing())
}

// deleteLabel names what 'D' would remove where the cursor is, or "" when it
// would do nothing. It is the enable flag and the hint text at once, so the
// two cannot disagree about whether the key does anything.
//
// It mirrors openDeleteConfirm's branching: a non-empty selection wins, and
// only then does a header row count.
func (m *appModel) deleteLabel() string {
	if n := m.sidebar.selectedCount(); n == 1 {
		return "delete"
	} else if n > 1 {
		return fmt.Sprintf("delete %d", n)
	}
	it, ok := m.sidebar.current()
	if !ok || it.col == nil {
		return ""
	}
	if it.folder != "" {
		return "delete folder"
	}
	return "delete collection"
}

// pathRowOverridden reports whether the Path tab's cursor sits on a variable
// this request overrides — the only case where resetting does anything.
func (m *appModel) pathRowOverridden() bool {
	if m.focus != focusEditor || m.editor.tab != tabPath {
		return false
	}
	r := m.editor.request()
	if r == nil {
		return false
	}
	rows := pathRows(r, m.editor.layers(r))
	if m.editor.path.cursor >= len(rows) {
		return false
	}
	return rows[m.editor.path.cursor].source == "req"
}

// shortHelp is the status bar's hint: what the cursor can do here, most
// specific first. It excludes '?', which statusBar pins after it so a narrow
// terminal cannot truncate away the route to every other key.
//
// Cursor motion is deliberately absent too. It is self-evident in a TUI, and
// it would spend the width the context keys need; '?' lists it.
func (m *appModel) shortHelp() []key.Binding {
	k := &m.keys
	if m.sending {
		return []key.Binding{k.AbortSending}
	}
	if m.focus == focusEditor && m.editor.Editing() {
		return m.editingHelp()
	}

	switch m.focus {
	case focusSidebar:
		if len(m.sidebar.items) == 0 {
			return []key.Binding{k.NewCol}
		}
		return []key.Binding{k.Fold, k.OpenVars, k.OpenRequest, k.Copy, k.Paste, k.Delete, k.ClearSelect}
	case focusResponse:
		return []key.Binding{k.PrevRespTab, k.NextRespTab, k.Scroll}
	}

	if m.editor.request() == nil {
		return []key.Binding{k.NewReq}
	}
	tabKeys := map[int][]key.Binding{
		tabPath:    {k.EditPath, k.ResetPath},
		tabParams:  {k.EditName, k.EditValue, k.AddRow, k.DeleteRow, k.ToggleRow},
		tabHeaders: {k.EditName, k.EditValue, k.AddRow, k.DeleteRow, k.ToggleRow},
		tabAuth:    {k.CycleAuth, k.EditAuth},
		tabBody:    {k.EditBody, k.ExternalEditor, k.CycleBody},
	}
	return append(tabKeys[m.editor.tab], k.EditURL, k.CycleMethod, k.PrevTab, k.NextTab)
}

// editingHelp is the hint while one of the editor's text fields is open.
func (m *appModel) editingHelp() []key.Binding {
	k := &m.keys
	switch {
	case m.editor.editingBody:
		return []key.Binding{k.FinishBody}
	case m.editor.params.Editing() || m.editor.headers.Editing():
		return []key.Binding{k.SaveField, k.NextField, k.CancelField}
	default:
		return []key.Binding{k.SaveField, k.CancelField}
	}
}

// helpGroup is one section of the '?' overlay.
type helpGroup struct {
	name     string
	bindings []key.Binding
}

// helpGroups is the full reference, rendered from defaultKeys so it lists
// every key regardless of what the cursor currently has enabled.
func helpGroups() []helpGroup {
	k := defaultKeys
	return []helpGroup{
		{"Global", []key.Binding{k.Send, k.Env, k.History, k.Curl, k.Method, k.NewReq, k.NewCol, k.NextPane, k.PrevPane, k.Help, k.Quit}},
		{"Move", []key.Binding{k.Up, k.Down, k.Top, k.Bottom}},
		{"Sidebar", []key.Binding{k.Fold, k.OpenRequest, k.OpenVars, k.Copy, k.Paste, k.Delete}},
		{"Selecting several", []key.Binding{k.ExtendUp, k.ExtendDown, k.ClearSelect}},
		{"Editor", []key.Binding{k.EditURL, k.CycleMethod, k.PrevTab, k.NextTab}},
		{"Path tab", []key.Binding{k.EditPath, k.ResetPath}},
		{"Params & Headers", []key.Binding{k.EditName, k.EditValue, k.AddRow, k.DeleteRow, k.ToggleRow}},
		{"Auth tab", []key.Binding{k.CycleAuth, k.EditAuth}},
		{"Body tab", []key.Binding{k.EditBody, k.ExternalEditor, k.CycleBody, k.FinishBody}},
		{"Response", []key.Binding{k.PrevRespTab, k.NextRespTab, k.Scroll}},
		{"Editing a field", []key.Binding{k.SaveField, k.NextField, k.CancelField}},
		{"Modals", []key.Binding{k.Select, k.PickMethod, k.Accept, k.Deny, k.Close}},
	}
}

// helpOverlay builds the '?' reference. It is an overlayList so it inherits
// the box, the ANSI-aware truncation and the row windowing — which means it
// scrolls with j/k on a short terminal instead of clipping, and the cursor
// doubles as a scroll position marker.
func helpOverlay(width int) overlayList {
	// Matches overlayList.view: the box's Width includes its border (2) and
	// Padding(1, 2) sides (4).
	textW := max(20, min(width-8, 90)) - 6
	var lines []string
	for i, g := range helpGroups() {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, collectionStyle.Render(g.name))
		for _, row := range helpRows(textW-2, g.bindings) {
			lines = append(lines, "  "+row)
		}
	}
	return overlayList{title: "Keys", lines: lines, footer: []key.Binding{defaultKeys.Close}}
}
