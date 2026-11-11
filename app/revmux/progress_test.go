package main

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/ui"
)

var progressAt = time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)

// painted renders an agent name the way line does for a process the roster does not carry, so a case
// below states what it is checking rather than a run of escape codes.
func painted(agent string) string { return prompt.DerivedSpec(agent).Paint(agent) }

func TestProgress_line(t *testing.T) {
	tests := []struct {
		name string
		ev   pipeline.Event
		want string
	}{
		{name: "stage", ev: pipeline.Event{Kind: pipeline.EventStage, Stage: "find"}, want: "16:02:11 ── find ──"},
		{
			name: "agent started names its lenses",
			ev:   pipeline.Event{Kind: pipeline.EventAgentStarted, Agent: "bugs+impl", Text: "bugs, impl"},
			want: "16:02:11 " + painted("bugs+impl") + "  started [bugs, impl]",
		},
		{
			name: "agent started with no lenses",
			ev:   pipeline.Event{Kind: pipeline.EventAgentStarted, Agent: "solo"},
			want: "16:02:11 " + painted("solo") + "  started",
		},
		{
			name: "activity", ev: pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs", Text: "tool: Read"},
			want: "16:02:11 " + painted("bugs") + "  tool: Read",
		},
		{
			name: "state", ev: pipeline.Event{Kind: pipeline.EventAgentState, Agent: "bugs", Text: "exit 0"},
			want: "16:02:11 " + painted("bugs") + "  exit 0",
		},
		{
			name: "done", ev: pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "bugs", Text: "3 findings"},
			want: "16:02:11 " + painted("bugs") + "  done, 3 findings",
		},
		{name: "done with no detail", ev: pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "bugs"}, want: "16:02:11 " + painted("bugs") + "  done"},
		{
			name: "retried", ev: pipeline.Event{Kind: pipeline.EventAgentRetried, Agent: "bugs", Text: "idle timeout"},
			want: "16:02:11 " + painted("bugs") + "  retrying: idle timeout",
		},
		{
			name: "degraded", ev: pipeline.Event{Kind: pipeline.EventAgentDegraded, Agent: "bugs", Text: "stalled twice"},
			want: "16:02:11 " + painted("bugs") + "  degraded: stalled twice",
		},
		{
			// dropped: the done event a moment later carries the same count
			name: "findings", ev: pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs", Findings: []finding.Finding{{}, {}}},
			want: "",
		},
		{
			name: "rate limit", ev: pipeline.Event{Kind: pipeline.EventRateLimit, Agent: "bugs", Text: "throttled"},
			want: "16:02:11 " + painted("bugs") + "  rate limited: throttled",
		},
		{name: "unknown kind renders nothing", ev: pipeline.Event{Kind: pipeline.EventKind("invented")}, want: ""},
	}

	pr := &progress{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := tt.ev
			ev.At = progressAt
			assert.Equal(t, tt.want, pr.line(ev))
		})
	}
}

func TestProgress_paint(t *testing.T) {
	roster := []prompt.AgentSpec{{Name: "bugs+impl", Color: "6"}, {Name: "codex", Color: "#ff8800"}}
	pr := &progress{roster: roster}

	// every name here is narrower than the floor derived names need, so the column is that floor and
	// each line pads from it. Stated as the rule rather than as counted spaces, which is what let the
	// floor change break these cases rather than prove them.
	require.Less(t, lipgloss.Width("bugs+impl"), minNameWidth, "the roster must be the narrow case")
	col := func(name string) string { return strings.Repeat(" ", minNameWidth-lipgloss.Width(name)+2) }

	tests := []struct {
		name string
		ev   pipeline.Event
		want string
	}{
		{
			"the agent's own color prefixes its line",
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs+impl", Text: "tool: Read", At: progressAt},
			"16:02:11 \x1b[36mbugs+impl\x1b[39m" + col("bugs+impl") + "tool: Read",
		},
		{
			"a hex color reaches the line as truecolor, padded out to the column",
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "tool: Grep", At: progressAt},
			"16:02:11 \x1b[38;2;255;136;0mcodex\x1b[39m" + col("codex") + "tool: Grep",
		},
		{
			"an agent the roster does not name is colored from the same place the TUI colors it",
			pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "stranger", Text: "tool: Read", At: progressAt},
			"16:02:11 " + painted("stranger") + col("stranger") + "tool: Read",
		},
		{
			"a stage change belongs to no agent and is indented to the same column",
			pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: progressAt},
			"16:02:11 " + col("") + "── find ──",
		},
		{
			"a derived verify group fits the column a short roster would not have given it",
			pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "verify root", Text: "5 of 5 kept", At: progressAt},
			"16:02:11 " + painted("verify root") + col("verify root") + "done, 5 of 5 kept",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pr.line(tt.ev))
		})
	}
}

