package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/pipeline"
)

// press builds the key message for one keystroke, runes and named keys alike.
func press(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestModel_key_tabs(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"tab moves forward", []string{"tab"}, 1},
		{"and again", []string{"tab", "tab"}, 2},
		{"past the last agent it stays put", []string{"tab", "tab", "tab"}, 2},
		{"shift+tab moves back", []string{"tab", "tab", "shift+tab"}, 1},
		{"back past the combined view stays put", []string{"shift+tab"}, 0},
		// tabs are labeled from one, so the digit typed is one past the index it selects
		{"a digit selects a pane directly", []string{"3"}, 2},
		{"one returns to the combined view", []string{"3", "1"}, 0},
		{"a digit past the roster is ignored", []string{"2", "7"}, 1},
		{"zero is not a tab", []string{"2", "0"}, 1},
		{"an unbound key changes nothing", []string{"2", "z"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(ModelConfig{Roster: roster()})
			for _, k := range tt.keys {
				m = feed(t, m, press(k))
			}
			assert.Equal(t, tt.want, m.view.tab)
		})
	}
}

func TestModel_key_scroll(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"up scrolls back one line", []string{"up"}, 1},
		{"and k does the same", []string{"k", "k"}, 2},
		{"down comes back toward the newest", []string{"up", "up", "down"}, 1},
		{"it never scrolls past the newest line", []string{"down"}, 0},
		{"page up moves a screenful", []string{"pgup"}, 5},
		{"page down comes back", []string{"pgup", "pgdown"}, 0},
		{"g jumps to the oldest line", []string{"g"}, 15},
		{"G returns to the newest", []string{"g", "G"}, 0},
		{"it never scrolls past the oldest line", []string{"g", "pgup", "pgup"}, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := filled(t, 20)
			m = feed(t, m, press("2")) // the first agent's pane; tabs are labeled from one
			require.Equal(t, 15, m.maxScroll(), "20 lines in a pane showing 5")
			for _, k := range tt.keys {
				m = feed(t, m, press(k))
			}
			assert.Equal(t, tt.want, m.view.scroll)
		})
	}

	t.Run("switching panes opens the new one at its newest line", func(t *testing.T) {
		m := feed(t, filled(t, 20), press("2"), press("g"))
		require.Equal(t, 15, m.view.scroll)
		m = feed(t, m, press("1"))
		assert.Equal(t, 0, m.view.scroll)
	})

	t.Run("a pane with nothing in it does not scroll", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), press("2"), press("up"))
		assert.Equal(t, 0, m.view.scroll)
	})
}

func TestModel_key_quit(t *testing.T) {
	t.Run("ctrl+c ends a running review", func(t *testing.T) {
		_, cmd := New(ModelConfig{Roster: roster()}).Update(press("ctrl+c"))
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd(), "quitting stops watching the run, package main still writes the report")
	})

	t.Run("ctrl+c ends it from a half-typed filter too", func(t *testing.T) {
		typing := feed(t, browsed(t, report()), press("/"), press("vie"))
		_, cmd := typing.Update(press("ctrl+c"))
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd())
	})

	t.Run("q waits for the report", func(t *testing.T) {
		next, cmd := New(ModelConfig{Roster: roster()}).Update(press("q"))
		assert.Nil(t, cmd, "q is inert while the review is still running")
		assert.False(t, next.(Model).done)
	})

	t.Run("q quits once the report is in", func(t *testing.T) {
		_, cmd := browsed(t, report()).Update(press("q"))
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd())
	})

	t.Run("esc never quits, mid-run", func(t *testing.T) {
		_, cmd := New(ModelConfig{Roster: roster()}).Update(press("esc"))
		assert.Nil(t, cmd)
	})

	t.Run("esc never quits, in the findings browser", func(t *testing.T) {
		m := browsed(t, report())
		require.True(t, m.browsing())
		next, cmd := m.Update(press("esc"))
		assert.Nil(t, cmd)
		assert.True(t, next.(Model).browsing(), "and it does not leave the browser either")
	})

	t.Run("esc still leaves the input viewer", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster(), Inputs: []InputDocument{
			{Label: "scope", Path: "input/scope.md", Content: "read me", Markdown: true},
		}})
		m = feed(t, m, press("i"))
		require.Equal(t, modeInputs, m.view.mode)
		next, cmd := m.Update(press("esc"))
		assert.Nil(t, cmd)
		assert.Equal(t, modeReview, next.(Model).view.mode)
	})

	t.Run("esc still abandons a filter", func(t *testing.T) {
		m := feed(t, browsed(t, report()), press("/"), press("vie"))
		require.Len(t, m.findings.matches, 1)
		next, cmd := m.Update(press("esc"))
		assert.Nil(t, cmd)
		m = next.(Model)
		assert.False(t, m.findings.typing)
		assert.Empty(t, m.findings.query)
		assert.Len(t, m.findings.matches, 3)
	})
}

