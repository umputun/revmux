package ui

import (
	"slices"
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

var at = time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)

// roster is the two-agent roster most cases render, one ANSI-named color and one hex.
func roster() []prompt.AgentSpec {
	return []prompt.AgentSpec{
		{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Color: "6"},
		{Name: "codex", Lenses: []string{"adversarial"}, Executor: "codex", Color: "#ff8800"},
	}
}

// feed drives Update the way bubbletea would and hands back the mutated model.
func feed(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		updated, ok := next.(Model)
		require.True(t, ok, "Update must return the same model type")
		m = updated
	}
	return m
}

// event builds one agent event at the shared timestamp.
func event(kind pipeline.EventKind, agent, text string) pipeline.Event {
	return pipeline.Event{Kind: kind, Agent: agent, Text: text, At: at}
}

func TestNew(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	require.Len(t, m.agents, 2)
	assert.Equal(t, "bugs+impl", m.agents[0].spec.Name, "rows follow roster order")
	assert.Equal(t, "codex", m.agents[1].spec.Name)
	assert.Equal(t, stateWaiting, m.agents[0].state)
	assert.Equal(t, 0, m.view.tab, "the combined view is focused by default")
	assert.NotNil(t, m.combined)
	assert.NotNil(t, m.md, "the document renderer is wired at construction, so nothing downstream guards nil")
	assert.Equal(t, m.style.profile, m.md.profile, "and it is profiled against the same surface as the frame")
	assert.Equal(t, mdStyle(m.style.dark), m.md.style,
		"and takes its palette from the same background, or a light terminal gets the dark one")

	t.Run("an empty roster still renders", func(t *testing.T) {
		empty := New(ModelConfig{})
		assert.Empty(t, empty.agents)
		assert.Contains(t, empty.View(), "0 agents")
	})
}

func TestModel_apply(t *testing.T) {
	tests := []struct {
		name      string
		ev        pipeline.Event
		wantState string
		wantLast  string
	}{
		{"started", event(pipeline.EventAgentStarted, "bugs+impl", "bugs, impl"), stateRunning, "started [bugs, impl]"},
		{"started with no lenses", event(pipeline.EventAgentStarted, "bugs+impl", ""), stateRunning, "started"},
		{"activity", event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"), stateRunning, "tool: Read"},
		{"state", event(pipeline.EventAgentState, "bugs+impl", "exit 0"), stateRunning, "exit 0"},
		{"done", event(pipeline.EventAgentDone, "bugs+impl", "3 findings"), stateDone, "done, 3 findings"},
		{"done with no detail", event(pipeline.EventAgentDone, "bugs+impl", ""), stateDone, "done"},
		{"retried", event(pipeline.EventAgentRetried, "bugs+impl", "idle timeout"), stateRetrying, "retrying: idle timeout"},
		{"degraded", event(pipeline.EventAgentDegraded, "bugs+impl", "stalled twice"), stateDegraded, "degraded: stalled twice"},
		{"rate limited", event(pipeline.EventRateLimit, "bugs+impl", "throttled"), stateLimited, "rate limited: throttled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := feed(t, New(ModelConfig{Roster: roster()}), tt.ev)
			assert.Equal(t, tt.wantState, m.agents[0].state)
			assert.Equal(t, tt.wantLast, m.agents[0].last)
			// the scrollback keeps the timestamp apart from the text, so the pane can wrap under its own
			// column; a joined string could not be re-split without parsing it back
			assert.Equal(t, []combinedEntry{{text: tt.wantLast, at: at}}, m.agents[0].lines)
			require.Len(t, m.combined.entries, 1)
			assert.Equal(t, combinedEntry{agent: "bugs+impl", text: tt.wantLast, at: at}, m.combined.entries[0])
		})
	}

	t.Run("findings count up without changing the state", func(t *testing.T) {
		ev := pipeline.Event{Kind: pipeline.EventFindings, Agent: "codex", At: at,
			Findings: []finding.Finding{{Title: "one"}, {Title: "two"}}}
		m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventAgentStarted, "codex", ""), ev)

		assert.Equal(t, 2, m.found.total)
		assert.Equal(t, stateRunning, m.agents[1].state, "emitting findings is not finishing")
		assert.Equal(t, "2 findings emitted", m.agents[1].last)
	})

	t.Run("a stage process replaces the count rather than adding to it", func(t *testing.T) {
		found := func(agent string, n int) pipeline.Event {
			list := make([]finding.Finding, n)
			return pipeline.Event{Kind: pipeline.EventFindings, Agent: agent, At: at, Findings: list}
		}
		m := feed(t, New(ModelConfig{Roster: roster()}),
			found("bugs+impl", 5), found("codex", 4), found("synthesis", 3))

		assert.Equal(t, 3, m.found.total, "synthesis merged what the finders reported, so its count supersedes theirs")
		assert.Contains(t, m.header(), "3 findings")
	})

	t.Run("a stage event names the stage and carries no agent", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}),
			pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at})

		assert.Equal(t, "find", m.stage)
		require.Len(t, m.combined.entries, 1)
		assert.Equal(t, combinedEntry{text: "find", at: at}, m.combined.entries[0])
		assert.Empty(t, m.agents[0].lines, "a stage change belongs to no agent")
	})

	t.Run("an unknown kind is dropped rather than rendered blank", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventKind("invented"), "bugs+impl", "x"))
		assert.Empty(t, m.combined.entries)
		assert.Empty(t, m.agents[0].lines)
		assert.Equal(t, stateWaiting, m.agents[0].state)
	})

	t.Run("an event with no agent is dropped", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventAgentActivity, "", "orphan"))
		assert.Len(t, m.agents, 2, "no row is opened for an unnamed agent")
		assert.Empty(t, m.combined.entries)
	})
}