// the roster carries the resolved color, so a reviewer switching renderers sees the same agent the
// same way. A color picked inside either renderer would exist in that one only.
func TestProgress_colorsAgreeWithTheTUI(t *testing.T) {
	roster := []prompt.AgentSpec{{Name: "bugs+impl", Color: "6"}, {Name: "codex", Color: "#ff8800"}}
	ev := pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "tool: Grep", At: progressAt}

	plain := (&progress{roster: roster}).line(ev)

	m := ui.New(ui.ModelConfig{Roster: roster})
	tui, _ := m.Update(ev)
	rendered := tui.(ui.Model).View()

	prefix := roster[1].Paint("codex")
	assert.Contains(t, plain, prefix)
	assert.Contains(t, rendered, prefix)
}

func TestProgress_run(t *testing.T) {
	t.Run("renders the whole sequence and stops on close", func(t *testing.T) {
		events := make(chan pipeline.Event, 4)
		for _, ev := range []pipeline.Event{
			{Kind: pipeline.EventStage, Stage: "find", At: progressAt},
			{Kind: pipeline.EventAgentStarted, Agent: "bugs", Text: "bugs", At: progressAt},
			{Kind: pipeline.EventKind("invented"), At: progressAt},
			{Kind: pipeline.EventAgentDone, Agent: "bugs", Text: "1 findings", At: progressAt},
		} {
			events <- ev
		}
		close(events)

		out := &strings.Builder{}
		pr := &progress{w: out}
		pr.run(events)

		assert.Equal(t, []string{
			"16:02:11 ── find ──",
			"16:02:11 " + painted("bugs") + "  started [bugs]",
			"16:02:11 " + painted("bugs") + "  done, 1 findings",
		}, strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"))
	})

	t.Run("an already closed channel writes nothing", func(t *testing.T) {
		events := make(chan pipeline.Event)
		close(events)

		out := &strings.Builder{}
		pr := &progress{w: out}
		pr.run(events)
		assert.Empty(t, out.String())
	})
}

func TestProgress_line_progressIsThrottled(t *testing.T) {
	pr := &progress{roster: []prompt.AgentSpec{{Name: "bugs"}}}
	beat := func(text string, d time.Duration) string {
		return pr.line(pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs",
			Text: text, At: progressAt.Add(d)})
	}

	assert.Contains(t, beat("Read", 0), "Read", "the first one shows life immediately")
	assert.Empty(t, beat("Read", time.Second), "the burst behind it is held back")
	assert.Empty(t, beat("Grep", 2*time.Second))
	assert.Contains(t, beat("Bash", ui.ProgressInterval), "Bash", "and it says so again once the interval passes")

	t.Run("prose resets the clock so a talker is never charged a tool line", func(t *testing.T) {
		fresh := &progress{roster: []prompt.AgentSpec{{Name: "codex"}}}
		// codex narrates a reasoning headline every few seconds; charging it a tool line on top is
		// what filled the log with "exec"
		assert.NotEmpty(t, fresh.line(pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex",
			Text: "Planning the diff", At: progressAt}))
		assert.Empty(t, fresh.line(pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "codex",
			Text: "exec", At: progressAt.Add(ui.ProgressInterval - time.Second)}),
			"still inside the interval measured from the prose")
	})

	t.Run("a real tool call outranks bare thinking", func(t *testing.T) {
		fresh := &progress{roster: []prompt.AgentSpec{{Name: "bugs"}}}
		// a line first, so the two below fall inside one interval instead of the first firing at once
		fresh.line(pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs", Text: "starting", At: progressAt})
		fresh.line(pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs",
			Text: "Read proc.go", At: progressAt.Add(time.Second)})
		out := fresh.line(pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs",
			Text: "Read proc.go", At: progressAt.Add(2 * ui.ProgressInterval)})
		assert.Contains(t, out, "Read proc.go", "the heartbeat reports what the agent is reading")
	})

	t.Run("prose is never throttled", func(t *testing.T) {
		for i := range 3 {
			line := pr.line(pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs",
				Text: "finding " + strconv.Itoa(i), At: progressAt})
			assert.Contains(t, line, "finding "+strconv.Itoa(i))
		}
	})

	t.Run("each agent is throttled on its own clock", func(t *testing.T) {
		other := pr.line(pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "codex",
			Text: "Read", At: progressAt.Add(time.Second)})
		assert.Contains(t, other, "Read", "one agent's burst must not silence another's first sign of life")
	})
}

