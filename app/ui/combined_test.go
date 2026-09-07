package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
)

func TestModel_combinedLines(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		event(pipeline.EventAgentStarted, "bugs+impl", "bugs, impl"),
		pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "codex", Text: "tool: Grep", At: at.Add(time.Second)},
		pipeline.Event{Kind: pipeline.EventFindings, Agent: "bugs+impl", At: at.Add(2 * time.Second),
			Findings: []finding.Finding{{Title: "one"}}},
		pipeline.Event{Kind: pipeline.EventAgentDone, Agent: "bugs+impl", Text: "1 findings", At: at.Add(3 * time.Second)},
	)

	lines := m.combinedLines()
	// four, not five: the findings event feeds the header count and the browser, but the done event a
	// moment later carries the same number, and printing both puts it on consecutive lines
	require.Len(t, lines, 4, "the stage, the start, one activity and the done line")
	assert.Contains(t, lines[0], "find",
		"a stage change names no agent and is indented to the same column as one that does")
	assert.Equal(t, "16:02:11 "+roster()[0].Paint("bugs+impl")+"  started [bugs, impl]", lines[1])
	assert.Equal(t, "16:02:12 "+roster()[1].Paint("codex")+"      tool: Grep", lines[2],
		"chronological, not grouped by agent, and the shorter name is padded out to the longest")
	assert.Contains(t, lines[3], "done, 1 findings")
	assert.Equal(t, 1, m.found.total, "the findings event still feeds the header count")
}

func TestModel_combinedLines_alignsOnTheWidestName(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: at},
		event(pipeline.EventAgentActivity, "bugs+impl", "the long name"),
		event(pipeline.EventAgentActivity, "codex", "the short one"),
	)

	// every line starts its text at one column, whatever the agent is called, so the log reads as a
	// column of prose rather than a ragged edge
	starts := make([]int, 0, 3)
	for _, line := range m.combinedLines() {
		starts = append(starts, strings.Index(stripColor(line), "the "))
	}
	assert.Equal(t, starts[1], starts[2], "a short agent name is padded out to the longest")
	assert.Positive(t, starts[1])
}

func TestModel_combinedLines_progressIsThrottled(t *testing.T) {
	m := feed(t, New(ModelConfig{Roster: roster()}),
		event(pipeline.EventAgentProgress, "bugs+impl", "thinking"),
		pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl", Text: "Read", At: at.Add(time.Second)},
		pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl", Text: "Grep", At: at.Add(2 * time.Second)},
		pipeline.Event{Kind: pipeline.EventAgentActivity, Agent: "bugs+impl",
			Text: "Six real problems across both lenses.", At: at.Add(3 * time.Second)},
		pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl", Text: "Bash", At: at.Add(4 * time.Second)},
	)

	lines := m.combinedLines()
	require.Len(t, lines, 2, "the first tool call shows life, the burst behind it is held back")
	assert.Contains(t, lines[0], "thinking", "the first one goes through immediately")
	assert.Contains(t, lines[1], "Six real problems across both lenses.", "and prose is never throttled")
	assert.Equal(t, "Bash", m.agents[0].last,
		"a throttled tool call still reaches the status cell - being held back from the log is the only thing it means")

	t.Run("prose resets the clock, so a talker is never charged a tool line", func(t *testing.T) {
		// the prose landed at +3s, so a tool call at +11s is only eight seconds after the last line
		quiet := feed(t, m, pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl",
			Text: "Bash", At: at.Add(ProgressInterval + time.Second)})
		assert.Len(t, quiet.combinedLines(), 2, "an agent narrating steadily is visibly alive already")
	})

	t.Run("another heartbeat once it has genuinely gone quiet", func(t *testing.T) {
		later := feed(t, m, pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl",
			Text: "Bash", At: at.Add(3*time.Second + ProgressInterval)})
		require.Len(t, later.combinedLines(), 3)
		assert.Contains(t, later.combinedLines()[2], "Bash", "silence for a full interval earns a line")
	})

	t.Run("the heartbeat reports the newest tool call, not the one that opened the interval", func(t *testing.T) {
		fresh := feed(t, New(ModelConfig{Roster: roster()}),
			event(pipeline.EventAgentStarted, "bugs+impl", "bugs"),
			pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl", Text: "Read codex.go", At: at.Add(time.Second)},
			pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl", Text: "Read proc.go", At: at.Add(2 * time.Second)},
			pipeline.Event{Kind: pipeline.EventAgentProgress, Agent: "bugs+impl", Text: "Read proc.go", At: at.Add(ProgressInterval + time.Second)},
		)
		lines := fresh.combinedLines()
		require.Len(t, lines, 2, "the started line, then one heartbeat")
		assert.Contains(t, lines[1], "Read proc.go",
			"thinking says nothing; the tool call says what the agent is looking at")
	})
}