func TestModel_apply_unknownAgent(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventAgentActivity, "stranger", "tool: Read"))

	require.Len(t, m.agents, 3, "an unrostered agent gets its own row rather than a panic")
	assert.Equal(t, "stranger", m.agents[2].spec.Name)
	assert.Equal(t, stateRunning, m.agents[2].state)

	var row string
	for l := range strings.SplitSeq(m.statusTable(), "\n") {
		if strings.Contains(l, "stranger") {
			row = l
		}
	}
	require.NotEmpty(t, row, "the unrostered agent still gets a status row")

	// a stage or verify group is unrostered but still worth telling apart in the combined log, so it
	// gets a color from prompt.DerivedSpec. This package must not pick one itself — the plain renderer
	// would then paint the same agent differently — so the assertion is that the row carries exactly
	// what prompt derived, never merely that it is colored
	// the name is padded before it is painted, so the row does not hold Paint("stranger") verbatim —
	// what it must hold is the sequence prompt resolved, applied to the name
	open, _, _ := strings.Cut(prompt.DerivedSpec("stranger").Paint("stranger"), "stranger")
	require.NotEmpty(t, open, "prompt must resolve a color for a derived agent")
	assert.Contains(t, row, open+"stranger",
		"the color comes from the spec, and both renderers take it from there")
	// padded out to the widest name like any other, so one unrostered agent does not break the column
	assert.Equal(t, "16:02:11 "+prompt.DerivedSpec("stranger").Paint("stranger")+"   tool: Read",
		m.combinedLines()[0])
}

func TestModel_apply_afterDone(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		event(pipeline.EventAgentDone, "bugs+impl", "3 findings"),
		event(pipeline.EventAgentActivity, "bugs+impl", "a late line"),
	)

	assert.Equal(t, "a late line", m.agents[0].last, "ordering across concurrent agents is not guaranteed")
	assert.Len(t, m.agents[0].lines, 2)
	assert.Len(t, m.agents, 2, "a late event does not open a second row")
}

