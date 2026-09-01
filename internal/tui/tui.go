// Package tui is the interactive terminal interface: a three-pane layout
// with a collection sidebar, request editor, and response viewer.
package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"

	"github.com/kalkis/chilli-crisp/internal/curlgen"
	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/model"
	"github.com/kalkis/chilli-crisp/internal/store"
	"github.com/kalkis/chilli-crisp/internal/vars"
)

var (
	dimStyle    = lipgloss.NewStyle().Foreground(compat.AdaptiveColor{Light: lipgloss.Color("244"), Dark: lipgloss.Color("241")})
	cursorStyle = lipgloss.NewStyle().Reverse(true)
	// selectedStyle marks the rest of a shift-extended range. It is dimmer
	// than cursorStyle so the range reads as one span with a position in it
	// rather than as several cursors.
	selectedStyle   = lipgloss.NewStyle().Background(compat.AdaptiveColor{Light: lipgloss.Color("252"), Dark: lipgloss.Color("237")})
	collectionStyle = lipgloss.NewStyle().Bold(true)
	titleStyle      = lipgloss.NewStyle().Bold(true)
	activeTabStyle  = lipgloss.NewStyle().Bold(true).Underline(true)
	tabStyle        = dimStyle
	okStatusStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	errStatusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	paneStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	focusedPane     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62"))
	overlayStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)
	statusStyle     = dimStyle
)

var methodColors = map[string]string{
	"GET": "42", "POST": "214", "PUT": "39", "PATCH": "135", "DELETE": "196",
	// QUERY is safe like GET, so it reads as a sibling of it without being
	// confusable with it.
	httpclient.MethodQuery: "44",
}

func methodStyle(m string) lipgloss.Style {
	if c, ok := methodColors[strings.ToUpper(m)]; ok {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
	}
	return lipgloss.NewStyle().Bold(true)
}

// renderTabBar renders tab names with the active one highlighted.
func renderTabBar(names []string, active int) string {
	var tabs []string
	for i, name := range names {
		st := tabStyle
		if i == active {
			st = activeTabStyle
		}
		tabs = append(tabs, st.Render(name))
	}
	return strings.Join(tabs, " ")
}

type focusArea int

const (
	focusSidebar focusArea = iota
	focusEditor
	focusResponse
)

type uiMode int

const (
	modeNormal uiMode = iota
	modeEnvPicker
	modeWorkspace
	modeHistory
	modePrompt
	modeVars
	modeMethod
	modeConfirm
	modeHelp
)

// sel identifies the request loaded in the editor, independent of where its
// collection or request currently sits. The zero value selects nothing.
type sel struct{ col, req model.ID }

// responseMsg is delivered when an async send finishes. The workspace,
// collection name and environment are captured when the send starts, so
// history is attributed correctly even if the user switches context — or
// workspace — before the response arrives.
type responseMsg struct {
	resp    *httpclient.Response
	err     error
	req     model.Request
	ws      *store.Workspace
	colName string
	env     string
}

type appModel struct {
	ws   *store.Workspace
	cols []*model.Collection
	envs []*model.Environment

	// client carries the workspace's request timeout, so switching workspace
	// replaces it. startSend copies it into a local before handing it to the
	// send goroutine, which is what makes replacing it safe mid-flight.
	client *httpclient.Client

	envIdx int // index into envs; -1 = no environment

	focus focusArea
	mode  uiMode

	// keys is this model's copy of defaultKeys; refreshKeys toggles the
	// context-dependent bindings on it.
	keys keyMap

	sidebar  sidebar
	editor   editor
	response responseView

	// clip is the copy buffer 'c' fills and 'p' drains. Process-local by
	// design; see clipboard.
	clip clipboard

	envList  overlayList
	wsList   overlayList
	wsPaths  []string // workspace roots, parallel to wsList's rows
	histList overlayList
	helpList overlayList
	history  []store.HistoryEntry
	// historyMode is the workspace's recording level, read at load time the
	// same way the timeout is. It is shown, never enforced here: the log is
	// written by store.AppendHistory, which applies the setting itself.
	historyMode store.HistoryMode
	prompt      prompt
	varsEd      varsEditor
	methodPick  methodPicker
	confirm     confirm

	active sel // request currently loaded in the editor

	sending bool
	// cancel aborts the send in flight; nil while idle. Only one send may be
	// in flight, so it always refers to the current one.
	cancel context.CancelFunc
	status string
	width  int
	height int

	rightW, editorH, respH int // pane sizes, set by layout()
}

// newAppModel builds a model with the defaults every construction path needs:
// no environment selected, a fresh editor and response pane, and its own copy
// of the keymap — a zero keyMap has every binding disabled, so forgetting it
// would silently stop the TUI responding to keys at all.
func newAppModel(w *store.Workspace) *appModel {
	m := &appModel{keys: defaultKeys}
	m.resetWorkspaceState(w)
	return m
}

