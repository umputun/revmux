package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModel_inputLines_markdown(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "# Review **scope**\n\n- inspect `app/ui`\n\n> keep the status visible\n\n```go\nfunc main() {}\n```",
	}}})
	m.view.mode = modeInputs

	lines := m.inputLines()
	out := plainMD(lines)

	assert.Contains(t, out, "input/scope.md")
	assert.Contains(t, out, "# Review", "h1 keeps its markdown hash")
	assert.Contains(t, out, "scope")
	assert.NotContains(t, out, "**scope**")
	assert.Contains(t, out, "• inspect", "a list renders as a list")
	assert.Contains(t, out, "app/ui")
	assert.NotContains(t, out, "`app/ui`")
	assert.Contains(t, out, "│ keep the status visible", "a blockquote renders as a quote")
	assert.NotContains(t, out, "```go")
	assert.Contains(t, out, "func main() {}")
}

func TestModel_inputLines_markdownFenceMatchesDelimiterAndLength(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "````go\n**literal one**\n~~~\n**literal two**\n```\n**literal three**\n````\n\n**rendered**",
	}}})
	m.view.mode = modeInputs

	out := plainMD(m.inputLines())
	assert.Contains(t, out, "**literal one**", "a shorter fence inside a longer one stays literal")
	assert.Contains(t, out, "~~~", "and so does a fence spelled with the other delimiter")
	assert.Contains(t, out, "**literal two**")
	assert.Contains(t, out, "```")
	assert.Contains(t, out, "**literal three**")
	assert.NotContains(t, out, "````", "only the matching delimiter of at least the opening length closes it")
	assert.NotContains(t, out, "**rendered**", "and prose after it renders again")
	assert.Contains(t, out, "rendered")
}

func TestModel_inputLines_markdownBlocksThatUsedToBeRaw(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "| flag | meaning |\n|---|---|\n| --task | the task id |\n\n---\n\n" +
			"read *carefully*, see [the docs](http://example.com/d), ~~ignore the rest~~\n",
	}}})
	m.view.mode = modeInputs

	out := plainMD(m.inputLines())
	assert.Contains(t, out, "│", "the table draws its columns")
	assert.Contains(t, out, "┼", "and its header separator")
	assert.NotContains(t, out, "|---|---|", "the raw separator row is gone")
	assert.NotContains(t, out, "| flag |")
	assert.Contains(t, out, "the task id")
	assert.Contains(t, out, "--------", "the horizontal rule is drawn, not left as three dashes")
	assert.Contains(t, out, "carefully")
	assert.NotContains(t, out, "*carefully*")
	assert.Contains(t, out, "the docs")
	assert.Contains(t, out, "http://example.com/d", "the link target is shown")
	assert.NotContains(t, out, "[the docs]")
	assert.Contains(t, out, "ignore the rest")
	assert.NotContains(t, out, "~~ignore the rest~~", "strikethrough is styled, not left as tildes")
}

func TestModel_inputLines_keepsAngleBracketText(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "the round is <tasks-dir>/<task>/<run>/input/scope.md, and the grammar is <binary>[/<model>]\n\n" +
			"```go\nfunc f(x <T>) {}\n```\n",
	}}})
	m.view.mode = modeInputs

	out := plainMD(m.inputLines())
	assert.Contains(t, out, "<tasks-dir>/<task>/<run>/input/scope.md", "an angle token in prose is not raw HTML to a reader")
	assert.Contains(t, out, "<binary>[/<model>]")
	assert.Contains(t, out, "func f(x <T>) {}", "and a fenced one stays literal")
	assert.NotContains(t, out, "&lt;", "with no entity left on screen")
}

func TestModel_inputLines_verbatimAndNotice(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{
		{Label: "data.json", Path: "input/context/data.json", Content: `{"literal":"**not markdown**"}`},
		{Label: "goal", Path: "input/goal.md", Markdown: true, Notice: "not provided"},
	}})
	m.view.mode = modeInputs

	assert.Contains(t, strings.Join(m.inputLinesAt(0), "\n"), `{"literal":"**not markdown**"}`)
	goal := strings.Join(m.inputLinesAt(1), "\n")
	assert.Contains(t, goal, "input/goal.md")
	assert.Contains(t, goal, "not provided")

	t.Run("a truncated document carries both its content and the notice", func(t *testing.T) {
		// the snapshotter caps a file at 1 MiB and reports the cut as a notice, so content and notice
		// arrive together — the document renders and the warning sits under it, separated by a blank row
		tm := New(ModelConfig{Inputs: []InputDocument{{
			Label: "scope", Path: "input/scope.md", Markdown: true,
			Content: "# scope\n\nthe part that fit", Notice: "truncated at 1 MiB"}}})
		tm.view.mode = modeInputs

		lines := tm.inputLines()
		out := plainMD(lines)
		assert.Contains(t, out, "# scope", "the document renders")
		assert.Contains(t, out, "the part that fit")
		assert.Contains(t, out, "truncated at 1 MiB", "and the notice follows it")
		assert.Less(t, strings.Index(out, "the part that fit"), strings.Index(out, "truncated at 1 MiB"),
			"the notice sits under the content, not above it")
		assert.Empty(t, strings.TrimSpace(xansi.Strip(lines[len(lines)-2])), "with a blank row between them")
	})
}

