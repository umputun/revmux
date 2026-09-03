package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

func TestModel_statusTable(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster(), Profile: "comprehensive"}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		event(pipeline.EventAgentStarted, "bugs+impl", "bugs, impl"),
		pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs+impl", Text: "tool: Read", At: at.Add(9 * time.Second)},
	)

	rows := strings.Split(m.statusTable(), "\n")
	require.Len(t, rows, 6,
		"a header, the rule under it, a column heading, one row per agent, and the closing rule")
	assert.Equal(t, m.rule(), rows[1], "the header is separated from the table rather than reading as its first row")
	assert.Equal(t, "revmux · comprehensive · find · ctrl+c to quit", rows[0])
	assert.Contains(t, rows[2], "AGENT", "the column heading names what each column holds")
	assert.Contains(t, rows[2], "ACTIVITY")
	assert.Contains(t, rows[3], "running")
	assert.Contains(t, rows[3], "9s", "elapsed comes off the event timestamps, not a clock")
	assert.Contains(t, rows[3], "tool: Read", "the row shows the last activity")
	assert.Contains(t, rows[4], "waiting", "an agent that has not reported yet is still listed")
	assert.Contains(t, rows[4], "-", "and has no elapsed time")
}

func TestModel_statusTable_color(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})
	rows := strings.Split(m.statusTable(), "\n")

	assert.Contains(t, rows[3], roster()[0].Paint("bugs+impl"), "an ANSI-named color reaches the row")
	assert.Contains(t, rows[4], "\x1b[38;2;255;136;0m", "a hex color reaches it as truecolor")

	t.Run("names are padded before they are painted", func(t *testing.T) {
		// the color sequence has no width: padding after painting would count it as if it did and
		// leave the state column ragged
		plain := strings.Split(New(ModelConfig{Roster: []prompt.AgentSpec{
			{Name: "bugs+impl"}, {Name: "codex"},
		}}).statusTable(), "\n")
		assert.Equal(t, strings.Index(plain[3], "waiting"), strings.Index(plain[4], "waiting"))
	})
}

func TestModel_statusTable_model(t *testing.T) {
	withModel := []prompt.AgentSpec{
		{Name: "bugs+impl", Model: "gpt-5.6-luna"},
		{Name: "codex", Executor: "codex", Model: "gpt-5.6-sol"},
	}

	t.Run("shown when the roster resolved one", func(t *testing.T) {
		m := New(ModelConfig{Roster: withModel})
		rows := strings.Split(m.statusTable(), "\n")
		assert.Contains(t, rows[2], "MODEL", "the column heading names the new column")
		assert.Contains(t, rows[3], "gpt-5.6-luna")
		assert.Contains(t, rows[4], "gpt-5.6-sol")
	})

	t.Run("absent when nothing in the roster resolved a model", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		rows := strings.Split(m.statusTable(), "\n")
		assert.NotContains(t, rows[2], "MODEL", "an empty column is chrome with nothing in it")
	})

	t.Run("a synthesis row carries no model and renders a blank cell", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: withModel}),
			event(pipeline.EventAgentStarted, "synthesis", ""))
		rows := strings.Split(m.statusTable(), "\n")
		synthesis := rows[len(rows)-2] // the derived row is appended after the roster
		assert.Contains(t, synthesis, "synthesis")
		assert.NotContains(t, synthesis, "gpt-5.6", "a derived process has no resolved model to show")
	})

	t.Run("dropped first when the pane is too narrow for it", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: withModel}), tea.WindowSizeMsg{Width: 30, Height: 24})
		rows := strings.Split(m.statusTable(), "\n")
		assert.NotContains(t, rows[2], "MODEL")
		assert.LessOrEqual(t, lipgloss.Width(rows[2]), 30)
	})
}