func TestModel_Update(t *testing.T) {
	t.Run("a window size resizes the panes", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 40, Height: 12})
		assert.Equal(t, 40, m.view.width())
		assert.Equal(t, 12, m.view.height())
	})

	t.Run("a resize clamps a scroll offset the shorter pane can no longer reach", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		for i := range 40 {
			m = feed(t, m, event(pipeline.EventAgentActivity, "bugs+impl", "line "+strings.Repeat("x", i%3)))
		}
		m = feed(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
		require.Positive(t, m.view.scroll)

		m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: 200})
		assert.Equal(t, 0, m.view.scroll, "everything fits now, so there is nothing to scroll back to")
	})

	t.Run("an event asks for the next one", func(t *testing.T) {
		events := make(chan pipeline.Event, 1)
		m := New(ModelConfig{Roster: roster(), Events: events})
		_, cmd := m.Update(event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"))
		assert.NotNil(t, cmd, "the model keeps watching the channel")
	})

	t.Run("a closed channel does not quit, the report is still coming", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		next, cmd := m.Update(eventsDone{})
		assert.Nil(t, cmd, "quitting here would drop the findings browser before it opened")
		assert.Nil(t, next.(Model).findings)
	})

	t.Run("an unrelated message changes nothing", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		next, cmd := m.Update("something else")
		assert.Nil(t, cmd)
		assert.Equal(t, m.View(), next.(Model).View())
	})
}

// mdWidths is every width the document renderer currently holds a renderer for.
func mdWidths(m Model) []int {
	out := make([]int, 0, len(m.md.renderers))
	for w := range m.md.renderers {
		out = append(out, w)
	}
	slices.Sort(out)
	return out
}

func TestModel_Update_resizeEvictsDocuments(t *testing.T) {
	doc := InputDocument{Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "| col | other |\n|---|---|\n| a | b |\n\n" + strings.Repeat("word ", 60)}
	sized := func(t *testing.T, cols int) Model {
		t.Helper()
		m := New(ModelConfig{Roster: roster(), Inputs: []InputDocument{doc}})
		return feed(t, m, tea.WindowSizeMsg{Width: cols, Height: 40}, CompletedMsg{Report: report()})
	}
	sentinel := func(m Model, width int) mdCacheKey {
		k := mdCacheKey{key: "sentinel", width: width}
		m.md.cache[k] = []string{"x"}
		return k
	}

	t.Run("a width change drops the renderers and the cache", func(t *testing.T) {
		m := sized(t, 100)
		require.NotEmpty(t, m.md.cache, "the completed report renders its documents")
		mark := sentinel(m, 100)

		m = feed(t, m, tea.WindowSizeMsg{Width: 60, Height: 40})
		assert.NotContains(t, m.md.cache, mark, "the old width is not kept around")
		for k := range m.md.cache {
			assert.LessOrEqual(t, k.width, 60, "nothing cached at a width the terminal no longer has")
		}
		for _, w := range mdWidths(m) {
			assert.LessOrEqual(t, w, 60)
		}
		for _, line := range m.paneLines() {
			assert.LessOrEqual(t, lipgloss.Width(line), 60, "and the pane re-wrapped to the new width")
		}
	})

	t.Run("a height-only resize keeps the cache", func(t *testing.T) {
		m := sized(t, 100)
		mark := sentinel(m, 100)

		m = feed(t, m, tea.WindowSizeMsg{Width: 100, Height: 20})
		assert.Contains(t, m.md.cache, mark,
			"a drag-resize delivers a stream of these and maxScroll re-renders every document on each one")
	})

	t.Run("a repeated identical resize keeps the cache", func(t *testing.T) {
		m := sized(t, 100)
		mark := sentinel(m, 100)

		m = feed(t, m, tea.WindowSizeMsg{Width: 100, Height: 40}, tea.WindowSizeMsg{Width: 100, Height: 40})
		assert.Contains(t, m.md.cache, mark)
	})

	t.Run("switching panes keeps both widths resident", func(t *testing.T) {
		m := sized(t, 100)
		require.Equal(t, []int{100 - mdIndent}, mdWidths(m), "the browser renders at the padded width")

		m = feed(t, m, press("i"))
		require.Equal(t, []int{100 - mdIndent, 100}, mdWidths(m), "and the input viewer at the full one")

		m = feed(t, m, press("i"), press("i"))
		assert.Equal(t, []int{100 - mdIndent, 100}, mdWidths(m), "switching panes is not a width change")
	})

	t.Run("a large report resizes without leaving the old width behind", func(t *testing.T) {
		rep := finding.Report{}
		for i := range 60 {
			rep.Findings = append(rep.Findings, finding.Finding{
				ID: "f" + strconv.Itoa(i), File: "app/ui/view.go", Line: i, Severity: finding.Major, Confidence: 70,
				Title: "finding " + strconv.Itoa(i), Body: "- one\n- two\n\n" + strings.Repeat("prose ", 40),
				Fix: "```go\nx := 1\n```",
			})
		}
		m := feed(t, New(ModelConfig{Roster: roster()}),
			tea.WindowSizeMsg{Width: 120, Height: 40}, CompletedMsg{Report: rep})
		require.Len(t, m.md.cache, 2*len(rep.Findings), "one body and one fix per finding")

		m = feed(t, m, tea.WindowSizeMsg{Width: 70, Height: 40})
		assert.Equal(t, []int{70 - mdIndent}, mdWidths(m), "only the live width survives")
		assert.Len(t, m.md.cache, 2*len(rep.Findings), "and the report is cached again rather than re-rendered per line")
		for _, line := range m.paneLines() {
			assert.LessOrEqual(t, lipgloss.Width(line), 70)
		}
	})
}