// stripColor removes SGR sequences so a test can measure where visible text actually starts. Agent
// names are painted with raw ANSI, and those bytes carry no display width.
func stripColor(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			out.WriteByte(s[i])
			continue
		}
		for i < len(s) && s[i] != 'm' {
			i++
		}
	}
	return out.String()
}

func TestModel_combinedLines_empty(t *testing.T) {
	assert.Equal(t, []string{"waiting for the first agent..."}, New(ModelConfig{Roster: roster()}).combinedLines())
}

func TestCombinedState_push(t *testing.T) {
	c := &combinedState{}
	for i := range combinedLimit + 5 {
		c.push(combinedEntry{agent: "bugs", text: strconv.Itoa(i), at: at})
	}

	require.Len(t, c.entries, combinedLimit, "the compact log is bounded like a pane")
	assert.Equal(t, "5", c.entries[0].text, "the oldest entries are the ones dropped")
}

func TestModel_paint(t *testing.T) {
	m := New(ModelConfig{Roster: roster()})

	assert.Equal(t, "\x1b[36mbugs+impl\x1b[39m", m.paint("bugs+impl"))
	assert.Equal(t, "stranger", m.paint("stranger"), "an agent with no spec keeps the default foreground")
}

func TestModel_combinedLines_wrap(t *testing.T) {
	// **clipping loses the end of exactly the lines worth reading.** A narrated step or a Bash command
	// is the informative part of the log and the part most likely to run long.
	long := "Bash git diff master..HEAD -- app/pipeline/ app/config.go app/main.go ':!vendor'"
	m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 76, Height: 24})
	m = feed(t, m, event(pipeline.EventAgentProgress, "bugs+impl", long))

	lines := m.combinedLines()
	require.Len(t, lines, 2, "one row could not hold it")
	assert.Contains(t, lines[0], "16:02:11", "the first row carries the timestamp and the agent")
	assert.NotContains(t, lines[1], "16:02:11", "the rest carry neither, so the entry reads as one thing")

	// continuing under the text column, not under the timestamp. Measured in display columns, because
	// the painted agent name carries ANSI and a byte index would count the escapes
	at := strings.Index(lines[0], "Bash")
	require.Positive(t, at)
	assert.Equal(t, lipgloss.Width(lines[0][:at]), lipgloss.Width(lines[1])-lipgloss.Width(strings.TrimLeft(lines[1], " ")),
		"the continuation starts in the same column the text does")

	joined := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	assert.Contains(t, joined, long, "every word survives the wrap")
	for _, l := range lines {
		assert.LessOrEqual(t, lipgloss.Width(l), 76, "no row runs past the terminal")
	}

	t.Run("a line that fits stays one row", func(t *testing.T) {
		short := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 76, Height: 24})
		short = feed(t, short, event(pipeline.EventAgentActivity, "bugs+impl", "reading proc.go"))
		assert.Len(t, short.combinedLines(), 1)
	})

	t.Run("a terminal too narrow to wrap into clips instead", func(t *testing.T) {
		// below the floor a wrapped entry is more rows of indent than of text
		narrow := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 24, Height: 24})
		narrow = feed(t, narrow, event(pipeline.EventAgentProgress, "bugs+impl", long))
		assert.Len(t, narrow.combinedLines(), 1)
	})

	t.Run("a word longer than the column is broken rather than dropped", func(t *testing.T) {
		unbroken := strings.Repeat("x", 200)
		m := feed(t, New(ModelConfig{Roster: roster()}), tea.WindowSizeMsg{Width: 76, Height: 24})
		m = feed(t, m, event(pipeline.EventAgentProgress, "bugs+impl", unbroken))
		lines := m.combinedLines()
		require.Greater(t, len(lines), 1)
		assert.Contains(t, strings.Join(strings.Fields(strings.Join(lines, "")), ""), unbroken,
			"it has no space to break on, so it is cut at the column and continues")
	})
}