func TestModel_key_inputs(t *testing.T) {
	docs := []InputDocument{
		{Label: "scope", Path: "input/scope.md", Content: strings.Repeat("scope line\n", 20), Markdown: true},
		{Label: "goal", Path: "input/goal.md", Notice: "not provided", Markdown: true},
	}
	m := filled(t, 20)
	m.cfg.Inputs, m.cfg.Task, m.cfg.Run = docs, "pr-123", "01-initial"
	m = feed(t, m, press("2"), press("g"))
	require.Equal(t, 1, m.view.tab)
	require.Positive(t, m.view.scroll)

	m = feed(t, m, press("i"))
	assert.Equal(t, modeInputs, m.view.mode)
	assert.Equal(t, 0, m.view.tab)
	assert.Equal(t, m.maxScroll(), m.view.scroll, "an input opens at the top of its document")
	assert.Contains(t, m.View(), "inputs · pr-123/01-initial")

	m = feed(t, m, press("down"))
	inputScroll := m.view.scroll
	m = feed(t, m, press("i"))
	assert.Equal(t, modeReview, m.view.mode)
	assert.Equal(t, 1, m.view.tab, "the agent pane is restored")
	assert.Equal(t, 15, m.view.scroll, "and so is its scroll position")

	m = feed(t, m, press("i"))
	assert.Equal(t, inputScroll, m.view.scroll, "the input position is restored too")
	next, cmd := m.Update(press("esc"))
	assert.Nil(t, cmd, "escape returns from inputs instead of quitting")
	m = next.(Model)
	assert.Equal(t, modeReview, m.view.mode)

	m = feed(t, m, press("i"))
	_, cmd = m.Update(press("q"))
	assert.Nil(t, cmd, "q does not end a review that is still running, from the input viewer either")

	m = feed(t, m, CompletedMsg{Report: report()})
	require.Equal(t, modeInputs, m.view.mode, "completion leaves the open document alone")
	_, cmd = m.Update(press("q"))
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

func TestModel_key_inputs_completionDoesNotInterrupt(t *testing.T) {
	m := New(ModelConfig{Roster: roster(), Inputs: []InputDocument{
		{Label: "scope", Path: "input/scope.md", Content: "read me", Markdown: true},
	}})
	m = feed(t, m, press("i"), CompletedMsg{Report: report()})

	assert.Equal(t, modeInputs, m.view.mode)
	assert.Equal(t, 0, m.view.tab, "the document stays focused")
	assert.True(t, m.view.reviewFindings, "findings wait behind the return to review mode")

	m = feed(t, m, event(pipeline.EventAgentStarted, "verify-late", "verify"))
	assert.NotEqual(t, m.findingsTab(), m.view.review.tab, "the numeric findings tab moved after completion")

	m = feed(t, m, press("i"))
	assert.Equal(t, modeReview, m.view.mode)
	assert.Equal(t, m.findingsTab(), m.view.tab)
	assert.Equal(t, m.maxScroll(), m.view.scroll, "the completed report opens at its top")
	assert.False(t, m.view.reviewFindings)
}

func TestModel_key_inputs_restoreFindingsAfterAgentListChanges(t *testing.T) {
	m := New(ModelConfig{Roster: roster(), Inputs: []InputDocument{
		{Label: "scope", Path: "input/scope.md", Content: "read me", Markdown: true},
	}})
	m = feed(t, m, CompletedMsg{Report: report()}, press("i"))
	require.True(t, m.view.reviewFindings)

	m = feed(t, m, event(pipeline.EventAgentStarted, "verify-late", "verify"), press("i"))

	assert.Equal(t, modeReview, m.view.mode)
	assert.Equal(t, m.findingsTab(), m.view.tab, "findings are restored by identity, not their stale numeric tab")
}

func TestModel_key_findingsFromInputsOpensAtTop(t *testing.T) {
	m := New(ModelConfig{Roster: roster(), Inputs: []InputDocument{
		{Label: "scope", Path: "input/scope.md", Content: "read me", Markdown: true},
	}})
	m = feed(t, m,
		tea.WindowSizeMsg{Width: 80, Height: len(roster()) + chromeLines + 5},
		press("i"),
		CompletedMsg{Report: report()},
		press("f"),
	)

	assert.Equal(t, modeReview, m.view.mode)
	assert.Equal(t, m.findingsTab(), m.view.tab)
	require.Positive(t, m.maxScroll(), "the report must be longer than the pane")
	assert.Equal(t, m.maxScroll(), m.view.scroll, "the findings report opens at its top")
}

func TestModel_key_inputs_emptyConfig(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}), press("i"))
	assert.Equal(t, modeReview, m.view.mode, "there is no empty mode when the renderer supplied no snapshot")
}