func TestModel_Update_resizeKeepsScrollAndFilter(t *testing.T) {
	t.Run("scroll stays in range when a wider terminal shortens the document", func(t *testing.T) {
		m := browsed(t, report())
		m = feed(t, m, tea.WindowSizeMsg{Width: 40, Height: len(roster()) + chromeLines + 5}, press("g"))
		require.Positive(t, m.view.scroll, "the report is longer than the pane at 40 columns")

		m = feed(t, m, tea.WindowSizeMsg{Width: 160, Height: len(roster()) + chromeLines + 5})
		assert.LessOrEqual(t, m.view.scroll, m.maxScroll(), "a shorter document cannot leave the offset past its end")
	})

	t.Run("maxScroll counts rendered lines, not source lines", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster(), Inputs: []InputDocument{
			{Label: "scope", Path: "input/scope.md", Markdown: true, Content: strings.Repeat("word ", 200)},
		}})
		m = feed(t, m, tea.WindowSizeMsg{Width: 60, Height: 20}, press("i"))

		assert.Equal(t, max(0, len(m.paneLines())-m.paneHeight()), m.maxScroll())
		assert.Greater(t, len(m.paneLines()), 1, "one source line renders as many wrapped ones")
	})

	t.Run("the filter still narrows after a resize", func(t *testing.T) {
		m := browsed(t, report())
		m = feed(t, m, press("/"), press("s"), press("t"), press("a"), press("l"), press("e"), press("enter"))
		require.Len(t, m.findings.matches, 1)

		m = feed(t, m, tea.WindowSizeMsg{Width: 60, Height: 40})
		require.Len(t, m.findings.matches, 1, "a resize does not touch the query")
		pane := strings.Join(m.findings.render(m.view.width()), "\n")
		assert.Contains(t, pane, "stale comment")
		assert.NotContains(t, pane, "unchecked error", "and no cached body from another finding is served")
	})
}

func TestModel_Init(t *testing.T) {
	t.Run("delivers events until the channel closes", func(t *testing.T) {
		events := make(chan pipeline.Event, 1)
		events <- event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read")
		m := New(ModelConfig{Roster: roster(), Events: events})

		cmd := m.Init()
		require.NotNil(t, cmd)
		assert.Equal(t, event(pipeline.EventAgentActivity, "bugs+impl", "tool: Read"), cmd())

		close(events)
		assert.Equal(t, eventsDone{}, cmd())
	})

	t.Run("no channel means nothing to wait for", func(t *testing.T) {
		assert.Nil(t, New(ModelConfig{Roster: roster()}).Init())
	})
}

func TestAgentState_push(t *testing.T) {
	a := &agentState{}
	for i := range scrollbackLimit + 10 {
		a.push(combinedEntry{at: at, text: "line " + strings.Repeat("x", i%2)})
	}

	assert.Len(t, a.lines, scrollbackLimit, "the oldest lines are dropped, the pane stays bounded")
}