// resetWorkspaceState points the model at w and returns everything belonging
// to one workspace to its initial value. It is the single list of what a
// workspace owns, which is why both construction and switching go through it.
//
// What it deliberately leaves alone: the keymap; the clipboard, which is
// process-local and whose whole point is that a copied request can be pasted
// anywhere, including into another workspace; the terminal size; and
// sending/cancel, because a send in flight has to keep its abort working
// across a switch.
func (m *appModel) resetWorkspaceState(w *store.Workspace) {
	m.ws = w
	m.cols, m.envs, m.history = nil, nil, nil
	m.historyMode = store.HistoryFull
	m.envIdx = -1
	m.editor = newEditor()
	// Bound to m, so the Path tab always reads the environment selected now.
	// Rebinding is not optional: replacing the editor without it leaves the
	// Path tab silently showing no environment values at all.
	m.editor.envVars = m.envVars
	m.response = newResponseView()
	// Fold state is keyed by collection ID and IDs are handed out afresh on
	// every load, so it could not survive a reload in any case. Clearing also
	// drops the stale selection anchor.
	m.sidebar = sidebar{}
	m.active = sel{}
	m.focus = focusSidebar
	m.mode = modeNormal
}

// loadWorkspace re-points the model at w, reloading its collections,
// environments and request timeout.
//
// Everything fallible happens before any state changes, so a workspace that
// fails to load — unparseable YAML, a directory that has gone — leaves the TUI
// exactly where it was rather than half-moved.
func (m *appModel) loadWorkspace(w *store.Workspace) error {
	cols, err := w.Collections()
	if err != nil {
		return err
	}
	envs, err := w.Environments()
	if err != nil {
		return err
	}
	// Each workspace configures its own timeout, so the client is rebuilt
	// rather than carried across: the TUI and the runner are meant to agree on
	// which value is in force, and a stale client would break that quietly.
	timeout, err := w.RequestTimeout()
	if err != nil {
		return err
	}
	mode, err := w.HistoryMode()
	if err != nil {
		return err
	}
	m.resetWorkspaceState(w)
	m.cols, m.envs = cols, envs
	m.client = httpclient.New(timeout)
	m.historyMode = mode
	m.sidebar.rebuild(cols)
	if len(cols) > 0 && len(cols[0].Requests) > 0 {
		m.loadRequest(cols[0], cols[0].Requests[0].ID)
	}
	return nil
}

// Run starts the TUI on the given workspace.
func Run(w *store.Workspace) error {
	m := newAppModel(w)
	if err := m.loadWorkspace(w); err != nil {
		return err
	}
	_, err := tea.NewProgram(m).Run()
	return err
}

func (m *appModel) Init() tea.Cmd { return nil }

func (m *appModel) loadRequest(col *model.Collection, reqID model.ID) {
	m.active = sel{col: col.ID, req: reqID}
	m.editor.load(col, reqID)
	m.sidebar.selectRequest(m.active)
}

