package ui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// keys are the bindings the detail panes answer to. Number keys are matched separately, since the
// digit selects the tab and listing ten bindings here would say the same thing ten times.
var keys = struct {
	quit, nextTab, prevTab, up, down, pageUp, pageDown, top, bottom key.Binding
	findings, inputs, startFilter                                   key.Binding
}{
	quit:        key.NewBinding(key.WithKeys("q")),
	nextTab:     key.NewBinding(key.WithKeys("tab", "right", "l")),
	prevTab:     key.NewBinding(key.WithKeys("shift+tab", "left", "h")),
	up:          key.NewBinding(key.WithKeys("up", "k")),
	down:        key.NewBinding(key.WithKeys("down", "j")),
	pageUp:      key.NewBinding(key.WithKeys("pgup", "ctrl+b")),
	pageDown:    key.NewBinding(key.WithKeys("pgdown", "ctrl+f")),
	top:         key.NewBinding(key.WithKeys("home", "g")),
	bottom:      key.NewBinding(key.WithKeys("end", "G")),
	findings:    key.NewBinding(key.WithKeys("f")),
	inputs:      key.NewBinding(key.WithKeys("i")),
	startFilter: key.NewBinding(key.WithKeys("/")),
}

// key handles one keystroke. Quitting stops watching the run, it does not stop the run: package main
// still holds the report and writes it once the program has returned.
//
// **Only ctrl+c ends a review that is still running.** `q` is inert until the report is in, and esc never
// quits at all — it abandons a filter and it leaves the input viewer, nothing else. A reader who hits esc
// to back out of something, or q expecting a pager, would otherwise lose the view of a live run.
func (m Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		// checked ahead of the filter editor, which treats every other key as text: a half-typed
		// query must never be a trap
		return m, tea.Quit
	}
	if m.view.mode == modeInputs && msg.Type == tea.KeyEsc {
		m.leaveInputs()
		return m, nil
	}
	if m.browsing() && m.browseKey(msg) {
		return m, nil
	}

	switch {
	case key.Matches(msg, keys.quit):
		return m, m.quitCmd()
	case key.Matches(msg, keys.inputs):
		m.toggleInputs()
	case key.Matches(msg, keys.findings):
		if m.view.mode == modeInputs && m.findings != nil {
			m.leaveInputs()
		}
		m.focus(m.findingsTab())
	case key.Matches(msg, keys.nextTab):
		m.focus(m.view.tab + 1)
	case key.Matches(msg, keys.prevTab):
		m.focus(m.view.tab - 1)
	case key.Matches(msg, keys.up):
		m.scroll(1)
	case key.Matches(msg, keys.down):
		m.scroll(-1)
	case key.Matches(msg, keys.pageUp):
		m.scroll(m.paneHeight())
	case key.Matches(msg, keys.pageDown):
		m.scroll(-m.paneHeight())
	case key.Matches(msg, keys.top):
		m.view.scroll = m.maxScroll()
	case key.Matches(msg, keys.bottom):
		m.view.scroll = 0
	default:
		// the token a tab shows is what selects it, digits first and then letters, so the bar itself
		// documents the keys and nothing has to be counted
		if idx := m.tabIndex(msg.String()); idx >= 0 {
			m.focus(idx)
		}
	}
	return m, nil
}

// quitCmd ends the program only once the report is in, and is nil until then: q is the pager key a
// reader reaches for, and it must not close the view of a review that is still running.
func (m Model) quitCmd() tea.Cmd {
	if !m.done {
		return nil
	}
	return tea.Quit
}

// focus switches panes, ignoring a tab that does not exist so a stray digit cannot blank the view.
// The new pane opens at its newest line, which is where a live log is worth reading from.
func (m *Model) focus(tab int) {
	if tab < 0 || tab > m.lastTab() {
		return
	}
	m.view.tab, m.view.scroll = tab, 0
	if m.view.mode == modeInputs || m.browsing() {
		m.view.scroll = m.maxScroll()
	}
}

// lastTab is the rightmost pane: the last agent, or the findings browser once the report has opened
// one past it.
func (m Model) lastTab() int {
	if m.view.mode == modeInputs {
		return len(m.cfg.Inputs) - 1
	}
	if m.findings != nil {
		return len(m.agents) + 1
	}
	return len(m.agents)
}

// browsing says the reader is in the findings browser, which is where the filter keys mean something.
// Scrolling there is the pane's own: there is no cursor and nothing folds.
func (m Model) browsing() bool {
	return m.view.mode == modeReview && m.findings != nil && m.view.tab == m.findingsTab()
}

func (m *Model) toggleInputs() {
	if len(m.cfg.Inputs) == 0 {
		return
	}
	if m.view.mode == modeInputs {
		m.leaveInputs()
		return
	}
	m.view.reviewFindings = m.browsing()
	m.view.review = m.view.navState
	m.view.mode = modeInputs
	m.view.navState = m.view.inputs
	if m.view.scroll < 0 {
		m.view.scroll = m.maxScroll()
	}
	m.view.scroll = min(m.view.scroll, m.maxScroll())
}

func (m *Model) leaveInputs() {
	if m.view.mode != modeInputs {
		return
	}
	m.view.inputs = m.view.navState
	m.view.mode = modeReview
	m.view.navState = m.view.review
	if m.view.reviewFindings {
		m.view.tab = m.findingsTab()
		m.view.scroll = m.reviewMaxScroll(m.view.tab)
		m.view.reviewFindings = false
	}
	m.view.scroll = min(m.view.scroll, m.maxScroll())
}

// browseKey handles the keys that mean something only in the browser and reports whether it took the
// keystroke. What it does not take falls through to the pane keys, so quitting and tab switching
// work from here too.
func (m *Model) browseKey(msg tea.KeyMsg) bool {
	if m.findings.typing {
		m.editFilter(msg)
		return true
	}

	// only the filter. **The browser renders the report and nothing more**: scrolling it is the same
	// scrolling every other pane has, so the arrow and page keys fall through rather than being
	// reimplemented here against a cursor the pane does not need.
	if key.Matches(msg, keys.startFilter) {
		m.findings.typing = true
		return true
	}
	return false
}

// editFilter feeds one keystroke to the filter query. Esc abandons it and enter accepts it; every
// other key is text, so a query can carry a q or a j without acting on the browser.
func (m *Model) editFilter(msg tea.KeyMsg) {
	f := m.findings
	switch msg.Type {
	case tea.KeyEsc:
		f.typing = false
		f.filter("")
	case tea.KeyEnter:
		f.typing = false
	case tea.KeyBackspace:
		if q := []rune(f.query); len(q) > 0 {
			f.filter(string(q[:len(q)-1]))
		}
	case tea.KeyRunes, tea.KeySpace:
		f.filter(f.query + string(msg.Runes))
	default: // a named key carries no text, so it edits nothing
	}
	m.view.scroll = m.maxScroll() // a narrowed report is read from its top, like the whole one
}

// scroll moves the window back through the log by n lines, clamped to what the pane actually holds.
func (m *Model) scroll(n int) {
	m.view.scroll = min(max(m.view.scroll+n, 0), m.maxScroll())
}