func TestAgentState_runtime(t *testing.T) {
	tests := []struct {
		name             string
		started, updated time.Time
		want             string
	}{
		{"never started", time.Time{}, time.Time{}, "-"},
		{"nothing since it started", at, at, "-"},
		{"sub-second rounds away", at, at.Add(400 * time.Millisecond), "-"},
		{"seconds", at, at.Add(12 * time.Second), "12s"},
		{"minutes", at, at.Add(62 * time.Second), "1m2s"},
		{"an out-of-order timestamp is not negative time", at, at.Add(-time.Minute), "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &agentState{started: tt.started, updated: tt.updated}
			// its own last event as "now" — the elapsed a quiet agent shows when nothing else is running
			assert.Equal(t, tt.want, a.runtime(tt.updated))
		})
	}
}

func TestModel_autoExit(t *testing.T) {
	t.Run("without the option the browser waits for a keypress", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster()})
		next, cmd := m.Update(CompletedMsg{Report: report()})
		m = next.(Model)
		assert.True(t, m.done)
		assert.Zero(t, m.exitIn, "nothing is counting down")
		assert.Nil(t, cmd, "and no tick was scheduled")
	})

	t.Run("the countdown runs down a second per tick and then quits", func(t *testing.T) {
		m := New(ModelConfig{Roster: roster(), AutoExit: 3 * time.Second})
		next, cmd := m.Update(CompletedMsg{Report: report()})
		m = next.(Model)
		require.Equal(t, 3*time.Second, m.exitIn)
		require.NotNil(t, cmd, "completion schedules the first tick")

		for want := 2 * time.Second; want > 0; want -= time.Second {
			next, cmd = m.Update(exitTick{})
			m = next.(Model)
			require.Equal(t, want, m.exitIn)
			require.NotNil(t, cmd, "each tick schedules the next one")
		}

		next, cmd = m.Update(exitTick{})
		m = next.(Model)
		assert.LessOrEqual(t, m.exitIn, time.Duration(0))
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd(), "the last tick quits")
	})

	t.Run("a keypress cancels it, and a tick already in flight does not quit", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster(), AutoExit: time.Second}), CompletedMsg{Report: report()})
		m = feed(t, m, press("j"))
		assert.Zero(t, m.exitIn, "the reader is reading")

		_, cmd := m.Update(exitTick{})
		assert.Nil(t, cmd, "the stale tick neither quits nor reschedules")
	})
}

func TestModel_closing(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})
	assert.Empty(t, m.closing(), "a running review says nothing about closing")

	t.Run("a finished review says it is finished and how to leave", func(t *testing.T) {
		done := feed(t, m, CompletedMsg{Report: report()})
		assert.Contains(t, done.View(), "complete")
		assert.Contains(t, done.View(), "q to quit")
	})

	t.Run("a counting-down one shows the seconds left and how to stop it", func(t *testing.T) {
		auto := feed(t, New(ModelConfig{Roster: roster(), AutoExit: 5 * time.Second}), CompletedMsg{Report: report()})
		assert.Contains(t, auto.View(), "closing in 5s")
		assert.Contains(t, auto.View(), "any key to stay")
	})
}

func TestAgentState_runtime_advancesWithTheRun(t *testing.T) {
	// **an agent's own last event freezes its clock the moment it goes quiet**, which is exactly when
	// a reader wants to know how long it has been quiet for. Measuring against the newest event time
	// anywhere in the run makes every row advance whenever anything happens.
	quiet := &agentState{started: at, updated: at.Add(5 * time.Second)}
	assert.Equal(t, "5s", quiet.runtime(at.Add(5*time.Second)), "nothing else has happened yet")
	assert.Equal(t, "40s", quiet.runtime(at.Add(40*time.Second)),
		"another agent spoke at 40s, so this row shows how long this one has been silent")

	t.Run("a stale now never rewinds a row", func(t *testing.T) {
		// events from concurrent agents arrive out of order, so now can lag a row's own last event
		assert.Equal(t, "5s", quiet.runtime(at.Add(time.Second)))
	})

	t.Run("the model tracks the newest event time it has seen", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}),
			event(pipeline.EventAgentStarted, "bugs+impl", "bugs"),
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "reading", At: at.Add(time.Minute)},
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "older", At: at.Add(time.Second)},
		)
		assert.Equal(t, at.Add(time.Minute), m.now, "an out-of-order event must not wind the run back")

		var row string
		for l := range strings.SplitSeq(m.statusTable(), "\n") {
			if strings.Contains(l, "bugs+impl") {
				row = l
			}
		}
		assert.Contains(t, row, "1m0s", "the quiet agent's row advanced because another one spoke")
	})
}