// collection resolves a collection by ID, or nil.
func (m *appModel) collection(id model.ID) *model.Collection {
	for _, c := range m.cols {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// activeCollection is the collection holding the request in the editor.
func (m *appModel) activeCollection() *model.Collection {
	return m.collection(m.active.col)
}

// save persists the active collection; failures surface in the status bar.
func (m *appModel) save() {
	m.saveCollection(m.activeCollection())
}

// saveCollection persists any collection — the variables modal may be editing
// one other than the one loaded in the editor.
func (m *appModel) saveCollection(col *model.Collection) {
	if col == nil {
		return
	}
	if err := m.ws.SaveCollection(col); err != nil {
		m.status = "save failed: " + err.Error()
	}
}

// envVars returns the selected environment's merged variables, or nil.
func (m *appModel) envVars() map[string]string {
	if m.envIdx >= 0 && m.envIdx < len(m.envs) {
		return m.envs[m.envIdx].AllVars()
	}
	return nil
}

// resolverFor builds r's variable stack in the one canonical order (col may
// be nil for a replay whose collection is gone).
func (m *appModel) resolverFor(r *model.Request, col *model.Collection) *vars.Resolver {
	return httpclient.ResolverFor(r, col, m.envVars())
}

func (m *appModel) envName() string {
	if m.envIdx >= 0 && m.envIdx < len(m.envs) {
		return m.envs[m.envIdx].Name
	}
	return ""
}

// envLabel is envName with a placeholder for display when no environment is
// selected.
func (m *appModel) envLabel() string {
	if name := m.envName(); name != "" {
		return name
	}
	return "(no env)"
}

// startSend launches req asynchronously against the given collection context
// (col may be nil for a replay whose collection no longer exists). Only one
// send may be in flight at a time.
func (m *appModel) startSend(req model.Request, col *model.Collection) tea.Cmd {
	if m.sending {
		m.status = "already sending…"
		return nil
	}
	m.sending = true
	// The status bar's send note already says so, with the cancel hint.
	m.status = ""
	// Clone so the goroutine (and the history snapshot) never shares Auth,
	// Body, or param arrays with the request the editor keeps mutating.
	req = req.Clone()
	colName := ""
	if col != nil {
		colName = col.Name
	}
	env := m.envName()
	res := m.resolverFor(&req, col)
	// The timeout lives on the client; the context carries only cancellation,
	// so a user abort (context.Canceled) stays distinguishable from a request
	// that simply ran out of time.
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	// Both are read now rather than when the response lands: the workspace may
	// have been switched by then, and this send's history belongs to the one
	// it was started from.
	client, ws := m.client, m.ws
	return func() tea.Msg {
		resp, err := client.Send(ctx, &req, col, res)
		return responseMsg{resp: resp, err: err, req: req, ws: ws, colName: colName, env: env}
	}
}

// cancelSend aborts the send in flight, if there is one. The goroutine still
// delivers its responseMsg, so no extra state machine is needed: sending stays
// true until it arrives.
func (m *appModel) cancelSend() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	m.status = "cancelling…"
}

func (m *appModel) sendActive() tea.Cmd {
	r := m.editor.request()
	if r == nil {
		m.status = "nothing to send"
		return nil
	}
	return m.startSend(*r, m.activeCollection())
}

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		// The reference wraps its groups to the width it was built at, so a
		// resize has to rebuild it rather than just re-truncate.
		if m.mode == modeHelp {
			cursor := m.helpList.cursor
			m.helpList = helpOverlay(m.width)
			m.helpList.cursor = min(cursor, max(0, len(m.helpList.lines)-1))
		}
		return m, nil

	case responseMsg:
		m.sending = false
		m.status = ""
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		// An aborted send is not a result: no history entry, and the response
		// pane keeps whatever it was already showing.
		if errors.Is(msg.err, context.Canceled) {
			m.status = "send cancelled"
			return m, nil
		}
		if msg.err != nil {
			m.response.SetText(errStatusStyle.Render("request failed"), msg.err.Error())
		} else {
			m.response.SetResponse(msg.resp)
			m.focus = focusResponse
		}
		_ = msg.ws.AppendHistory(store.NewHistoryEntry(msg.colName, msg.env, msg.req, msg.resp, msg.err))
		return m, nil

	case editorExternalMsg:
		dirty, err := m.editor.finishExternalEdit(msg)
		if err != nil {
			m.status = "external editor: " + err.Error()
		} else if dirty {
			m.save()
			m.status = "body updated"
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *appModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	// Before dispatch, not just before drawing: a key the status bar is
	// hiding must be inert in this frame, not the next one.
	m.refreshKeys()

	switch m.mode {
	case modeEnvPicker:
		return m.handleEnvPickerKey(msg)
	case modeWorkspace:
		return m.handleWorkspacePickerKey(msg)
	case modeHistory:
		return m.handleHistoryKey(msg)
	case modePrompt:
		return m.handlePromptKey(msg)
	case modeVars:
		return m.handleVarsKey(msg)
	case modeMethod:
		return m.handleMethodKey(msg)
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modeHelp:
		return m.handleHelpKey(msg)
	}

	// While a text field is being edited, keys go straight to the editor.
	if m.focus == focusEditor && m.editor.Editing() {
		cmd, dirty := m.editor.update(msg, &m.keys)
		if dirty {
			m.save()
			m.sidebar.rebuild(m.cols)
		}
		return m, cmd
	}

	// AbortSending is only enabled while a send is in flight; otherwise esc
	// falls through to the focused pane as before.
	if key.Matches(msg, m.keys.AbortSending) {
		m.cancelSend()
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.NextPane):
		m.focus = (m.focus + 1) % 3
		return m, nil
	case key.Matches(msg, m.keys.PrevPane):
		m.focus = (m.focus + 2) % 3
		return m, nil
	case key.Matches(msg, m.keys.Send):
		return m, m.sendActive()
	case key.Matches(msg, m.keys.Env):
		m.openEnvPicker()
		return m, nil
	case key.Matches(msg, m.keys.Workspace):
		m.openWorkspacePicker()
		return m, nil
	case key.Matches(msg, m.keys.History):
		return m, m.openHistory()
	case key.Matches(msg, m.keys.Curl):
		m.showCurl()
		return m, nil
	case key.Matches(msg, m.keys.Help):
		m.helpList = helpOverlay(m.width)
		m.mode = modeHelp
		return m, nil
	case key.Matches(msg, m.keys.Method):
		r := m.editor.request()
		if r == nil {
			m.status = "no request selected"
			return m, nil
		}
		m.openMethodPicker(r.Method, "")
		return m, nil
	case key.Matches(msg, m.keys.NewCol):
		m.mode = modePrompt
		m.prompt = newPrompt("New collection name", "new-collection", "")
		return m, textinput.Blink
	case key.Matches(msg, m.keys.NewReq):
		if len(m.cols) == 0 {
			m.status = "create a collection first ('N')"
			return m, nil
		}
		m.mode = modePrompt
		m.prompt = newPrompt("New request name", "new-request", "")
		return m, textinput.Blink
	}

	switch m.focus {
	case focusSidebar:
		return m.handleSidebarKey(msg)
	case focusEditor:
		cmd, dirty := m.editor.update(msg, &m.keys)
		if dirty {
			m.save()
			m.sidebar.rebuild(m.cols)
		}
		return m, cmd
	case focusResponse:
		return m, m.response.update(msg, &m.keys)
	}
	return m, nil
}

// handleHelpKey drives the '?' reference. It is read-only, so anything that
// is not navigation closes it.
func (m *appModel) handleHelpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close, m.keys.Help, m.keys.Quit):
		m.mode = modeNormal
	default:
		m.helpList.update(msg, &m.keys)
	}
	return m, nil
}

