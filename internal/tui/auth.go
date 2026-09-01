package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kalkis/chilli-crisp/internal/model"
)

// authTypes cycles nil (inherit collection auth) through the concrete types.
var authTypes = []string{"inherit", model.AuthNone, model.AuthBasic, model.AuthBearer, model.AuthAPIKey}

type authField struct {
	label string
	get   func(*model.Auth) string
	set   func(*model.Auth, string)
}

var authFields = map[string][]authField{
	model.AuthBasic: {
		{"username", func(a *model.Auth) string { return a.Username }, func(a *model.Auth, v string) { a.Username = v }},
		{"password", func(a *model.Auth) string { return a.Password }, func(a *model.Auth, v string) { a.Password = v }},
	},
	model.AuthBearer: {
		{"token", func(a *model.Auth) string { return a.Token }, func(a *model.Auth, v string) { a.Token = v }},
	},
	model.AuthAPIKey: {
		{"key", func(a *model.Auth) string { return a.Key }, func(a *model.Auth, v string) { a.Key = v }},
		{"value", func(a *model.Auth) string { return a.Value }, func(a *model.Auth, v string) { a.Value = v }},
		{"in (header/query)", func(a *model.Auth) string { return a.In }, func(a *model.Auth, v string) { a.In = v }},
	},
}

// authEditor edits the per-request auth override.
type authEditor struct {
	cursor  int
	editing bool
	input   textinput.Model
}

func newAuthEditor() authEditor {
	in := textinput.New()
	in.Prompt = ""
	return authEditor{input: in}
}

func (a *authEditor) load() {
	a.cursor = 0
	a.editing = false
}

func (a *authEditor) Editing() bool { return a.editing }

func (a *authEditor) fields(r *model.Request) []authField {
	if r.Auth == nil {
		return nil
	}
	return authFields[r.Auth.Type]
}

func (a *authEditor) update(msg tea.KeyPressMsg, r *model.Request, km *keyMap) (tea.Cmd, bool) {
	fields := a.fields(r)

	if a.editing {
		switch {
		case key.Matches(msg, km.SaveField):
			fields[a.cursor].set(r.Auth, a.input.Value())
			a.editing = false
			a.input.Blur()
			return nil, true
		case key.Matches(msg, km.CancelField):
			a.editing = false
			a.input.Blur()
			return nil, false
		default:
			var cmd tea.Cmd
			a.input, cmd = a.input.Update(msg)
			return cmd, false
		}
	}

	switch {
	case key.Matches(msg, km.CycleAuth):
		a.cycleType(r)
		a.cursor = 0
		return nil, true
	case key.Matches(msg, km.Up):
		if a.cursor > 0 {
			a.cursor--
		}
	case key.Matches(msg, km.Down):
		if a.cursor < len(fields)-1 {
			a.cursor++
		}
	case key.Matches(msg, km.EditAuth):
		if len(fields) == 0 {
			return nil, false
		}
		f := fields[a.cursor]
		// The apikey "in" field toggles instead of free-text editing.
		if r.Auth.Type == model.AuthAPIKey && a.cursor == 2 {
			if r.Auth.In == model.APIKeyInQuery {
				r.Auth.In = model.APIKeyInHeader
			} else {
				r.Auth.In = model.APIKeyInQuery
			}
			return nil, true
		}
		a.editing = true
		a.input.SetValue(f.get(r.Auth))
		a.input.CursorEnd()
		a.input.Focus()
		return textinput.Blink, false
	}
	return nil, false
}

func (a *authEditor) cycleType(r *model.Request) {
	current := "inherit"
	if r.Auth != nil {
		current = r.Auth.Type
		if current == "" {
			current = model.AuthNone
		}
	}
	for i, t := range authTypes {
		if t == current {
			next := authTypes[(i+1)%len(authTypes)]
			if next == "inherit" {
				r.Auth = nil
			} else {
				r.Auth = &model.Auth{Type: next, In: model.APIKeyInHeader}
			}
			return
		}
	}
	r.Auth = nil
}

func (a *authEditor) view(r *model.Request, col *model.Collection, focused bool) string {
	label := "inherit"
	if r.Auth != nil {
		label = r.Auth.Type
	}
	out := fmt.Sprintf(" type: %s  ('t' to cycle)\n", label)
	if r.Auth == nil {
		inherited := "none"
		if col != nil && col.Auth != nil {
			inherited = col.Auth.Type
		}
		return out + dimStyle.Render(fmt.Sprintf(" using collection auth: %s", inherited))
	}
	fields := a.fields(r)
	for i, f := range fields {
		value := f.get(r.Auth)
		if a.editing && i == a.cursor {
			value = a.input.View()
		}
		line := fmt.Sprintf("  %-18s %s", f.label+":", value)
		if i == a.cursor && focused {
			line = cursorStyle.Render(line)
		}
		out += line + "\n"
	}
	return out
}