func TestAgentState_runtime_freezesWhenFinished(t *testing.T) {
	// **a finished agent's elapsed must stop.** Measuring against the run's newest event makes every
	// completed row climb toward the run's age, so a reader can no longer tell the finder that took
	// forty seconds from the one that took four minutes — they converge.
	for _, state := range []string{stateDone, stateDegraded} {
		t.Run(state+" stops counting", func(t *testing.T) {
			a := &agentState{state: state, started: at, updated: at.Add(40 * time.Second)}
			assert.Equal(t, "40s", a.runtime(at.Add(40*time.Second)))
			assert.Equal(t, "40s", a.runtime(at.Add(4*time.Minute)),
				"the rest of the run went on without it, and its own elapsed is what it took")
		})
	}

	for _, state := range []string{stateRunning, stateRetrying, stateLimited, stateWaiting} {
		t.Run(state+" keeps counting", func(t *testing.T) {
			a := &agentState{state: state, started: at, updated: at.Add(40 * time.Second)}
			assert.Equal(t, "4m0s", a.runtime(at.Add(4*time.Minute)),
				"it has not finished, so how long it has been quiet is the number worth showing")
		})
	}

	t.Run("through a whole run, in the table", func(t *testing.T) {
		m := feed(t, New(ModelConfig{Roster: roster()}),
			event(pipeline.EventAgentStarted, "bugs+impl", "bugs"),
			event(pipeline.EventAgentStarted, "codex", "adversarial"),
			pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "bugs+impl", At: at.Add(40 * time.Second)},
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "still going",
				At: at.Add(4 * time.Minute)},
		)

		rows := map[string]string{}
		for l := range strings.SplitSeq(m.statusTable(), "\n") {
			for _, name := range []string{"bugs+impl", "codex"} {
				if strings.Contains(l, name) && !strings.Contains(l, "AGENT") {
					rows[name] = l
				}
			}
		}
		assert.Contains(t, rows["bugs+impl"], "40s", "the finished row holds the time it actually took")
		assert.Contains(t, rows["codex"], "4m0s", "while the one still going keeps counting")
	})
}

func TestModel_complete_rebuildsTheTally(t *testing.T) {
	// verify rejects findings after synthesis emitted its count, and --min-confidence filters after
	// that, so the header must take its number from the report it was handed rather than from the last
	// event it happened to see
	m := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "synthesis", At: at, Findings: []finding.Finding{
			{Severity: finding.Critical}, {Severity: finding.Major}, {Severity: finding.Minor},
		}},
	)
	require.Equal(t, 3, m.found.total, "synthesis reported three")

	done := feed(t, m, CompletedMsg{Report: finding.Report{Findings: []finding.Finding{
		{Severity: finding.Major}, {Severity: finding.Minor},
	}}})
	assert.Equal(t, 2, done.found.total, "verify rejected one, and the header says so")
	assert.Equal(t, 0, done.found.critical, "the rejected one was the critical, and it is gone from the count")
	assert.Contains(t, done.header(), "2 findings (0 critical, 1 major, 1 minor)")

	t.Run("an empty report empties the count rather than leaving the old one", func(t *testing.T) {
		none := feed(t, m, CompletedMsg{Report: finding.Report{}})
		assert.Equal(t, 0, none.found.total)
		assert.NotContains(t, none.header(), "findings", "nothing survived, so the header says nothing about them")
	})
}