// handleSidebarKey routes a key in the sidebar. The row the cursor sits on
// decides which bindings refreshKeys left enabled, so Fold/OpenVars and
// OpenRequest/Delete are never both live despite sharing keystrokes.
func (m *appModel) handleSidebarKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	it, ok := m.sidebar.current()
	switch {
	case !ok:
		m.sidebar.update(msg, &m.keys)
	case key.Matches(msg, m.keys.Fold):
		m.sidebar.toggleFold(m.cols)
	case key.Matches(msg, m.keys.OpenVars):
		// A collection or folder header: edit that scope's variables.
		m.varsEd = newVarsEditor(it.col, it.folder)
		m.mode = modeVars
	case key.Matches(msg, m.keys.OpenRequest):
		m.loadRequest(it.col, it.reqID)
		m.focus = focusEditor
	case key.Matches(msg, m.keys.Copy):
		m.copySelection()
	case key.Matches(msg, m.keys.Paste):
		m.pasteClipboard()
	case key.Matches(msg, m.keys.ClearSelect):
		m.sidebar.clearSelection()
	case key.Matches(msg, m.keys.Delete):
		m.openDeleteConfirm()
	default:
		m.sidebar.update(msg, &m.keys)
	}
	return m, nil
}

func (m *appModel) openEnvPicker() {
	lines := []string{"(no environment)"}
	for _, e := range m.envs {
		lines = append(lines, e.Name)
	}
	m.envList = overlayList{title: "Environment", lines: lines, cursor: m.envIdx + 1}
	m.mode = modeEnvPicker
}

func (m *appModel) handleEnvPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		m.mode = modeNormal
	case key.Matches(msg, m.keys.Select):
		m.envIdx = m.envList.cursor - 1
		m.mode = modeNormal
		m.status = "environment: " + m.envLabel()
	default:
		m.envList.update(msg, &m.keys)
	}
	return m, nil
}

// openPathEntry is the picker's last row: not a workspace, but the way to
// name one the list does not know about.
const openPathEntry = "Open a path…"

// shortWorkspace renders a workspace path as its last two segments, which is
// what distinguishes one .crisp from another when several are listed.
func shortWorkspace(path string) string {
	short := filepath.Base(path)
	if parent := filepath.Base(filepath.Dir(path)); parent != "." && parent != string(filepath.Separator) {
		short = filepath.Join(parent, short)
	}
	return short
}

// knownWorkspaces lists the workspaces the picker offers: the one open now,
// the home workspace, and whatever a search from the current directory finds.
// Without a registry that is the whole set — a later one would feed this same
// list more entries and change nothing else.
func (m *appModel) knownWorkspaces() (paths, labels []string) {
	add := func(path, note string) {
		if path == "" || slices.Contains(paths, path) {
			return
		}
		paths = append(paths, path)
		labels = append(labels, fmt.Sprintf("%s  (%s)", shortWorkspace(path), note))
	}
	add(m.ws.Root, "current")
	// Open is the existence check: it is exactly the question of whether that
	// path is a workspace worth offering.
	if home, err := store.HomeWorkspacePath(); err == nil {
		if _, err := store.Open(home); err == nil {
			add(home, "home")
		}
	}
	if w, ok := store.Discover("."); ok {
		add(w.Root, "here")
	}
	return paths, labels
}

func (m *appModel) openWorkspacePicker() {
	paths, labels := m.knownWorkspaces()
	m.wsPaths = paths
	m.wsList = overlayList{title: "Workspace", lines: append(labels, openPathEntry)}
	m.mode = modeWorkspace
}

func (m *appModel) handleWorkspacePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		m.mode = modeNormal
	case key.Matches(msg, m.keys.Select):
		// The last row is the escape hatch rather than a workspace, so it is
		// the one cursor position that has no entry in wsPaths.
		if m.wsList.cursor >= len(m.wsPaths) {
			m.mode = modePrompt
			m.prompt = newPrompt("Workspace path", "open-workspace", m.ws.Root)
			return m, textinput.Blink
		}
		m.switchWorkspace(m.wsPaths[m.wsList.cursor])
	default:
		m.wsList.update(msg, &m.keys)
	}
	return m, nil
}

// switchWorkspace re-points the model at the workspace at path. Every failure
// leaves the current workspace in place and says why in the status bar, so a
// mistyped path costs nothing.
func (m *appModel) switchWorkspace(path string) {
	m.mode = modeNormal
	w, err := store.Open(path)
	if err != nil {
		m.status = "workspace: " + err.Error()
		return
	}
	if w.Root == m.ws.Root {
		m.status = "already on " + shortWorkspace(w.Root)
		return
	}
	if err := m.loadWorkspace(w); err != nil {
		m.status = "workspace: " + err.Error()
		return
	}
	m.status = "workspace: " + shortWorkspace(w.Root)
}

// historyTitle names the history overlay, saying so when the workspace is
// recording less than everything. Without it a log that never fills up looks
// like a bug rather than the setting it is — this overlay is the only place
// the recording level is visible from inside the TUI.
func historyTitle(mode store.HistoryMode) string {
	switch mode {
	case store.HistoryOff:
		return "History (recording is off)"
	case store.HistoryMetadata:
		return "History (response bodies are not recorded)"
	default:
		return "History"
	}
}

