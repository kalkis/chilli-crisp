package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
	"github.com/kalkis/chilli-crisp/internal/model"
)

// methods is the set the editor offers, and the order 'm' cycles through.
// QUERY (RFC 10008) is appended rather than slotted next to GET so that
// cycling GET -> POST stays one keystroke.
var methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", httpclient.MethodQuery}

const (
	tabPath = iota
	tabParams
	tabHeaders
	tabAuth
	tabBody
	tabCount
)

var tabNames = []string{"Path", "Params", "Headers", "Auth", "Body"}

// editorExternalMsg carries the body text back from an external $EDITOR run.
type editorExternalMsg struct {
	path string
	err  error
}

// editor is the request pane: URL bar plus Params/Headers/Auth/Body tabs.
// It edits the request in place (a pointer into the active collection); the
// root model persists after any change.
type editor struct {
	col   *model.Collection
	reqID model.ID

	tab        int
	urlIn      textinput.Model
	editingURL bool

	// envVars supplies the selected environment's variables, so the Path tab
	// can show which layer a value comes from. Nil is treated as no
	// environment, which keeps the model usable in tests.
	envVars func() map[string]string

	path        pathEditor
	params      kvEditor
	headers     kvEditor
	auth        authEditor
	body        textarea.Model
	editingBody bool

	width int
}

func newEditor() editor {
	urlIn := textinput.New()
	urlIn.Prompt = ""
	body := textarea.New()
	body.ShowLineNumbers = false
	return editor{
		urlIn:   urlIn,
		path:    newPathEditor(),
		params:  newKVEditor(),
		headers: newKVEditor(),
		auth:    newAuthEditor(),
		body:    body,
	}
}

// request resolves the loaded request by ID, so it survives requests around
// it being added, removed, or reordered. It returns nil once the request is
// gone.
func (e *editor) request() *model.Request {
	if e.col == nil {
		return nil
	}
	i := e.col.FindRequest(e.reqID)
	if i < 0 {
		return nil
	}
	return &e.col.Requests[i]
}

// layers is the variable precedence stack for r, in the same order as
// httpclient.ResolverFor — the Path tab reports values against it.
func (e *editor) layers(r *model.Request) varLayers {
	l := varLayers{req: r.Vars}
	if e.envVars != nil {
		l.env = e.envVars()
	}
	if e.col != nil {
		l.col = e.col.Vars
		l.folder = e.col.FolderVars(r.Folder)
	}
	return l
}

func (e *editor) load(col *model.Collection, reqID model.ID) {
	e.col = col
	e.reqID = reqID
	e.editingURL = false
	e.editingBody = false
	e.path = newPathEditor()
	r := e.request()
	if r == nil {
		return
	}
	e.urlIn.SetValue(r.URL)
	e.params.load(r.Query)
	e.headers.load(r.Headers)
	e.auth.load()
	if r.Body != nil {
		e.body.SetValue(r.Body.Content)
	} else {
		e.body.SetValue("")
	}
}

func (e *editor) Editing() bool {
	return e.editingURL || e.editingBody || e.path.Editing() || e.params.Editing() || e.headers.Editing() || e.auth.Editing()
}

// update handles a key when the editor pane is focused. dirty reports that
// the request changed and must be saved.
func (e *editor) update(msg tea.KeyPressMsg, km *keyMap) (tea.Cmd, bool) {
	r := e.request()
	if r == nil {
		return nil, false
	}

	if e.editingURL {
		switch {
		case key.Matches(msg, km.SaveField):
			r.URL = e.urlIn.Value()
			e.editingURL = false
			e.urlIn.Blur()
			return nil, true
		case key.Matches(msg, km.CancelField):
			e.urlIn.SetValue(r.URL)
			e.editingURL = false
			e.urlIn.Blur()
			return nil, false
		default:
			var cmd tea.Cmd
			e.urlIn, cmd = e.urlIn.Update(msg)
			return cmd, false
		}
	}

	if e.editingBody {
		switch {
		case key.Matches(msg, km.FinishBody):
			e.editingBody = false
			e.body.Blur()
			return nil, e.syncBody()
		default:
			var cmd tea.Cmd
			e.body, cmd = e.body.Update(msg)
			return cmd, false
		}
	}

	// Global keys apply unless the current tab has a field editor open (the
	// tab editors handle their own editing state internally).
	if !e.tabEditing() {
		switch {
		case key.Matches(msg, km.PrevTab):
			e.tab = (e.tab + tabCount - 1) % tabCount
			return nil, false
		case key.Matches(msg, km.NextTab):
			e.tab = (e.tab + 1) % tabCount
			return nil, false
		case key.Matches(msg, km.EditURL):
			e.editingURL = true
			e.urlIn.SetValue(r.URL)
			e.urlIn.CursorEnd()
			e.urlIn.Focus()
			return textinput.Blink, false
		case key.Matches(msg, km.CycleMethod):
			applyMethod(r, nextMethod(r.Method))
			return nil, true
		}
	}

	switch e.tab {
	case tabPath:
		return e.path.update(msg, r, e.layers(r), km)
	case tabParams:
		return kvUpdate(&e.params, &r.Query, msg, km)
	case tabHeaders:
		return kvUpdate(&e.headers, &r.Headers, msg, km)
	case tabAuth:
		return e.auth.update(msg, r, km)
	case tabBody:
		switch {
		case key.Matches(msg, km.EditBody):
			e.editingBody = true
			e.body.Focus()
			return textarea.Blink, false
		case key.Matches(msg, km.ExternalEditor):
			return e.openExternalEditor(), false
		case key.Matches(msg, km.CycleBody):
			cycleBodyType(r)
			e.load(e.col, e.reqID)
			return nil, true
		}
	}
	return nil, false
}