func TestModel_inputLines_nonMarkdownStaysVerbatim(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "notes.txt", Path: "input/context/notes.txt",
		Content: "| flag | meaning |\n|---|---|\n# not a heading\n- not a list\n",
	}}})
	m.view.mode = modeInputs

	out := strings.Join(m.inputLines(), "\n")
	assert.Contains(t, out, "| flag | meaning |", "a document not marked Markdown is never rendered as one")
	assert.Contains(t, out, "|---|---|")
	assert.Contains(t, out, "# not a heading")
	assert.Contains(t, out, "- not a list")
	assert.NotContains(t, out, "•")
}

func TestModel_inputLines_wraps(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "A paragraph with enough words to wrap across several terminal rows without losing its tail.",
	}}})
	m.view.mode = modeInputs
	m.view.cols = 30

	lines := m.inputLines()
	require.Greater(t, len(lines), 4)
	assert.Contains(t, strings.Join(strings.Fields(plainMD(lines)), " "), "losing its tail.")
	for _, line := range lines {
		assert.LessOrEqual(t, lipgloss.Width(line), 30)
	}
}

func TestModel_inputLines_fitsEveryWidth(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Label: "scope", Path: "input/scope.md", Markdown: true,
		Content: "# A heading that runs past the narrowest width under test by a comfortable margin\n\n" +
			"| a wide column header | another wide column header |\n|---|---|\n" +
			"| " + strings.Repeat("x", 50) + " | " + strings.Repeat("y", 50) + " |\n\n" +
			"```go\nfunc main() { println(\"" + strings.Repeat("a", 250) + "\") }\n```\n",
	}}})
	m.view.mode = modeInputs

	for _, width := range []int{60, 80, 200} {
		m.view.cols = width
		lines := m.inputLines()
		require.NotEmpty(t, lines)
		for i, line := range lines {
			assert.LessOrEqual(t, lipgloss.Width(line), width, "line %d at width %d: %q", i, width, line)
		}
	}
}

func TestModel_inputLines_verbatimPreservesWhitespace(t *testing.T) {
	m := New(ModelConfig{})
	m.view.cols = 20
	line := strings.Repeat("x", 19) + "  y"
	assert.Equal(t, line, strings.Join(m.verbatimLine(line), ""),
		"hard wrapping may move whitespace to another row but must not discard it")

	wrapped := m.verbatimLine("\tmake  target")
	for _, part := range wrapped {
		assert.NotContains(t, part, "\t", "tabs are expanded before width accounting")
		assert.LessOrEqual(t, lipgloss.Width(part), 20)
	}
	assert.Equal(t, "        make  target", strings.Join(wrapped, ""),
		"a verbatim row reproduces the terminal, which draws a tab as eight columns")

	m.cfg.Inputs = []InputDocument{{Path: "input/scope.md", Content: "word\tword", Markdown: true}}
	m.view.mode = modeInputs
	assert.NotContains(t, strings.Join(m.inputLines(), "\n"), "\t",
		"Markdown prose expands tabs before the document reaches the renderer")
}

func TestModel_inputLines_expandsTabsPerLine(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{
		Path: "input/scope.md", Markdown: true, Content: "```\nab\tc\nab\tc\nab\tc\n```\n",
	}}})
	m.view.mode = modeInputs

	// expandTabs never resets its column on a newline, so one call over the whole document would give
	// each row a different stop; the column has to restart per line
	rows := []string{}
	for l := range strings.SplitSeq(plainMD(m.inputLines()), "\n") {
		if strings.Contains(l, "ab") {
			rows = append(rows, strings.TrimLeft(l, " "))
		}
	}
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"ab  c", "ab  c", "ab  c"}, rows)
}

func TestModel_inputLines_sanitizesMetadata(t *testing.T) {
	m := New(ModelConfig{Task: "bad\x1b]52;c;payload\a", Run: "run\nspoof", Inputs: []InputDocument{{
		Label: "name\x1b[2J", Path: "input/\npath", Notice: "failed\rspoof",
	}}})
	m.view.mode = modeInputs

	assert.Equal(t, "bad�name", m.visibleMetadata("bad\nname"))
	out := m.View()
	assert.NotContains(t, out, "\x1b]52", "metadata must not emit an OSC command")
	assert.NotContains(t, out, "\x1b[2J", "metadata must not emit a CSI command")
	assert.Contains(t, out, "bad�]52;c;payload�/run�spoof")
	assert.Contains(t, out, "input/�path")
	assert.Contains(t, m.tabBar(), "name�[2J")
}

func TestModel_inputLines_emptyFile(t *testing.T) {
	m := New(ModelConfig{Inputs: []InputDocument{{Label: "goal", Path: "input/goal.md", Markdown: true}}})
	m.view.mode = modeInputs
	assert.Contains(t, strings.Join(m.inputLines(), "\n"), "(empty file)")
}

func TestModel_inputLines_missingTab(t *testing.T) {
	m := New(ModelConfig{})
	m.view.mode = modeInputs
	assert.Equal(t, []string{"no such input"}, m.inputLines())
}