// the column is padded in display cells, and every ASCII name has as many cells as runes — so only a
// name where the two differ can tell the two implementations apart. Without a case like this, reverting
// nameWidth to len([]rune(...)) leaves the rest of this file green.
func TestProgress_alignsByDisplayCellsNotRunes(t *testing.T) {
	// textCol is the column a line's text starts at, measured the way a terminal measures it: the
	// prefix carries color escapes, which occupy bytes and no cells.
	textCol := func(t *testing.T, line, text string) int {
		t.Helper()
		at := strings.Index(line, text)
		require.GreaterOrEqual(t, at, 0, "the line must carry its text")
		return lipgloss.Width(line[:at])
	}

	// column renders one roster's agent line and its agentless stage line, and returns where each starts
	column := func(t *testing.T, agent string) (agentCol, stageCol int) {
		t.Helper()
		pr := &progress{roster: []prompt.AgentSpec{{Name: agent}}}
		agentLine := pr.line(pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: agent,
			Text: "tool: Read", At: progressAt})
		stageLine := pr.line(pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: progressAt})
		return textCol(t, agentLine, "tool: Read"), textCol(t, stageLine, "\u2500\u2500 find \u2500\u2500")
	}

	tests := []struct {
		name  string
		agent string
		ascii string // an ASCII name of the same display width, which must land in the same column
	}{
		{"a CJK name is twice as many cells as runes", "\u6570\u636e\u5ba1\u67e5\u4ee3\u7406", "abcdefghijkl"},
		{"a decomposed name has more runes than cells", "e\u0301clair-e\u0301quipe", "abcdefghijklm"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COLUMNS", "200")
			// the floor swallows any name narrower than it, so runes != cells is necessary but not
			// sufficient: what has to differ is the width nameWidth would actually return. A 7-rune,
			// 6-cell name floors to 11 either way and proves nothing, which is why none is used here.
			require.NotEqual(t,
				max(len([]rune(tc.agent)), minNameWidth), max(lipgloss.Width(tc.agent), minNameWidth),
				"a name the floor swallows cannot tell the two implementations apart")
			require.Equal(t, lipgloss.Width(tc.agent), lipgloss.Width(tc.ascii), "same display width")

			agentCol, stageCol := column(t, tc.agent)
			assert.Equal(t, stageCol, agentCol, "an agent line and an agentless one start at one column")

			// comparing a line only against its own stage line is blind to a width that is wrong for
			// both: a nameWidth of max(cells, runes) over-pads this name and shifts them together. The
			// ASCII name of equal display width is the fixed reference that catches it.
			asciiCol, _ := column(t, tc.ascii)
			assert.Equal(t, asciiCol, agentCol,
				"two names of one display width start at one column, whatever their rune counts")
		})
	}
}