// tabEditing reports whether the current tab's field editor is open.
func (e *editor) tabEditing() bool {
	switch e.tab {
	case tabPath:
		return e.path.Editing()
	case tabParams:
		return e.params.Editing()
	case tabHeaders:
		return e.headers.Editing()
	case tabAuth:
		return e.auth.Editing()
	}
	return false
}

// kvUpdate routes a key to a kv editor and syncs its rows back on change.
func kvUpdate(k *kvEditor, dst *[]model.Param, msg tea.KeyPressMsg, km *keyMap) (tea.Cmd, bool) {
	cmd, changed := k.update(msg, km)
	if changed {
		*dst = k.rows
	}
	return cmd, changed
}

// syncBody writes the textarea content back into the request, creating a
// JSON body by default for previously body-less requests.
func (e *editor) syncBody() bool {
	r := e.request()
	content := e.body.Value()
	if r.Body == nil {
		if strings.TrimSpace(content) == "" {
			return false
		}
		r.Body = &model.Body{Type: model.BodyJSON}
	}
	if r.Body.Content == content {
		return false
	}
	r.Body.Content = content
	return true
}

// applyMethod sets a request's method, giving a QUERY somewhere to put its
// content: RFC 10008 requires the request to carry content with a
// Content-Type, and a server must reject one without. Seeding the body the
// way cycleBodyType does for a body-less request keeps the request valid by
// construction; an existing body is never touched.
func applyMethod(r *model.Request, method string) {
	r.Method = method
	if method == httpclient.MethodQuery && r.Body == nil {
		r.Body = &model.Body{Type: model.BodyJSON}
	}
}

func cycleBodyType(r *model.Request) {
	order := []string{model.BodyNone, model.BodyJSON, model.BodyText}
	if r.Body == nil {
		r.Body = &model.Body{Type: model.BodyJSON}
		return
	}
	for i, t := range order {
		if r.Body.Type == t {
			r.Body.Type = order[(i+1)%len(order)]
			return
		}
	}
	r.Body.Type = model.BodyJSON
}

// openExternalEditor writes the body to a temp file and opens $EDITOR on
// it; editorExternalMsg picks the result up.
func (e *editor) openExternalEditor() tea.Cmd {
	ed := os.Getenv("EDITOR")
	if ed == "" {
		if runtime.GOOS == "windows" {
			ed = "notepad"
		} else {
			ed = "vi"
		}
	}
	f, err := os.CreateTemp("", "crisp-body-*.json")
	if err != nil {
		return func() tea.Msg { return editorExternalMsg{err: err} }
	}
	path := f.Name()
	_, werr := f.WriteString(e.body.Value())
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return func() tea.Msg { return editorExternalMsg{err: fmt.Errorf("write temp file: %v / %v", werr, cerr)} }
	}
	parts := strings.Fields(ed) // support EDITOR="code --wait" style values
	parts = append(parts, path)
	// #nosec G204 G702 -- launching the user's own $EDITOR on a temp file is
	// the feature (same trust model as git's core.editor).
	c := exec.Command(parts[0], parts[1:]...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorExternalMsg{path: path, err: err}
	})
}

// finishExternalEdit reads the temp file back into the body. dirty reports
// whether the request changed.
func (e *editor) finishExternalEdit(msg editorExternalMsg) (bool, error) {
	if msg.path != "" {
		defer os.Remove(msg.path)
	}
	if msg.err != nil {
		return false, msg.err
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		return false, err
	}
	e.body.SetValue(string(data))
	return e.syncBody(), nil
}

func nextMethod(m string) string {
	for i, name := range methods {
		if name == strings.ToUpper(m) {
			return methods[(i+1)%len(methods)]
		}
	}
	return methods[0]
}

// setSize fits the editor into a width x height content area.
func (e *editor) setSize(width, height int) {
	e.width = width
	e.urlIn.SetWidth(max(10, width-12))
	e.body.SetWidth(max(10, width-2))
	// The URL bar, tab bar, and body-type label sit above the textarea.
	e.body.SetHeight(max(3, height-3))
}

func (e *editor) view(focused bool) string {
	r := e.request()
	if r == nil {
		return dimStyle.Render("\n  No request selected.\n  Pick one in the sidebar, or press 'n' to create one.")
	}

	url := e.urlIn.View()
	if !e.editingURL {
		url = r.URL
		if url == "" {
			url = dimStyle.Render("(press 'u' to set URL)")
		}
	}
	head := methodStyle(r.Method).Render(fmt.Sprintf(" %-7s", r.Method)) + " " + url

	tabBar := renderTabBar(tabNames, e.tab)

	var content string
	switch e.tab {
	case tabPath:
		content = e.path.view(e.width-2, focused, r, e.layers(r))
	case tabParams:
		content = e.params.view(e.width-2, focused)
	case tabHeaders:
		content = e.headers.view(e.width-2, focused)
	case tabAuth:
		content = e.auth.view(r, e.col, focused)
	case tabBody:
		bodyType := "none"
		if r.Body != nil {
			bodyType = r.Body.Type
		}
		// The keys that act on it are the status bar's job, not this label's.
		label := dimStyle.Render("type: " + bodyType)
		content = label + "\n" + e.body.View()
	}

	return lipgloss.JoinVertical(lipgloss.Left, head, tabBar, content)
}
