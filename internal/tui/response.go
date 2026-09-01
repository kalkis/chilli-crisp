package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/wrap"

	"github.com/kalkis/chilli-crisp/internal/httpclient"
)

const (
	respPretty = iota
	respRaw
	respHeaders
	respTabCount
)

var respTabNames = []string{"Pretty", "Raw", "Headers"}

// responseView renders the last response (or any plain text, e.g. a curl
// command) in a scrollable viewport.
type responseView struct {
	title string
	tabs  [respTabCount]string // pre-rendered content per tab
	tab   int
	text  string // non-response text mode (curl export, errors); overrides tabs
	vp    viewport.Model
	has   bool
}

func newResponseView() responseView {
	return responseView{vp: viewport.New()}
}

// setSize fits the view into a width x height content area; the title row
// sits above the viewport.
func (v *responseView) setSize(width, height int) {
	v.vp.SetWidth(max(1, width))
	v.vp.SetHeight(max(1, height-1))
	v.refresh()
}

// SetResponse ingests a captured response.
func (v *responseView) SetResponse(resp *httpclient.Response) {
	statusStyle := okStatusStyle
	if resp.StatusCode >= 400 {
		statusStyle = errStatusStyle
	}
	truncNote := ""
	if resp.Truncated {
		truncNote = " (body truncated)"
	}
	v.title = fmt.Sprintf("%s  %s  %s%s",
		statusStyle.Render(resp.Status),
		resp.Duration.Round(time.Millisecond),
		httpclient.FormatSize(len(resp.Body)), truncNote)

	v.tabs[respRaw] = string(resp.Body)
	v.tabs[respPretty] = highlight(prettify(resp.Body), langFor(resp.Headers.Get("Content-Type"), resp.Body))

	var hdr []string
	keys := make([]string, 0, len(resp.Headers))
	for k := range resp.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		hdr = append(hdr, fmt.Sprintf("%s: %s", k, strings.Join(resp.Headers[k], ", ")))
	}
	v.tabs[respHeaders] = strings.Join(hdr, "\n")

	v.text = ""
	v.has = true
	v.tab = respPretty
	v.refresh()
	v.vp.GotoTop()
}

// SetText shows arbitrary text (errors, curl commands) instead of a
// response.
func (v *responseView) SetText(title, text string) {
	v.title = title
	v.text = text
	v.has = true
	v.refresh()
	v.vp.GotoTop()
}

func (v *responseView) refresh() {
	if v.text != "" {
		v.vp.SetContent(wrapToWidth(v.text, v.vp.Width()))
		return
	}
	v.vp.SetContent(wrapToWidth(v.tabs[v.tab], v.vp.Width()))
}

func (v *responseView) update(msg tea.KeyPressMsg, km *keyMap) tea.Cmd {
	switch {
	case key.Matches(msg, km.PrevRespTab):
		v.tab = (v.tab + respTabCount - 1) % respTabCount
		v.refresh()
		return nil
	case key.Matches(msg, km.NextRespTab):
		v.tab = (v.tab + 1) % respTabCount
		v.refresh()
		return nil
	}
	var cmd tea.Cmd
	v.vp, cmd = v.vp.Update(msg)
	return cmd
}

func (v *responseView) view() string {
	if !v.has {
		return dimStyle.Render("  No response yet — press 's' to send.")
	}
	head := v.title
	if v.text == "" {
		head = v.title + "   " + renderTabBar(respTabNames, v.tab)
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, v.vp.View())
}

// prettify indents JSON bodies; anything else passes through untouched.
func prettify(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return string(body)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		return string(body)
	}
	return buf.String()
}

// wrapToWidth hard-wraps long lines (ANSI-aware, so highlighted content
// wraps on visible width) so the viewport never scrolls horizontally into
// hidden content.
func wrapToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	return wrap.String(s, width)
}