func colored(t *testing.T, width int) Model {
	t.Helper()
	m := New(ModelConfig{Roster: roster()})
	m.style.profile = termenv.ANSI
	return feed(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
}

func TestModel_combinedLines_paintsTextInTheAgentColor(t *testing.T) {
	m := feed(t, colored(t, 76),
		event(pipeline.EventAgentActivity, "bugs+impl", "reading proc.go"),
		event(pipeline.EventAgentActivity, "codex", "tool: Grep"),
		pipeline.Event{Kind: pipeline.EventStage, Stage: "verify", At: at},
	)

	lines := m.combinedLines()
	require.Len(t, lines, 3)
	assert.Equal(t, "16:02:11 "+roster()[0].Paint("bugs+impl")+"  "+roster()[0].Paint("reading proc.go"), lines[0],
		"the text is painted like the name, opened after the prefix and closed at the row end")
	assert.Equal(t, "16:02:11 "+roster()[1].Paint("codex")+"      "+roster()[1].Paint("tool: Grep"), lines[1],
		"a hex color paints the text too")
	assert.NotContains(t, lines[2], roster()[0].SGR(), "a stage band keeps its own style")
	assert.NotContains(t, lines[2], roster()[1].SGR())

	t.Run("a terminal reporting no color leaves the text plain", func(t *testing.T) {
		plain := feed(t, New(ModelConfig{Roster: roster()}), event(pipeline.EventAgentActivity, "bugs+impl", "reading proc.go"))
		assert.Equal(t, "16:02:11 "+roster()[0].Paint("bugs+impl")+"  reading proc.go", plain.combinedLines()[0],
			"the name keeps the raw paint it always had, and nothing new is emitted")
	})

	t.Run("an inline code span re-opens the agent color after it", func(t *testing.T) {
		m := feed(t, colored(t, 76), event(pipeline.EventAgentActivity, "codex", "reading `proc.go` again"))
		seq := roster()[1].SGR()
		assert.Contains(t, m.combinedLines()[0], seq+"reading "+ansiCodeOn+"proc.go"+ansiCodeOff+seq+" again"+ansiCodeOff)
	})

	t.Run("leading spaces stay ahead of the paint", func(t *testing.T) {
		m := feed(t, colored(t, 76), event(pipeline.EventAgentActivity, "codex", "   indented"))
		assert.Contains(t, m.combinedLines()[0], "      "+"   "+roster()[1].Paint("indented"),
			"the wrapper measures the indent on the plain text, so the color opens after it")
	})
}

func TestModel_combinedLines_paintsEveryWrappedRow(t *testing.T) {
	long := "Bash git diff master..HEAD -- app/pipeline/ app/config.go app/main.go ':!vendor'"
	m := feed(t, colored(t, 76), event(pipeline.EventAgentProgress, "bugs+impl", long))
	seq := roster()[0].SGR()

	lines := m.combinedLines()
	require.Len(t, lines, 2, "one row could not hold it")
	for i, l := range lines {
		assert.True(t, strings.HasSuffix(l, ansiCodeOff), "row %d closes the color it opened: %q", i, l)
		assert.LessOrEqual(t, lipgloss.Width(l), 76, "no row runs past the terminal")
	}
	col := strings.Index(stripColor(lines[0]), "Bash")
	require.Positive(t, col)
	assert.True(t, strings.HasPrefix(lines[1], strings.Repeat(" ", col)+seq),
		"the continuation row opens the agent color after its indent: %q", lines[1])

	joined := strings.Join(strings.Fields(stripColor(strings.Join(lines, " "))), " ")
	assert.Contains(t, joined, long, "every word survives the wrap")

	t.Run("a code span broken by the wrap keeps its own color on the next row", func(t *testing.T) {
		text := "reading `app/pipeline/find.go app/pipeline/synthesis.go app/pipeline/verify.go` now"
		m := feed(t, colored(t, 60), event(pipeline.EventAgentProgress, "codex", text))
		hex := roster()[1].SGR()

		lines := m.combinedLines()
		require.Len(t, lines, 3, "one path per row")
		assert.Contains(t, lines[0], hex+"reading "+ansiCodeOn+"app/pipeline/find.go", "the span opens on the first row")
		for i, l := range lines[:2] {
			assert.True(t, strings.HasSuffix(l, ansiCodeOff), "row %d closes before the span does: %q", i, l)
		}
		for i, l := range lines[1:] {
			rest := strings.TrimLeft(l, " ")
			assert.True(t, strings.HasPrefix(rest, ansiCodeOn), "row %d re-opens the span, not the agent color: %q", i+1, rest)
		}
		assert.True(t, strings.HasSuffix(lines[2], ansiCodeOff+hex+" now"+ansiCodeOff), "the agent color returns once the span closes")
	})

	t.Run("a word longer than the column is painted on every piece", func(t *testing.T) {
		unbroken := strings.Repeat("x", 200)
		m := feed(t, colored(t, 76), event(pipeline.EventAgentProgress, "bugs+impl", unbroken))
		lines := m.combinedLines()
		require.Greater(t, len(lines), 1)
		for i, l := range lines {
			assert.Contains(t, l, seq+"xxx", "row %d opens the color", i)
			assert.True(t, strings.HasSuffix(l, ansiCodeOff), "row %d closes it", i)
		}
		assert.Contains(t, strings.Join(strings.Fields(stripColor(strings.Join(lines, ""))), ""), unbroken)
	})

	t.Run("a terminal too narrow to wrap into still closes the clipped row", func(t *testing.T) {
		m := feed(t, colored(t, 24), event(pipeline.EventAgentProgress, "bugs+impl", long))
		lines := m.combinedLines()
		require.Len(t, lines, 1)
		assert.True(t, strings.HasSuffix(lines[0], ansiCodeOff), "%q", lines[0])
		assert.LessOrEqual(t, lipgloss.Width(lines[0]), 24)
	})
}
