package ui

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

// filled is a model holding n activity lines from one agent, sized so the pane shows exactly 5.
func filled(t *testing.T, n int) Model {
	t.Helper()
	m := New(ModelConfig{Roster: roster()})
	// height leaves a five-line pane: two agent rows plus the frame's chrome. The scroll expectations
	// downstream are all measured in screenfuls, so the pane size is what has to stay fixed here.
	m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: len(roster()) + chromeLines + 5})
	for i := range n {
		m = feed(t, m, event(pipeline.EventAgentActivity, "bugs+impl", "line "+strconv.Itoa(i)))
	}
	return m
}

func TestModel_View(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"),
	)

	out := m.View()
	assert.Contains(t, out, "revmux · 2 agents · find", "the status table is on top")
	assert.Contains(t, out, "AGENT", "under a column heading")
	assert.Contains(t, out, "▸ 1 all", "tabs are numbered from one, and the combined view is focused by default")
	assert.Contains(t, out, "2 bugs+impl")
	assert.Contains(t, out, "3 codex")
	assert.Contains(t, out, "tool: Read", "and the detail pane under it")
}

func TestModel_tabBar(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	assert.Equal(t, "▸ 1 all  │  2 bugs+impl  │  3 codex", m.tabBar())
	m.view.tab = 2
	assert.Equal(t, "  1 all  │  2 bugs+impl  │▸ 3 codex", m.tabBar(),
		"exactly one tab is marked, and it is the focused one")

	t.Run("input mode is advertised and gets its own tabs", func(t *testing.T) {
		inputs := New(ModelConfig{Roster: roster(), Inputs: []InputDocument{
			{Label: "scope"}, {Label: "goal"}, {Label: "design/spec.md"},
		}})
		assert.Contains(t, inputs.tabBar(), "i inputs")

		inputs = feed(t, inputs, press("i"))
		assert.Equal(t, "▸ 1 scope  │  2 goal  │  3 design/spec.md  │  i back", inputs.tabBar())
		inputs = feed(t, inputs, press("3"))
		assert.Contains(t, inputs.tabBar(), "▸ 3 design/spec.md")
	})

	t.Run("a late input remains named when the document count exceeds the width", func(t *testing.T) {
		docs := make([]InputDocument, 130)
		for i := range docs {
			docs[i].Label = "context/file-" + strconv.Itoa(i) + ".md"
		}
		inputs := feed(t, New(ModelConfig{Inputs: docs}), tea.WindowSizeMsg{Width: 60, Height: 24}, press("i"))
		inputs.focus(127)

		out := inputs.tabBar()
		assert.LessOrEqual(t, lipgloss.Width(out), 60)
		assert.Contains(t, out, "context/file-127.md")
		assert.Contains(t, out, "i back")
		assert.NotContains(t, out, "context/file-0.md", "the bar windows around the focused document")
	})
}

func TestModel_detailPane(t *testing.T) {
	t.Run("everything fits, and the pane is padded out to keep the frame's shape", func(t *testing.T) {
		m := filled(t, 3)
		m.view.tab = 1
		pane := m.detailPane()
		require.Len(t, pane, m.paneHeight())
		assert.Contains(t, pane[2], "line 2")
		assert.Empty(t, pane[3])
		assert.Equal(t, 0, m.maxScroll(), "there is nothing to scroll back to")
	})

	t.Run("a longer log shows its newest lines", func(t *testing.T) {
		m := filled(t, 20)
		m.view.tab = 1
		pane := m.detailPane()
		require.Len(t, pane, m.paneHeight())
		assert.Contains(t, pane[len(pane)-1], "line 19", "a live log is read from the bottom")
	})

	t.Run("scrolling back moves the window", func(t *testing.T) {
		m := filled(t, 20)
		m.view.tab = 1
		m.view.scroll = 3
		pane := m.detailPane()
		assert.Contains(t, pane[len(pane)-1], "line 16")
	})

	t.Run("a scroll past the top clamps to the oldest line", func(t *testing.T) {
		m := filled(t, 20)
		m.view.tab = 1
		m.view.scroll = 500
		assert.Contains(t, m.detailPane()[0], "line 0")
	})
}

func TestModel_clip(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 12, Height: 20})

	assert.Equal(t, "hello world ", m.clip("hello world that runs on"))
	assert.Equal(t, "\x1b[36mbugs\x1b[39m", m.clip(roster()[0].Paint("bugs")),
		"a color sequence has no width and must not be counted as if it did")

	t.Run("an unsized terminal falls back to a usable default", func(t *testing.T) {
		bare := Model{}
		assert.Equal(t, defaultCols, bare.view.width())
		assert.Equal(t, defaultRows, bare.view.height())
	})
}

func TestModel_paneHeight(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 80, Height: 10})
	assert.Equal(t, 2, m.paneHeight(), "the table, its heading, all three rules and the tab bar take their rows first")
	assert.Len(t, strings.Split(m.View(), "\n"), 10, "and the frame still fits the terminal exactly")

	t.Run("a terminal too short for the table still renders a pane", func(t *testing.T) {
		tiny := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 40, Height: 2})
		assert.Equal(t, 1, tiny.paneHeight())
	})
}