func (m *appModel) openHistory() tea.Cmd {
	entries, err := m.ws.History(200)
	if err != nil {
		m.status = "history: " + err.Error()
		return nil
	}
	m.history = entries
	var lines []string
	for _, e := range entries {
		lines = append(lines, historyLine(e))
	}
	m.histList = overlayList{
		title:  historyTitle(m.historyMode),
		lines:  lines,
		footer: []key.Binding{defaultKeys.Replay, defaultKeys.Close},
	}
	m.mode = modeHistory
	return nil
}

func (m *appModel) handleHistoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		m.mode = modeNormal
		return m, nil
	case key.Matches(msg, m.keys.Replay):
		if m.histList.cursor < len(m.history) {
			e := m.history[m.histList.cursor]
			m.mode = modeNormal
			// Replay against the entry's collection if it still exists, so
			// collection vars and default auth resolve as they did before.
			var col *model.Collection
			for _, c := range m.cols {
				if c.Name == e.Collection {
					col = c
					break
				}
			}
			return m, m.startSend(e.Request, col)
		}
		return m, nil
	default:
		m.histList.update(msg, &m.keys)
		return m, nil
	}
}

func (m *appModel) handlePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		m.mode = modeNormal
		return m, nil
	case key.Matches(msg, m.keys.Submit):
		name := strings.TrimSpace(m.prompt.input.Value())
		m.mode = modeNormal
		if name == "" {
			return m, nil
		}
		switch m.prompt.action {
		case "new-collection":
			col := &model.Collection{Name: name}
			col.EnsureIDs()
			if err := m.ws.SaveCollection(col); err != nil {
				m.status = "save failed: " + err.Error()
				return m, nil
			}
			m.cols = append(m.cols, col)
			m.sidebar.rebuild(m.cols)
			m.status = "created collection " + name
		case "new-request":
			// Nothing is written until the method is chosen, so an esc at
			// that step leaves no half-made request behind.
			m.openMethodPicker("GET", name)
		case "open-workspace":
			m.switchWorkspace(name)
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.prompt.input, cmd = m.prompt.input.Update(msg)
		return m, cmd
	}
}

func (m *appModel) showCurl() {
	r := m.editor.request()
	if r == nil {
		m.status = "nothing to export"
		return
	}
	col := m.activeCollection()
	resolved, err := httpclient.Resolve(r, col, m.resolverFor(r, col))
	if err != nil {
		m.response.SetText(errStatusStyle.Render("cannot build curl"), err.Error())
		return
	}
	m.response.SetText(titleStyle.Render("curl"), highlight(curlgen.Command(resolved), "bash"))
	m.focus = focusResponse
}

const sidebarW = 30

// layout computes the pane sizes render draws with and resizes the inner
// components to match. Width/Height include the border in lipgloss v2, so a
// pane's content area is 2 smaller than the size render draws it with.
func (m *appModel) layout() {
	contentH := m.height - 2 // minus status bar
	m.rightW = max(20, m.width-sidebarW-4)
	m.editorH = max(8, contentH*2/5)
	m.respH = max(4, contentH-m.editorH-4)
	m.editor.setSize(m.rightW-2, m.editorH-2)
	m.response.setSize(m.rightW-2, m.respH-2)
}

// View declares the frame and the alt-screen state.
func (m *appModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *appModel) render() string {
	if m.width == 0 {
		return "loading…"
	}
	// Idempotent, and cheap: it only sets flags. Repeating it here means the
	// first frame is right before any key has arrived.
	m.refreshKeys()
	switch m.mode {
	case modeEnvPicker:
		return m.envList.view(m.width, m.height)
	case modeWorkspace:
		return m.wsList.view(m.width, m.height)
	case modeHistory:
		return m.histList.view(m.width, m.height)
	case modePrompt:
		return m.prompt.view(m.width, m.height)
	case modeVars:
		return m.varsEd.view(m.width, m.height)
	case modeMethod:
		return m.methodPick.list.view(m.width, m.height)
	case modeConfirm:
		return m.confirm.view(m.width, m.height)
	case modeHelp:
		return m.helpList.view(m.width, m.height)
	}

	contentH := m.height - 2 // minus status bar

	pane := func(s lipgloss.Style, w, h int, content string) string {
		return s.Width(w).Height(h).MaxWidth(w + 2).Render(content)
	}
	style := func(f focusArea) lipgloss.Style {
		if m.focus == f {
			return focusedPane
		}
		return paneStyle
	}

	left := pane(style(focusSidebar), sidebarW, contentH-2,
		m.sidebar.view(m.cols, sidebarW-2, contentH-4, m.focus == focusSidebar, m.active))

	top := pane(style(focusEditor), m.rightW, m.editorH, m.editor.view(m.focus == focusEditor))
	bottom := pane(style(focusResponse), m.rightW, m.respH, m.response.view())
	right := lipgloss.JoinVertical(lipgloss.Left, top, bottom)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	return lipgloss.JoinVertical(lipgloss.Left, body, m.statusBar())
}