func TestProgress_clipsToTerminalWidth(t *testing.T) {
	progRoster := []prompt.AgentSpec{{Name: "bugs", Color: "6"}}
	// the other half of "display width belongs to the renderers". The TUI clips against a width
	// bubbletea reports; nothing reports one here, so without this the decoder's sanity bound — runes
	// in the thousands, not a column count — is what reaches stderr.
	long := strings.Repeat("wide ", 60)
	ev := pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs", Text: long, At: progressAt}

	rows := func(t *testing.T, out string) []string {
		t.Helper()
		got := strings.Split(out, "\n")
		for _, r := range got {
			assert.LessOrEqual(t, lipgloss.Width(r), 60, "no row may run past the terminal")
		}
		return got
	}

	t.Run("a long entry wraps rather than losing its tail", func(t *testing.T) {
		t.Setenv("COLUMNS", "60")
		got := rows(t, (&progress{roster: progRoster}).line(ev))
		require.Greater(t, len(got), 1, "one row could not hold it")
		assert.Contains(t, got[0], "bugs", "the first row carries the timestamp and the agent")
		assert.NotContains(t, got[1], "16:02:11", "and the rest carry neither, so the entry reads as one thing")
		assert.True(t, strings.HasPrefix(got[1], "  "), "continuing under the text column, not the timestamp")

		joined := strings.Join(strings.Fields(strings.Join(got, " ")), " ")
		assert.Contains(t, joined, strings.TrimSpace(long), "every word survives the wrap")
	})

	t.Run("a width below the floor is ignored rather than obeyed", func(t *testing.T) {
		t.Setenv("COLUMNS", "3")
		out := (&progress{roster: progRoster}).line(ev)
		assert.Greater(t, lipgloss.Width(strings.Split(out, "\n")[0]), 3,
			"a 3-column line would be all prefix and no content")
	})

	t.Run("an unmeasurable writer falls back rather than failing", func(t *testing.T) {
		t.Setenv("COLUMNS", "")
		// a bytes.Buffer is not a terminal and has no width to ask for, which is what a redirected
		// stderr or a CI log looks like
		out := (&progress{w: &bytes.Buffer{}, roster: progRoster}).line(ev)
		for r := range strings.SplitSeq(out, "\n") {
			assert.LessOrEqual(t, lipgloss.Width(r), fallbackCols)
		}
	})

	t.Run("a short line is left alone", func(t *testing.T) {
		t.Setenv("COLUMNS", "200")
		short := pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs", Text: "reading proc.go", At: progressAt}
		out := (&progress{roster: progRoster}).line(short)
		assert.NotContains(t, out, "\n", "one row is enough, so it stays one row")
		assert.Contains(t, out, "reading proc.go")
	})
}