func TestModel_tabBar_collapsesWhenItCannotFit(t *testing.T) {
	// a real roster plus the verify groups runs past 80 columns easily, and there is no horizontal
	// scroll on this line: clipping alone cuts the right-hand tabs mid-word, so a reader cannot tell
	// how many panes exist or what is hiding past the edge
	wide := []prompt.AgentSpec{
		{Name: "bugs+impl"}, {Name: "arch+quality"}, {Name: "docs+tests"}, {Name: "codex"},
		{Name: "synthesis"}, {Name: "verify ui"}, {Name: "verify executor"},
	}
	bar := func(t *testing.T, width int) string {
		t.Helper()
		m := feed(t, New(ModelConfig{Roster: wide}), tea.WindowSizeMsg{Width: width, Height: 24})
		m.view.tab = 6 // "verify ui", second from the right
		out := m.tabBar()
		assert.LessOrEqual(t, lipgloss.Width(out), width, "the bar must fit the terminal, not be cut off by it")
		assert.Contains(t, out, "7 verify ui", "the focused tab keeps its name at every width")
		assert.Contains(t, out, "1 all", "and so does tab one, which is short and is where a reader falls back to")
		return out
	}

	t.Run("a bar that fits is left alone", func(t *testing.T) {
		full := bar(t, 200)
		assert.Contains(t, full, "1 all", "nothing is abbreviated when there is room")
		assert.Contains(t, full, "8 verify executor")
		assert.Contains(t, full, "  │", "and it keeps the padding around the separator")
	})

	t.Run("the padding goes before any name does", func(t *testing.T) {
		// padding is decoration and a name is information: giving up a name to save two spaces per tab
		// is the wrong trade, and making it was the defect this pins
		snug := bar(t, 120)
		assert.Contains(t, snug, "2 bugs+impl", "every name survives at a width the padded bar could not reach")
		assert.Contains(t, snug, "8 verify executor")
		assert.NotContains(t, snug, "  │", "what was given up is the padding")
	})

	t.Run("only then do tabs collapse, and from the left", func(t *testing.T) {
		// panes fill left to right as a run goes on, so the right-hand end is the recent work and the
		// names nearest the focused tab are the ones a reader is most likely looking for
		narrow := bar(t, 90)
		assert.NotContains(t, narrow, "bugs+impl", "tab two gives way first, since tab one is exempt")
		assert.Contains(t, narrow, "3 arch+quality", "while everything that still fits keeps its name")
		assert.Contains(t, narrow, "8 verify executor")

		narrower := bar(t, 70)
		assert.NotContains(t, narrower, "arch+quality", "more give way as the terminal shrinks")
		assert.NotContains(t, narrower, "docs+tests")
		assert.Contains(t, narrower, "5 codex", "and it stops as soon as it fits, rather than clearing the bar")
		assert.Contains(t, narrower, "8 verify executor")
	})

	t.Run("every tab stays reachable however far it collapses", func(t *testing.T) {
		tiny := bar(t, 30)
		for _, token := range []string{"2", "3", "4", "5", "6", "8"} {
			assert.Contains(t, tiny, token, "token %s must remain, or that pane cannot be reached", token)
		}
		assert.Contains(t, tiny, "7 verify ui",
			"and the focused tab keeps its name even here — a bare token would leave the pane on screen unnamed")
	})
}

func TestTabToken(t *testing.T) {
	m0 := New(ModelConfig{})
	assert.Equal(t, "1", m0.tabToken(0), "the combined view is tab one")
	assert.Equal(t, "9", m0.tabToken(8))
	assert.Equal(t, "a", m0.tabToken(9), "letters carry on where the digits stop")
	assert.Equal(t, " ", m0.tabToken(9+len(tabLetters)), "past the tokens a pane has none, and is reached with tab")

	t.Run("every token round-trips to the pane it names", func(t *testing.T) {
		for i := range 9 + len(tabLetters) {
			assert.Equal(t, i, m0.tabIndex(m0.tabToken(i)), "token %q must select pane %d", m0.tabToken(i), i)
		}
	})

	t.Run("no token collides with a key already bound to something else", func(t *testing.T) {
		// a bound key is matched before the token lookup is reached, so a tab assigned one would be
		// unreachable — silently, and only on a run with enough panes to get that far
		for _, b := range []key.Binding{keys.quit, keys.nextTab, keys.prevTab, keys.up, keys.down,
			keys.pageUp, keys.pageDown, keys.top, keys.bottom, keys.findings, keys.inputs,
			keys.startFilter} {
			for _, k := range b.Keys() {
				assert.Equal(t, -1, m0.tabIndex(k), "key %q is bound already and must not also name a tab", k)
			}
		}
	})

	t.Run("a key naming nothing is ignored rather than blanking the view", func(t *testing.T) {
		for _, k := range []string{"0", "", "ab", "ctrl+c", "Z"} {
			assert.Equal(t, -1, m0.tabIndex(k))
		}
	})
}

func TestModel_tabBar_clipBackstop(t *testing.T) {
	// the one path where the width guarantee can actually break. Every other width is satisfied inside
	// the search loop, where asserting the bar fits only re-checks the loop's own exit condition — the
	// loop returns when it fits, so that assertion is tautological there and proves nothing.
	wide := []prompt.AgentSpec{
		{Name: "bugs+impl"}, {Name: "arch+quality"}, {Name: "docs+tests"}, {Name: "codex"},
		{Name: "synthesis"}, {Name: "verify ui"}, {Name: "verify executor"},
	}
	for _, width := range []int{18, 12, 4, 1} {
		t.Run(strconv.Itoa(width)+" columns is below anything the search can satisfy", func(t *testing.T) {
			m := feed(t, New(ModelConfig{Roster: wide}), tea.WindowSizeMsg{Width: width, Height: 24})
			m.view.tab = 6

			out := m.tabBar()
			assert.LessOrEqual(t, lipgloss.Width(out), width,
				"the backstop must still hold the width, or the frame breaks its own shape")
			assert.NotContains(t, out, "\n", "however narrow, the bar is one line")
			assert.True(t, utf8.ValidString(out), "clipping must not cut a rune or a color sequence in half")
		})
	}
}