// statusBar draws the environment label and status message on the left and
// the context hint on the right. The two halves are rendered apart because
// helpLine carries its own styling — wrapping the pair in statusStyle would
// reset the hint's colours mid-line.
func (m *appModel) statusBar() string {
	sendNote := ""
	if m.sending {
		sendNote = "  ⏳ sending… (esc cancels)"
	}
	left := statusStyle.Render(fmt.Sprintf(" env: %s%s  %s", m.envLabel(), sendNote, m.status))
	leftW := lipgloss.Width(left)
	// Truncating the hint is how the bar fits a narrow terminal; the left
	// half is never cut, so it keeps at least a column of its own.
	avail := max(0, m.width-leftW-2)

	// '?' is pinned rather than truncated along with the rest: the bar shows
	// only what the cursor can do, so it is the route to every other key and
	// losing it on a narrow terminal would strand the user. It renders empty
	// while a text field is open, where '?' has to be typed instead.
	sep := dimStyle.Render(" · ")
	tail := helpLine(avail, m.keys.Help)
	hint := helpLine(avail-lipgloss.Width(tail)-lipgloss.Width(sep), m.shortHelp()...)
	switch {
	case hint == "":
		hint = tail
	case tail != "":
		hint += sep + tail
	}

	gap := max(1, m.width-leftW-lipgloss.Width(hint)-1)
	return left + strings.Repeat(" ", gap) + hint
}

// handleVarsKey drives the collection/folder variables modal. esc closes it,
// except while a field is being edited, where kvEditor claims esc to abandon
// that field.
func (m *appModel) handleVarsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c":
		return m, tea.Quit
	case key.Matches(msg, m.keys.Close) && !m.varsEd.kv.Editing():
		m.mode = modeNormal
		return m, nil
	}
	cmd, changed := m.varsEd.update(msg, &m.keys)
	if changed {
		m.saveCollection(m.varsEd.col)
	}
	return m, cmd
}

// openMethodPicker shows the method list. A non-empty pendingName means the
// chosen method will create that request rather than retitle the loaded one.
func (m *appModel) openMethodPicker(current, pendingName string) {
	m.methodPick = newMethodPicker(current, pendingName)
	m.mode = modeMethod
}

// handleMethodKey drives the method picker. esc closes it, abandoning a
// pending creation, which has written nothing.
func (m *appModel) handleMethodKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c":
		return m, tea.Quit
	case key.Matches(msg, m.keys.Close):
		m.mode = modeNormal
		return m, nil
	}
	method := m.methodPick.choose(msg, &m.keys)
	if method == "" {
		m.methodPick.list.update(msg, &m.keys)
		return m, nil
	}
	m.mode = modeNormal
	if name := m.methodPick.pendingName; name != "" {
		m.createRequest(name, method)
		return m, nil
	}
	r := m.editor.request()
	if r == nil {
		m.status = "no request selected"
		return m, nil
	}
	// No editor reload: the Body tab reads r.Body live, and a body seeded
	// for QUERY starts empty, which is what the textarea already shows.
	applyMethod(r, method)
	m.save()
	return m, nil
}

// createRequest appends a named request to whichever collection the sidebar
// points at and opens it in the editor.
func (m *appModel) createRequest(name, method string) {
	col := m.activeCollection()
	if it, ok := m.sidebar.current(); ok {
		col = it.col
	}
	if col == nil {
		if len(m.cols) == 0 {
			return
		}
		col = m.cols[0]
	}
	r := model.Request{Name: name}
	applyMethod(&r, method)
	reqID := col.Append(r)
	if err := m.ws.SaveCollection(col); err != nil {
		m.status = "save failed: " + err.Error()
		return
	}
	m.sidebar.rebuild(m.cols)
	m.loadRequest(col, reqID)
	m.focus = focusEditor
}

// maxConfirmDetail caps how many requests a confirmation lists by name. A
// shift-selected range has no upper bound, and the modal is centred on the
// terminal, so past a handful the list has to give way to a count.
const maxConfirmDetail = 6

// openDeleteConfirm asks before destroying whatever 'D' would remove. Deleting
// has no undo, which is why this is the one action that asks; a param or
// header row deleted with 'd' is cheap to retype, and confirming each of those
// would make the editor unusable.
//
// A non-empty selection always wins, and that is what stops a range
// escalating. Extending one across a folder boundary sweeps up the header
// between the two runs; if that header counted, D would take the whole folder,
// including requests below the range that were never highlighted and may be
// off screen. So headers are actionable only when the cursor is parked on one
// with no range open — "delete the thing under the cursor".
//
// A range that happens to cover a folder header and all of its requests still
// empties the folder, and PruneFolders then drops its entry, so the two routes
// agree without the header ever needing to count.
func (m *appModel) openDeleteConfirm() {
	if items := m.sidebar.selected(); len(items) > 0 {
		m.confirmDeleteRequests(items)
		return
	}
	it, ok := m.sidebar.current()
	if !ok || it.col == nil {
		return
	}
	if it.folder != "" {
		m.confirmDeleteFolder(it.col, it.folder)
		return
	}
	m.confirmDeleteCollection(it.col)
}