func TestProgress_finish(t *testing.T) {
	finishedAt := time.Date(2026, 7, 26, 16, 7, 28, 0, time.UTC)
	base := finding.Report{
		Sources: finding.SourceStatus{Expected: 2, Reported: 2},
		Stats:   finding.Stats{FinishedAt: finishedAt, DurationMS: 291000},
	}
	// "bugs" is narrower than the floor the column takes for derived names, so the indent is the floor
	indent := strings.Repeat(" ", minNameWidth+2)

	tests := []struct {
		name string
		rep  func(finding.Report) finding.Report
		want []string
	}{
		{
			// the findings carry title, body and location on purpose: the exact-line equality below is
			// what proves none of that text reaches stderr, and title-less findings would prove nothing
			name: "a clean run reports duration, sources and counts, and no finding text",
			rep: func(r finding.Report) finding.Report {
				r.Findings = []finding.Finding{
					{Severity: finding.Minor, Title: "unchecked error", Body: "Close is dropped", File: "proc.go"},
					{Severity: finding.Minor, Title: "stale comment", Body: "says the old behavior", File: "ui.go"},
					{Severity: finding.Major, Title: "lost report", Body: "exits 2 over a live round", File: "main.go"},
				}
				return r
			},
			want: []string{
				"16:07:28 " + indent + "── complete ──",
				"16:07:28 " + indent + "4m51s, sources 2/2, degraded none",
				"16:07:28 " + indent + "3 findings: 1 major, 2 minor",
			},
		},
		{
			// only the claude path has a schema constraining severity, so a codex peer on a --no-synthesis
			// run can answer anything. counting it under its own name keeps the parts summing to the total
			name: "a severity outside the enum is counted under its own name, not dropped",
			rep: func(r finding.Report) finding.Report {
				r.Findings = []finding.Finding{{Severity: finding.Major}, {Severity: "high"}, {Severity: "high"}}
				return r
			},
			want: []string{
				"16:07:28 " + indent + "── complete ──",
				"16:07:28 " + indent + "4m51s, sources 2/2, degraded none",
				"16:07:28 " + indent + "3 findings: 1 major, 2 high",
			},
		},
		{
			name: "an empty severity is labeled rather than printed as a bare count",
			rep: func(r finding.Report) finding.Report {
				r.Findings = []finding.Finding{{Severity: ""}}
				return r
			},
			want: []string{
				"16:07:28 " + indent + "── complete ──",
				"16:07:28 " + indent + "4m51s, sources 2/2, degraded none",
				"16:07:28 " + indent + "1 findings: 1 unlabeled",
			},
		},
		{
			name: "no findings says so rather than printing an empty tally",
			rep:  func(r finding.Report) finding.Report { return r },
			want: []string{
				"16:07:28 " + indent + "── complete ──",
				"16:07:28 " + indent + "4m51s, sources 2/2, degraded none",
				"16:07:28 " + indent + "no findings",
			},
		},
		{
			name: "a degraded source is named, never counted as reported",
			rep: func(r finding.Report) finding.Report {
				r.Sources = finding.SourceStatus{Expected: 2, Reported: 1, DegradedSources: []string{"codex"}}
				r.Findings = []finding.Finding{{Severity: finding.Critical}}
				return r
			},
			want: []string{
				"16:07:28 " + indent + "── complete ──",
				"16:07:28 " + indent + "4m51s, sources 1/2, DEGRADED: codex",
				"16:07:28 " + indent + "1 findings: 1 critical",
			},
		},
		{
			name: "a source degraded only by its own flag is named too",
			rep: func(r finding.Report) finding.Report {
				r.Sources = finding.SourceStatus{Expected: 2, Reported: 1,
					Agents: []finding.SourceStat{{Name: "bugs"}, {Name: "codex", Degraded: true}}}
				return r
			},
			want: []string{
				"16:07:28 " + indent + "── complete ──",
				"16:07:28 " + indent + "4m51s, sources 1/2, DEGRADED: codex",
				"16:07:28 " + indent + "no findings",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COLUMNS", "200")
			out := &strings.Builder{}
			(&progress{w: out, roster: []prompt.AgentSpec{{Name: "bugs"}}}).finish(tc.rep(base), nil)
			assert.Equal(t, tc.want, strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"))
		})
	}

	t.Run("a failed run prints nothing, since its error is already on stderr", func(t *testing.T) {
		out := &strings.Builder{}
		(&progress{w: out}).finish(base, errors.New("review failed"))
		assert.Empty(t, out.String())
	})

	t.Run("the summary lines up with the event lines above them", func(t *testing.T) {
		t.Setenv("COLUMNS", "200")
		roster := []prompt.AgentSpec{{Name: "arch+quality"}}
		out := &strings.Builder{}
		pr := &progress{w: out, roster: roster}
		ev := pr.line(pipeline.Event{Kind: pipeline.EventStage, Stage: "verify", At: progressAt})
		pr.finish(base, nil)

		summary, _, _ := strings.Cut(out.String(), "\n")
		assert.Equal(t, strings.Index(ev, "──"), strings.Index(summary, "──"),
			"a stage banner and the closing banner start at one column")
	})
}
