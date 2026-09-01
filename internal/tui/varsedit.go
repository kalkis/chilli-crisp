package tui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// varsEditor is the modal that edits one scope's variables: a collection's,
// or a folder's. It is opened with enter on a sidebar header row.
//
// The rows are a kvEditor because a variable map is exactly a name/value
// list; only the enabled checkbox is meaningless here, which hideToggle
// drops. The map is rebuilt from the rows after every change, so the ordering
// the widget imposes never leaks into the file.
type varsEditor struct {
	col    *model.Collection
	folder string // "" edits the collection's own vars
	kv     kvEditor
}

func newVarsEditor(col *model.Collection, folder string) varsEditor {
	v := varsEditor{col: col, folder: folder, kv: newKVEditor()}
	v.kv.hideToggle = true
	v.kv.load(varRows(v.current()))
	return v
}

// current is the map this editor is bound to, or nil when the scope has no
// variables yet.
func (v *varsEditor) current() map[string]string {
	if v.col == nil {
		return nil
	}
	if v.folder == "" {
		return v.col.Vars
	}
	return v.col.FolderVars(v.folder)
}

// varRows renders a variable map as sorted rows, so the modal opens the same
// way every time.
func varRows(m map[string]string) []model.Param {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]model.Param, 0, len(names))
	for _, name := range names {
		rows = append(rows, model.Param{Name: name, Value: m[name]})
	}
	return rows
}

func (v *varsEditor) title() string {
	if v.col == nil {
		return "Variables"
	}
	if v.folder != "" {
		return "Folder variables · " + v.folder
	}
	return "Collection variables · " + v.col.Name
}

// update handles a key. changed reports that the collection was modified and
// must be saved.
func (v *varsEditor) update(msg tea.KeyPressMsg, km *keyMap) (tea.Cmd, bool) {
	cmd, changed := v.kv.update(msg, km)
	if changed {
		v.apply()
	}
	return cmd, changed
}

// apply rebuilds the bound map from the rows. Rows with a blank name are
// skipped rather than dropped from the widget, so a freshly added row can be
// named without a phantom "" variable appearing in the file in the meantime;
// on a duplicate name the last row wins.
func (v *varsEditor) apply() {
	if v.col == nil {
		return
	}
	var out map[string]string
	for _, row := range v.kv.rows {
		if row.Name == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[row.Name] = row.Value
	}
	if v.folder == "" {
		v.col.Vars = out
		return
	}
	if out == nil {
		v.dropFolder()
		return
	}
	v.col.EnsureFolder(v.folder).Vars = out
}

// dropFolder removes a folder entry that no longer carries anything. The
// folder itself still exists — it is implied by its requests — so leaving an
// empty entry behind would only be noise in the file.
func (v *varsEditor) dropFolder() {
	for i := range v.col.Folders {
		if v.col.Folders[i].Name == v.folder {
			v.col.Folders = append(v.col.Folders[:i], v.col.Folders[i+1:]...)
			return
		}
	}
}

func (v *varsEditor) view(width, height int) string {
	inner := max(20, min(width-8, 72))
	// Width includes the border (2) and Padding(1, 2) sides (4); longer rows
	// would wrap and break the one-line-per-row cursor math.
	textW := inner - 6

	body := v.kv.view(textW, true)
	if len(v.kv.rows) == 0 {
		body = dimStyle.Render("(no variables)")
	}
	// ToggleRow is absent because these rows have no disabled state; that is
	// the same fact kvEditor.hideToggle encodes.
	box := overlayStyle.Width(inner).Render(
		titleStyle.Render(v.title()) + "\n\n" + body + "\n\n" +
			helpLine(textW, defaultKeys.EditName, defaultKeys.EditValue,
				defaultKeys.AddRow, defaultKeys.DeleteRow, defaultKeys.Close),
	)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