// confirmDeleteRequests asks about a set of individual requests.
func (m *appModel) confirmDeleteRequests(items []sbItem) {
	var targets []sel
	var detail []string
	for _, it := range items {
		r := m.sidebar.requestFor(it)
		if r == nil {
			continue
		}
		targets = append(targets, sel{col: it.col.ID, req: it.reqID})
		if len(detail) < maxConfirmDetail {
			detail = append(detail,
				methodStyle(r.Method).Render(fmt.Sprintf("%-7s", r.Method))+" "+r.QualifiedName())
		}
	}
	if len(targets) == 0 {
		return
	}
	if extra := len(targets) - len(detail); extra > 0 {
		detail = append(detail, dimStyle.Render(fmt.Sprintf("…and %d more", extra)))
	} else if len(targets) == 1 {
		// Room to spare, and knowing which collection is about to be
		// rewritten matters when two of them hold a similar request.
		detail = append(detail, dimStyle.Render(items[0].col.Name))
	}

	title := "Delete request?"
	verb := "delete"
	if len(targets) > 1 {
		title = fmt.Sprintf("Delete %s?", countRequests(len(targets)))
		verb = fmt.Sprintf("delete %d", len(targets))
	}
	m.confirm = confirm{
		title:   title,
		detail:  detail,
		accept:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", verb)),
		action:  "delete-request",
		targets: targets,
	}
	m.mode = modeConfirm
}

// confirmDeleteFolder asks about a folder and everything in it.
//
// Its requests are resolved now rather than when the answer arrives, so the
// delete reuses deleteRequests unchanged and the modal cannot be answered
// against a collection that has since moved underneath it.
func (m *appModel) confirmDeleteFolder(col *model.Collection, folder string) {
	var targets []sel
	for i := range col.Requests {
		if col.Requests[i].Folder == folder {
			targets = append(targets, sel{col: col.ID, req: col.Requests[i].ID})
		}
	}
	m.confirm = confirm{
		title: "Delete folder?",
		detail: []string{
			collectionStyle.Render(folder),
			countRequests(len(targets)),
			dimStyle.Render(col.Name),
		},
		accept:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "delete folder")),
		action:  "delete-folder",
		targets: targets,
		col:     col.ID,
		folder:  folder,
	}
	m.mode = modeConfirm
}

// confirmDeleteCollection asks about a whole collection.
//
// This is the only action in the TUI that removes a file, so the modal names
// the file: it is the one place the user can see what is about to go before
// answering, and a collection's size alone does not convey that this reaches
// the filesystem.
func (m *appModel) confirmDeleteCollection(col *model.Collection) {
	detail := []string{
		collectionStyle.Render(col.Name),
		collectionSummary(col),
	}
	if col.Path != "" {
		detail = append(detail, dimStyle.Render(m.workspacePath(col.Path)))
	}
	m.confirm = confirm{
		title:  "Delete collection?",
		detail: detail,
		accept: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "delete collection")),
		action: "delete-collection",
		col:    col.ID,
	}
	m.mode = modeConfirm
}

// collectionSummary is a collection's size in one line, so a confirmation can
// convey how much is about to go.
func collectionSummary(c *model.Collection) string {
	out := countRequests(len(c.Requests))
	folders := map[string]bool{}
	for i := range c.Requests {
		if f := c.Requests[i].Folder; f != "" {
			folders[f] = true
		}
	}
	switch n := len(folders); {
	case n == 1:
		out += ", 1 folder"
	case n > 1:
		out += fmt.Sprintf(", %d folders", n)
	}
	return out
}

// workspacePath renders a file the way the docs talk about it
// (".crisp/collections/api.yaml"), so a modal can name a file without spending
// its width on an absolute path.
func (m *appModel) workspacePath(path string) string {
	rel, err := filepath.Rel(filepath.Dir(m.ws.Root), path)
	if err != nil {
		return path
	}
	return rel
}

// handleConfirmKey drives a confirmation modal. Only 'y' accepts; anything
// that is not an explicit answer leaves the modal open rather than falling
// through to an action the user did not ask for.
func (m *appModel) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c":
		return m, tea.Quit
	case key.Matches(msg, m.keys.Deny):
		m.mode = modeNormal
	case key.Matches(msg, m.keys.Accept):
		m.mode = modeNormal
		switch m.confirm.action {
		case "delete-request":
			m.deleteRequests(m.confirm.targets)
		case "delete-folder":
			m.deleteFolder(m.confirm.folder, m.confirm.targets)
		case "delete-collection":
			m.deleteCollection(m.confirm.col)
		}
	}
	return m, nil
}