func TestModel_statusTable_header(t *testing.T) {
	tests := []struct {
		name string
		msgs []tea.Msg
		want string
	}{
		{"nothing has happened yet", nil, "revmux · comprehensive · ctrl+c to quit"},
		{
			"a stage is named once it opens",
			[]tea.Msg{pipeline.Event{Kind: pipeline.EventStage, Stage: "synthesis", At: at}},
			"revmux · comprehensive · synthesis · ctrl+c to quit",
		},
		{
			"findings are counted across agents, and broken down by severity",
			[]tea.Msg{
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
					{Severity: finding.Critical}, {Severity: finding.Minor}}},
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "codex", At: at, Findings: []finding.Finding{
					{Severity: finding.Major}}},
			},
			"revmux · comprehensive · 3 findings (1 critical, 1 major, 1 minor) · ctrl+c to quit",
		},
		{
			"a severity the model invented counts toward the total and is named nowhere",
			[]tea.Msg{
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
					{Severity: finding.Major}, {Severity: "invented"}}},
			},
			"revmux · comprehensive · 2 findings (0 critical, 1 major, 0 minor) · ctrl+c to quit",
		},
		{
			"a synthesis row replaces the finders' total rather than adding to it",
			[]tea.Msg{
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
					{Severity: finding.Major}, {Severity: finding.Major}}},
				pipeline.Event{Kind: pipeline.EventFindings, Agent: "synthesis", At: at, Findings: []finding.Finding{
					{Severity: finding.Minor}}},
			},
			"revmux · comprehensive · 1 findings (0 critical, 0 major, 1 minor) · ctrl+c to quit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := feed(t, New(ModelConfig{Roster: roster(), Profile: "comprehensive"}), tt.msgs...)
			assert.Equal(t, tt.want, m.header())
		})
	}
}

func TestModel_header_quitHint(t *testing.T) {
	// q and esc are inert while a review runs, so a reader who tries them sees a frame that does not
	// change. The header is the only thing on screen that can say which key does work
	m := feed(t, New(ModelConfig{Roster: roster(), Profile: "comprehensive"}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "verify", At: at},
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Major}, {Severity: finding.Minor}}},
	)

	t.Run("a running review names the one key that ends it", func(t *testing.T) {
		wide := feed(t, m, tea.WindowSizeMsg{Width: 120, Height: 24}).header()
		assert.Contains(t, wide, "ctrl+c to quit")
		assert.Contains(t, wide, "(1 critical, 1 major, 1 minor)", "and it costs the run state nothing")
	})

	t.Run("the breakdown is spent before the hint", func(t *testing.T) {
		narrow := feed(t, m, tea.WindowSizeMsg{Width: 72, Height: 24}).header()
		assert.Contains(t, narrow, "ctrl+c to quit", "the hint has no shorter form, so it is not what pays for the width")
		assert.NotContains(t, narrow, "critical", "the breakdown does have one")
		assert.Contains(t, narrow, "3 findings", "and the total keeps the worst severity in its color")
	})

	t.Run("the hint goes once the total alone no longer leaves room", func(t *testing.T) {
		narrow := feed(t, m, tea.WindowSizeMsg{Width: 45, Height: 24}).header()
		assert.NotContains(t, narrow, "ctrl+c", "run state is what the header is for")
		assert.Contains(t, narrow, "3 findings")
	})

	t.Run("a completed run swaps the hint for the completion notice", func(t *testing.T) {
		done := feed(t, m, CompletedMsg{Report: report()}, tea.WindowSizeMsg{Width: 120, Height: 24}).header()
		assert.NotContains(t, done, "ctrl+c", "q is the key once the report is in")
		assert.Contains(t, done, "q to quit")
	})

	t.Run("the ladder a reader sees is the one that runs", func(t *testing.T) {
		live := m.headerLevels()
		require.True(t, live[0].hint, "the hint is offered from the widest rung")
		require.True(t, live[1].hint)
		assert.False(t, live[2].hint, "and given up under the breakdown")

		done := feed(t, m, CompletedMsg{Report: report()})
		levels := done.headerLevels()
		assert.Equal(t, countFull, levels[0].count, "the breakdown is still the widest rung")
		for i, l := range levels {
			assert.False(t, l.hint, "rung %d promises a hint a finished run never prints", i)
		}
		assert.Len(t, levels, len(live)-1, "and the rung that only differed by it is gone rather than a duplicate")
	})
}