// removeRequests deletes each target and persists every collection it touched
// once, reporting how many went, the name of the last one, and whether all the
// saves succeeded.
//
// Requests are removed by ID, one lookup at a time, so removing one does not
// shift the indices the rest resolve through — the reason a range may span
// collections safely, and the reason this cannot be a single index sweep.
func (m *appModel) removeRequests(targets []sel) (n int, lastName string, saved bool) {
	var touched []*model.Collection
	for _, t := range targets {
		col := m.collection(t.col)
		if col == nil {
			continue
		}
		i := col.FindRequest(t.req)
		if i < 0 {
			continue
		}
		lastName = col.Requests[i].Name
		col.Requests = append(col.Requests[:i], col.Requests[i+1:]...)
		n++
		if !slices.Contains(touched, col) {
			touched = append(touched, col)
		}
		// Any other selection still resolves by ID; only the deleted request
		// needs clearing.
		if m.active == t {
			m.active = sel{}
			m.editor.load(col, 0)
		}
	}

	saved = true
	for _, col := range touched {
		// A folder exists only because its requests do, so emptying one has
		// to take its vars entry with it — whether that happened by deleting
		// the folder or by deleting the last request in it.
		col.PruneFolders()
		if err := m.ws.SaveCollection(col); err != nil {
			m.status = "save failed: " + err.Error()
			saved = false
			break
		}
	}
	return n, lastName, saved
}

// afterDelete puts the sidebar back in a consistent state.
func (m *appModel) afterDelete() {
	m.sidebar.clearSelection()
	m.sidebar.rebuild(m.cols)
}

// deleteRequests removes the selected requests.
func (m *appModel) deleteRequests(targets []sel) {
	n, lastName, saved := m.removeRequests(targets)
	if n == 0 {
		return
	}
	if saved {
		if n == 1 {
			m.status = "deleted " + lastName
		} else {
			m.status = "deleted " + countRequests(n)
		}
	}
	m.afterDelete()
}

// deleteFolder removes a folder's requests; PruneFolders inside removeRequests
// takes the folder's own entry with them.
func (m *appModel) deleteFolder(folder string, targets []sel) {
	n, _, saved := m.removeRequests(targets)
	if saved {
		m.status = fmt.Sprintf("deleted folder %s (%s)", folder, countRequests(n))
	}
	m.afterDelete()
}

// deleteCollection removes a collection and its file.
//
// The file goes first: if the removal fails the collection stays visible and
// the error reaches the status bar, rather than disappearing from the sidebar
// while still on disk to return at the next start.
func (m *appModel) deleteCollection(colID model.ID) {
	col := m.collection(colID)
	if col == nil {
		return
	}
	if err := m.ws.DeleteCollection(col); err != nil {
		m.status = "delete failed: " + err.Error()
		return
	}
	m.cols = slices.DeleteFunc(m.cols, func(c *model.Collection) bool { return c == col })
	// The editor resolves by ID and would degrade to nil anyway, but its
	// collection pointer would keep the deleted collection alive.
	if m.active.col == colID {
		m.active = sel{}
		m.editor.load(nil, 0)
	}
	m.status = "deleted collection " + col.Name
	m.afterDelete()
}

// copySelection fills the clipboard from the sidebar selection.
func (m *appModel) copySelection() {
	var reqs []*model.Request
	for _, it := range m.sidebar.selected() {
		if r := m.sidebar.requestFor(it); r != nil {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) == 0 {
		return
	}
	m.clip.copyRequests(reqs)
	m.status = "copied " + countRequests(len(reqs))
}

// resolvePasteTarget works out where a paste lands from the cursor's row.
//
// The insert index is always inside the target folder's contiguous run, never
// at the end of the collection: a folder is a run of requests naming it rather
// than a container, so a request placed outside the run opens a second one and
// the folder is drawn twice.
func (m *appModel) resolvePasteTarget() (pasteTarget, bool) {
	it, ok := m.sidebar.current()
	if !ok || it.col == nil {
		return pasteTarget{}, false
	}
	switch {
	case it.reqID != 0:
		// On a request: directly after it, inheriting its folder. This is the
		// duplicate-in-place case.
		i := it.col.FindRequest(it.reqID)
		if i < 0 {
			return pasteTarget{}, false
		}
		return pasteTarget{col: it.col, folder: it.col.Requests[i].Folder, at: i + 1}, true
	case it.folder != "":
		// On a folder header: the end of that folder's run.
		end := folderRunEnd(it.col, it.folder)
		if end < 0 {
			return pasteTarget{}, false
		}
		return pasteTarget{col: it.col, folder: it.folder, at: end}, true
	default:
		// On a collection header: the top of the collection, outside any
		// folder.
		return pasteTarget{col: it.col, folder: "", at: 0}, true
	}
}

// pasteClipboard inserts the clipboard into the folder the cursor is in.
//
// Unlike createRequest this leaves focus in the sidebar and does not load the
// copy into the editor: paste is an organising action on the tree, and taking
// over the editor would throw away the user's current view — badly so when
// several requests were pasted and only one could be shown.
func (m *appModel) pasteClipboard() {
	if m.clip.empty() {
		return
	}
	t, ok := m.resolvePasteTarget()
	if !ok {
		return
	}
	reqs := m.clip.requests()
	var first model.ID
	for i, r := range reqs {
		r.Folder = t.folder
		r.Name = store.UniqueRequestName(t.col, r.Name)
		id := t.col.InsertAt(t.at+i, r)
		if i == 0 {
			first = id
		}
	}
	if err := m.ws.SaveCollection(t.col); err != nil {
		m.status = "save failed: " + err.Error()
		return
	}
	m.status = "pasted " + countRequests(len(reqs))
	m.sidebar.clearSelection()
	m.sidebar.rebuild(m.cols)
	m.sidebar.selectRequest(sel{col: t.col.ID, req: first})
}