func TestModel_header_quitHintOnARealRun(t *testing.T) {
	// the band the hint has to survive is the shipped comprehensive profile in a normal pane: its name
	// and a severity breakdown put the full line at 90 columns, so anything ordered below the breakdown
	// is gone from the first EventFindings on — which is the run this hint exists for
	big := []prompt.AgentSpec{{Name: "bugs+impl"}, {Name: "architecture"}, {Name: "quality+docs"}, {Name: "codex"}}
	m := feed(t, New(ModelConfig{Roster: big, Task: "pr-123", Run: "after-fix", Profile: "comprehensive", Inputs: []InputDocument{
		{Label: "scope", Path: "/tmp/scope.md", Content: "# scope", Markdown: true}}}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Critical},
			{Severity: finding.Major}, {Severity: finding.Major}, {Severity: finding.Major},
			{Severity: finding.Minor}, {Severity: finding.Minor}}},
	)

	for _, width := range []int{80, 90, 100} {
		t.Run("review mode at "+strconv.Itoa(width)+" columns", func(t *testing.T) {
			h := feed(t, m, tea.WindowSizeMsg{Width: width, Height: 24}).header()
			assert.Contains(t, h, "ctrl+c to quit")
			assert.Contains(t, h, "7 findings", "and the run state a reader came for is still on the line")
			assert.LessOrEqual(t, lipgloss.Width(h), width)
		})
	}

	// the input viewer prepends the mode and the task/run, which is where the margin is tightest — and
	// where esc is bound to leaving the pane rather than to quitting, so the key is least guessable
	inputs := feed(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, modeInputs, inputs.view.mode, "the case is only about the input viewer's own header")
	for _, width := range []int{90, 100} {
		t.Run("input viewer at "+strconv.Itoa(width)+" columns", func(t *testing.T) {
			h := feed(t, inputs, tea.WindowSizeMsg{Width: width, Height: 24}).header()
			assert.Contains(t, h, "ctrl+c to quit")
			assert.Contains(t, h, "pr-123/after-fix", "the round is what the input viewer's header adds")
			assert.LessOrEqual(t, lipgloss.Width(h), width)
		})
	}
}

func TestModel_header_degradesInsteadOfClipping(t *testing.T) {
	// the completion notice is the rightmost thing on the line, so clipping takes it first — and it is
	// the one part a reader needs at exactly the moment the line is longest
	full := feed(t, New(ModelConfig{Roster: roster(), Profile: "comprehensive"}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "verify", At: at},
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Major}, {Severity: finding.Minor}}},
		CompletedMsg{Report: finding.Report{Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Major}, {Severity: finding.Minor}}}},
	)

	for _, width := range []int{120, 70, 55, 40, 30, 12} {
		t.Run(strconv.Itoa(width)+" columns", func(t *testing.T) {
			m := feed(t, full, tea.WindowSizeMsg{Width: width, Height: 24})
			assert.LessOrEqual(t, lipgloss.Width(m.header()), width, "the header must fit rather than be cut")
		})
	}

	t.Run("the completion notice outlives everything the header can give up", func(t *testing.T) {
		// down to the width where the notice alone no longer fits, which is where clipping takes over
		for _, width := range []int{120, 70, 55, 40, 32} {
			m := feed(t, full, tea.WindowSizeMsg{Width: width, Height: 24})
			assert.Contains(t, m.header(), "complete", "at %d columns", width)
		}
	})

	t.Run("it gives up the breakdown before the notice", func(t *testing.T) {
		wide := feed(t, full, tea.WindowSizeMsg{Width: 120, Height: 24})
		assert.Contains(t, wide.header(), "(1 critical, 1 major, 1 minor)", "there is room for all of it")

		narrow := feed(t, full, tea.WindowSizeMsg{Width: 55, Height: 24})
		assert.NotContains(t, narrow.header(), "critical", "the longest part goes first")
		assert.Contains(t, narrow.header(), "3 findings", "but the total stays")
	})
}

func TestModel_header_ladderOrder(t *testing.T) {
	// the order is stated in .claude/rules/tui.md and was wrong once: the total outlives the stage,
	// because a reader who has lost the stage still learns whether anything was found, while a stage
	// name with no count says only that something is happening. Reverting that rung left every other
	// test green, so this is what holds it.
	m := feed(t, New(ModelConfig{Roster: roster(), Profile: "comprehensive"}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "verify", At: at},
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at, Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Major}, {Severity: finding.Minor}}},
	)

	// widths chosen to land on each rung in turn
	at120 := feed(t, m, tea.WindowSizeMsg{Width: 120, Height: 24}).header()
	assert.Contains(t, at120, "comprehensive")
	assert.Contains(t, at120, "verify")
	assert.Contains(t, at120, "(1 critical, 1 major, 1 minor)", "everything fits")

	at40 := feed(t, m, tea.WindowSizeMsg{Width: 40, Height: 24}).header()
	assert.NotContains(t, at40, "critical", "the breakdown went first, and the quit hint under it")
	assert.NotContains(t, at40, "ctrl+c")
	assert.Contains(t, at40, "3 findings", "the total is still here")

	at28 := feed(t, m, tea.WindowSizeMsg{Width: 28, Height: 24}).header()
	assert.NotContains(t, at28, "comprehensive", "then the profile")
	assert.Contains(t, at28, "3 findings")

	at20 := feed(t, m, tea.WindowSizeMsg{Width: 20, Height: 24}).header()
	assert.NotContains(t, at20, "verify", "then the stage")
	assert.Contains(t, at20, "3 findings", "and the total is the last thing to go")
}
