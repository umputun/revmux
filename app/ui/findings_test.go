package ui

import (
	"strconv"
	"strings"

	"testing"

	xansi "github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

// report is what a finished run hands the browser: three findings, one per severity, out of order so
// the sort has something to do.
func report() finding.Report {
	return finding.Report{
		Findings: []finding.Finding{
			{ID: "f1", File: "app/pipeline/find.go", Line: 12, Severity: finding.Minor, Confidence: 60,
				Title: "stale comment", Body: "the comment names the old field"},
			{ID: "f2", File: "app/main.go", Line: 42, EndLine: 48, Severity: finding.Critical, Confidence: 95,
				Title: "unchecked error", Body: "the write error is dropped\nso a short write reads as success",
				Fix: "check it", Sources: []string{"bugs+impl", "codex"}, Lenses: []string{"bugs"},
				Verdict: finding.Confirmed},
			{ID: "f3", File: "app/ui/view.go", Severity: finding.Major, Confidence: 80, Title: "pane clipping"},
		},
		OpenQuestions: []finding.Finding{{Title: "is the retry budget right"}},
	}
}

// listed is what the browser lists under the active filter, in the order render walks it.
func listed(f *findingsState) []finding.Finding {
	out := make([]finding.Finding, 0, len(f.matches))
	for _, i := range f.matches {
		out = append(out, f.rows[i])
	}
	return out
}

// listedReport is a report whose findings all carry a body and a fix, so the browser caches two
// documents per row and the cache count is a plain function of the finding count.
func listedReport() finding.Report {
	rep := finding.Report{}
	for i := range 3 {
		rep.Findings = append(rep.Findings, finding.Finding{
			File: "a.go", Line: i, Severity: finding.Major, Title: "finding " + strconv.Itoa(i),
			Body: "- one\n- two", Fix: "```go\nx := " + strconv.Itoa(i) + "\n```"})
	}
	return rep
}

// browsed is a model with the report already in it, sized so the findings pane shows exactly 5 lines.
func browsed(t *testing.T, rep finding.Report) Model {
	t.Helper()
	m := New(ModelConfig{Roster: roster()})
	// same five-line pane the scroll tests use, so the scroll expectations stay in screenfuls
	m = feed(t, m, tea.WindowSizeMsg{Width: 100, Height: len(roster()) + chromeLines + 5},
		CompletedMsg{Report: rep})
	return m
}

func TestModel_complete(t *testing.T) {
	m := browsed(t, report())

	require.NotNil(t, m.findings)
	assert.Equal(t, 3, m.view.tab, "the browser opens one past the last agent")
	assert.Equal(t, m.findingsTab(), m.view.tab)
	assert.True(t, m.browsing())

	t.Run("worst findings first", func(t *testing.T) {
		vis := listed(m.findings)
		require.Len(t, vis, 3)
		assert.Equal(t, []string{"f2", "f3", "f1"}, []string{vis[0].ID, vis[1].ID, vis[2].ID})
	})

	t.Run("the agent tabs stay reachable", func(t *testing.T) {
		back := feed(t, m, press("2")) // tabs are labeled from one, so 2 is the first agent
		assert.Equal(t, 1, back.view.tab)
		assert.False(t, back.browsing())
		assert.Contains(t, back.tabBar(), "2 bugs+impl", "and so does the browser's own tab")
		assert.Contains(t, back.tabBar(), "f findings")
	})

	t.Run("there is no browser tab before the report arrives", func(t *testing.T) {
		early := New(ModelConfig{Roster: roster()})
		assert.Equal(t, -1, early.findingsTab())
		assert.NotContains(t, early.tabBar(), "findings")
		assert.False(t, early.browsing())
		assert.Equal(t, []string{"the review is still running..."}, early.findingsPane())
	})

	t.Run("f does nothing while there is nothing to browse", func(t *testing.T) {
		early := feed(t, New(ModelConfig{Roster: roster()}), press("f"))
		assert.Equal(t, 0, early.view.tab)
	})
}

func TestModel_findingsPane(t *testing.T) {
	pane := strings.Join(browsed(t, report()).findingsPane(), "\n")

	// the report's own headings, rendered: "## Critical" for a severity group, "### title" per finding
	// literals, not calls to the function under test: asserting heading(2, "Critical") against itself
	// passes whatever heading emits, so the rendered form would be pinned nowhere
	assert.Contains(t, pane, "\x1b[1m\x1b[36m## Critical\x1b[39m\x1b[22m")
	assert.Contains(t, pane, "\x1b[1m\x1b[36m## Major\x1b[39m\x1b[22m")
	assert.Contains(t, pane, "\x1b[1m\x1b[36m## Minor\x1b[39m\x1b[22m")
	// the report's own shape: the title is the heading and the location sits under it, as "### title"
	// followed by its `file:line` does on stdout
	assert.Contains(t, pane, "\x1b[1m\x1b[36m### unchecked error  [95]\x1b[39m\x1b[22m", "the worst finding is first")
	assert.Contains(t, pane, "    app/main.go:42-48", "with where it is on the line under it")
	assert.Contains(t, pane, "\x1b[1m\x1b[36m### pane clipping  [80]\x1b[39m\x1b[22m")
	assert.Contains(t, pane, "    app/ui/view.go", "a file-level finding renders as the bare path")
	// the body is a rendered document and glamour colors it word by word, so the prose is asserted on the
	// stripped text; the indent survives stripping and is still pinned
	assert.Contains(t, xansi.Strip(pane), "    the write error is dropped",
		"a finding opens showing its body, not just its summary")
	assert.NotContains(t, pane, "is the retry budget right", "open questions are the report's, not the browser's")

	t.Run("an unrecognized severity is grouped rather than dropped", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{
			{File: "a.go", Line: 1, Severity: "invented", Title: "odd"},
			{File: "b.go", Line: 2, Severity: finding.Critical, Title: "bad"},
		}}
		lines := browsed(t, rep).findingsPane()
		assert.Equal(t, []string{
			heading(2, "Critical"), "\x1b[1m\x1b[36m### bad  [0]\x1b[39m\x1b[22m", "    b.go:2", "",
			heading(2, "Invented"), "\x1b[1m\x1b[36m### odd  [0]\x1b[39m\x1b[22m", "    a.go:1", "",
		}, lines)
	})

	t.Run("a finding with no severity at all still has a heading", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{File: "a.go", Line: 1, Title: "unranked"}}}
		assert.Equal(t, heading(2, "Unspecified"), browsed(t, rep).findingsPane()[0])
	})

	t.Run("an empty report says so", func(t *testing.T) {
		assert.Equal(t, []string{"no findings."}, browsed(t, finding.Report{}).findingsPane())
	})
}

// the browser has no cursor: it renders the report and the pane's own scrolling reads it, which is the
// same scrolling every other pane has.
func TestFindingsState_scroll(t *testing.T) {
	t.Run("the arrow and vi keys scroll it like any other pane", func(t *testing.T) {
		m := browsed(t, report())
		require.Positive(t, m.maxScroll(), "the report is longer than the pane, or there is nothing to test")

		// it opens at the top, so forward is the only way to go and j is what goes there
		down := feed(t, m, press("j"))
		assert.Equal(t, m.view.scroll-1, down.view.scroll, "j reads forward through the report")

		back := feed(t, down, press("k"))
		assert.Equal(t, m.view.scroll, back.view.scroll, "and k comes back")
	})

	t.Run("it opens on the worst finding rather than at the end of the last one", func(t *testing.T) {
		m := browsed(t, report())
		assert.Equal(t, m.maxScroll(), m.view.scroll, "a report is read from its top, unlike a live log")
		assert.Contains(t, strings.Join(m.detailPane(), "\n"), heading(2, "Critical"),
			"so the first thing on screen is the worst finding")
	})

	t.Run("outside the browser the same keys still scroll their own pane", func(t *testing.T) {
		m := feed(t, filled(t, 20), CompletedMsg{Report: report()}, press("2"), press("k"))
		assert.Equal(t, 1, m.view.scroll, "the agent pane scrolled, not the report")
	})
}

func TestFindingsState_rendersTheWholeReport(t *testing.T) {
	// the browser shows the report, not an index of it: every part the markdown on stdout carries is on
	// screen without a keystroke. Folding was tried and removed — it put the review behind a keypress
	// per finding and needed state kept in step with the pane.
	pane := strings.Join(browsed(t, report()).findingsPane(), "\n")

	assert.Contains(t, pane, "\x1b[1m\x1b[36m### unchecked error  [95]\x1b[39m\x1b[22m", "the title, as the report's ### heading")
	assert.Contains(t, pane, "    app/main.go:42-48", "where it is, on the line under it")
	assert.Contains(t, xansi.Strip(pane), "    the write error is dropped", "the body, indented under that")
	assert.Contains(t, xansi.Strip(pane), "so a short write reads as success",
		"as a rendered document, so a soft line break folds into the paragraph rather than breaking it")
	assert.Contains(t, xansi.Strip(pane), "    fix: check it")
	assert.Contains(t, pane, "    sources: bugs+impl, codex | lenses: bugs | verdict: confirmed")

	t.Run("enter is not a fold and does nothing here", func(t *testing.T) {
		after := feed(t, browsed(t, report()), press("enter"))
		assert.Equal(t, browsed(t, report()).findingsPane(), after.findingsPane())
	})

	t.Run("a finding with no body still shows where it is", func(t *testing.T) {
		terse := finding.Report{Findings: []finding.Finding{
			{File: "a.go", Line: 1, Severity: finding.Minor, Title: "terse"}}}
		assert.Equal(t, []string{heading(2, "Minor"), "\x1b[1m\x1b[36m### terse  [0]\x1b[39m\x1b[22m", "    a.go:1", ""},
			browsed(t, terse).findingsPane())
	})

	t.Run("an empty report says so", func(t *testing.T) {
		empty := feed(t, browsed(t, finding.Report{}), press("enter"))
		assert.Equal(t, []string{"no findings."}, empty.findingsPane())
	})
}

func TestFindingsState_rendersBodyAndFixAsDocuments(t *testing.T) {
	t.Run("a list in a body renders as a list", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{
			File: "a.go", Line: 1, Severity: finding.Major, Title: "listy",
			Body: "two things go wrong here:\n\n- the first one\n- the second one"}}}
		pane := plainMD(browsed(t, rep).findingsPane())
		assert.Contains(t, pane, "• the first one")
		assert.Contains(t, pane, "• the second one")
		assert.NotContains(t, pane, "- the first one", "the raw bullet marker is consumed")
	})

	t.Run("a fenced snippet in a fix renders as a code block", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{
			File: "a.go", Line: 1, Severity: finding.Major, Title: "fenced",
			Fix: "```go\nif err != nil { return err }\n```"}}}
		pane := plainMD(browsed(t, rep).findingsPane())
		assert.Contains(t, pane, "fix:", "the label survives as its own line rather than as a prefix")
		assert.Contains(t, pane, "if err != nil { return err }")
		assert.NotContains(t, pane, "```", "and the fence delimiters are consumed")
	})

	t.Run("an unclosed fence in a body cannot swallow the fix and the attribution", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{
			File: "a.go", Line: 1, Severity: finding.Major, Title: "ragged",
			Body: "look at this:\n\n```go\nfunc x() {}", Fix: "close the fence",
			Sources: []string{"bugs"}, Verdict: finding.Confirmed}}}
		pane := plainMD(browsed(t, rep).findingsPane())
		assert.Contains(t, pane, "func x() {}")
		assert.Contains(t, pane, "fix: close the fence", "the fix is its own document, so the fence never reaches it")
		assert.Contains(t, pane, "sources: bugs | verdict: confirmed")
	})

	t.Run("angle-bracket text survives in a body and in a fix", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{
			File: "a.go", Line: 1, Severity: finding.Major, Title: "angles",
			Body: "the round is <tasks-dir>/<task>/<run>/input/scope.md and the type is List<String>",
			Fix:  "spell it <binary>[/<model>][:<effort>]\n\n```go\nfunc f(x <T>) {}\n```"}}}
		pane := plainMD(browsed(t, rep).findingsPane())
		assert.Contains(t, pane, "<tasks-dir>/<task>/<run>/input/scope.md")
		assert.Contains(t, pane, "List<String>")
		assert.Contains(t, pane, "<binary>[/<model>][:<effort>]")
		assert.Contains(t, pane, "func f(x <T>) {}", "a fenced snippet keeps its angle brackets literally")
		assert.NotContains(t, pane, "&lt;", "and no entity reaches the screen")
	})

	t.Run("a row names its own cache entries", func(t *testing.T) {
		assert.Equal(t, mdKey("finding:3:body"), findingRow(3).key("body"))
		assert.Equal(t, mdKey("finding:3:fix"), findingRow(3).key("fix"))
		assert.NotEqual(t, findingRow(3).key("body"), findingRow(30).key("body"),
			"the row is part of the key, not a prefix of another row's")
	})

	t.Run("filtering does not serve another finding's cached body", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{
			{File: "alpha.go", Line: 1, Severity: finding.Major, Title: "first", Body: "the alpha body"},
			{File: "beta.go", Line: 2, Severity: finding.Major, Title: "second", Body: "the beta body"},
		}}
		m := browsed(t, rep)
		require.Contains(t, plainMD(m.findingsPane()), "the alpha body")

		narrowed := feed(t, m, press("/"), press("beta"), press("enter"))
		pane := plainMD(narrowed.findingsPane())
		assert.Contains(t, pane, "the beta body")
		assert.NotContains(t, pane, "the alpha body",
			"index 0 is a different finding under the filter, and the cache is keyed on the row rather than on it")
	})

	t.Run("a tab-indented snippet fits the pane", func(t *testing.T) {
		// a tab measures one cell to lipgloss and up to eight to the terminal, so an unexpanded row is
		// measured short, passes the wrap untouched and is drawn well past the pane's edge. Models write
		// Go tab-indented, so this is the ordinary case rather than a corner
		rep := finding.Report{Findings: []finding.Finding{{
			File: "a.go", Line: 1, Severity: finding.Major, Title: "tabby",
			Body: "the guard is missing:\n\n```go\nif err != nil {\n\t\t\t\t\treturn fmt.Errorf(\"write: %w\", err)\n}\n```",
			Fix:  "```go\nfunc f() {\n\t\t\t\t\tclose(w)\n}\n```"}}}
		m := feed(t, browsed(t, rep), tea.WindowSizeMsg{Width: 60, Height: 40})
		lines := m.findingsPane()
		// the expanded indent is what pushes it past the width, so it arrives broken up rather than whole
		require.Contains(t, plainMD(lines), "Errorf", "the snippet is on screen at all")
		require.Contains(t, plainMD(lines), "close(w)", "and so is the fix's")
		for i, l := range lines {
			assert.NotContains(t, l, "\t", "line %d still carries a raw tab: %q", i, l)
			assert.LessOrEqual(t, lipgloss.Width(l), 60, "line %d: %q", i, l)
		}
	})

	// pins the wiring the renderer's eviction reads: without it the browser's own entries look stale to
	// the pass using them, and a report past the cache bound re-renders on every repaint
	t.Run("laying the pane out is exactly one pass, and every entry the browser reads carries it", func(t *testing.T) {
		m := browsed(t, listedReport())
		before := m.md.frame
		require.Len(t, m.md.cache, 2*len(listedReport().Findings))

		m.paneLines()
		assert.Equal(t, before+1, m.md.frame,
			"one pane layout is one pass — a second opened inside it would sweep the report mid-render")
		for k, e := range m.md.cache {
			assert.Equal(t, m.md.frame, e.frame, "%v was read by this pass, so eviction may not take it", k)
		}
	})

	// closing the pass on the way in rather than on the way out passes every other assertion here: the
	// premature sweep sees an under-bound cache and the render restamps everything behind it. Over the
	// bound it takes the whole report first and re-renders it, which is the thrash all of this exists
	// to stop, so the sentinel is what tells the two orderings apart
	t.Run("an over-bound report is served from the cache rather than swept and re-rendered", func(t *testing.T) {
		m := browsed(t, listedReport())
		key := mdCacheKey{key: findingRow(0).key("body"), width: m.view.width()}
		require.Contains(t, m.md.cache, key)

		e := m.md.cache[key]
		e.lines = []string{"  sentinel"}
		m.md.cache[key] = e
		pastBound(m.md, key)

		assert.Contains(t, strings.Join(m.paneLines(), "\n"), "sentinel",
			"the pass reads its own working set before anything can be evicted from under it")
		assert.Contains(t, m.md.cache, key)
	})

	// pins the hole the pass-boundary fix left, and the one after it: the pass was opened by the
	// document renderers, so a pane rendering none never swept what the browser left — and once it did,
	// eviction still ran on the way in, so the first such layout kept the report and a second one that
	// may never come was needed to drop it
	t.Run("the first pane that renders no document sweeps the browser's report", func(t *testing.T) {
		m := browsed(t, listedReport())
		require.NotEmpty(t, m.md.cache)
		pastBound(m.md, mdCacheKey{key: findingRow(0).key("body"), width: m.view.width()})

		m.view.tab = 0 // the combined log, which renders no document at all
		m.paneLines()
		assert.Empty(t, m.md.cache, "one log layout is enough, and after a finished run it may be the only one")
		assert.Zero(t, m.md.cached)
	})

	t.Run("a rendered document still fits the pane", func(t *testing.T) {
		rep := finding.Report{Findings: []finding.Finding{{
			File: "a.go", Line: 1, Severity: finding.Major, Title: "wide",
			Body: "| a wide header | another wide header |\n|---|---|\n| " + strings.Repeat("x", 50) + " | y |",
			Fix:  "```\n" + strings.Repeat("z", 200) + "\n```"}}}
		m := browsed(t, rep)
		for _, width := range []int{60, 80, 200} {
			m = feed(t, m, tea.WindowSizeMsg{Width: width, Height: 40})
			for i, l := range m.findingsPane() {
				assert.LessOrEqual(t, lipgloss.Width(l), width, "width %d, line %d: %q", width, i, l)
			}
		}
	})
}

func TestFindingsState_filter(t *testing.T) {
	// a fresh model per case: Model copies share one findingsState pointer, so a query typed in one
	// subtest would still be there in the next
	querying := func(t *testing.T) Model {
		t.Helper()
		return feed(t, browsed(t, report()), press("/"), press("v"), press("i"), press("e"))
	}

	open := feed(t, browsed(t, report()), press("/"))
	require.True(t, open.findings.typing)
	assert.Contains(t, open.findingsPane()[0], "filter: _", "the caret says the query is open")

	m := querying(t)
	require.Len(t, m.findings.matches, 1)
	assert.Equal(t, "pane clipping", listed(m.findings)[0].Title, "the path matches, not only the title")

	t.Run("enter accepts the query and hands the keys back", func(t *testing.T) {
		done := feed(t, querying(t), press("enter"))
		assert.False(t, done.findings.typing)
		assert.Equal(t, "vie", done.findings.query)
		assert.Contains(t, done.findingsPane()[0], "filter: vie (1 of 3)")
	})

	t.Run("esc abandons it and everything is listed again", func(t *testing.T) {
		back := feed(t, querying(t), press("esc"))
		assert.False(t, back.findings.typing)
		assert.Empty(t, back.findings.query)
		assert.Len(t, back.findings.matches, 3)
		assert.NotContains(t, back.findingsPane()[0], "filter:")
	})

	t.Run("backspace takes the last rune back", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("ü"), press("x"), press("backspace"), press("backspace"))
		assert.Empty(t, typed.findings.query, "a multi-byte rune goes back whole")
		assert.Len(t, typed.findings.matches, 3)
	})

	t.Run("backspace on an empty query is not an underflow", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("backspace"))
		assert.Empty(t, typed.findings.query)
	})

	t.Run("a space is text, not a lost keystroke", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("stale"), press("space"))
		assert.Equal(t, "stale ", typed.findings.query)
	})

	t.Run("keys that act on the browser are text while the query is open", func(t *testing.T) {
		typed := feed(t, browsed(t, report()), press("/"), press("j"), press("q"))
		assert.Equal(t, "jq", typed.findings.query)
		assert.Equal(t, typed.maxScroll(), typed.view.scroll,
			"j went into the query rather than scrolling, so the narrowed report is still at its top")
	})

	t.Run("ctrl+c is never text: a half-typed query must not be a trap", func(t *testing.T) {
		typing := feed(t, browsed(t, report()), press("/"))
		_, cmd := typing.Update(press("ctrl+c"))
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd())
	})

	t.Run("a query nothing matches says so rather than rendering blank", func(t *testing.T) {
		none := feed(t, browsed(t, report()), press("/"), press("zzz"), press("enter"))
		assert.Empty(t, none.findings.matches)
		assert.Equal(t, `nothing matches "zzz".`, none.findingsPane()[1])
	})

	t.Run("the filter is case-insensitive", func(t *testing.T) {
		up := feed(t, browsed(t, report()), press("/"), press("UNCHECKED"), press("enter"))
		require.Len(t, up.findings.matches, 1)
		assert.Equal(t, "unchecked error", listed(up.findings)[0].Title)
	})

	t.Run("a filter key outside the browser is not swallowed", func(t *testing.T) {
		agent := feed(t, browsed(t, report()), press("1"), press("/"))
		assert.False(t, agent.findings.typing)
	})
}

func TestModel_findingsScrollWindow(t *testing.T) {
	// twelve findings in a five-line pane: the report is far longer than the window, and reading it is
	// the pane's own scrolling rather than anything the browser tracks
	many := finding.Report{}
	for i := range 12 {
		many.Findings = append(many.Findings, finding.Finding{
			File: "app/f" + string(rune('a'+i)) + ".go", Line: i + 1, Severity: finding.Major,
			Title: "issue " + string(rune('a'+i))})
	}

	m := browsed(t, many)
	require.Equal(t, 5, m.paneHeight())
	require.Positive(t, m.maxScroll(), "the report is longer than the pane")
	assert.Contains(t, strings.Join(m.detailPane(), "\n"), "issue a", "it opens on the first finding")

	t.Run("reading forward reaches the last one", func(t *testing.T) {
		down := m
		for down.view.scroll > 0 {
			down = feed(t, down, press("j"))
		}
		assert.Contains(t, strings.Join(down.detailPane(), "\n"), "issue l")
	})

	t.Run("and reading back returns to the first", func(t *testing.T) {
		up := m
		for range 40 {
			up = feed(t, up, press("k"))
		}
		assert.Equal(t, up.maxScroll(), up.view.scroll, "it stops at the top rather than running past it")
		assert.Contains(t, strings.Join(up.detailPane(), "\n"), "issue a")
	})
}

func TestFindingsState_titleWrapsWithEveryRowStyled(t *testing.T) {
	// **a style opened on one row and closed on another does not survive the break.** Styling the whole
	// rendered string and then wrapping it leaves the first row opening a style it never closes and the
	// rows after it carrying no opener — the title paints its first line and renders the rest plain.
	long := finding.Report{Findings: []finding.Finding{{
		File: "app/ui/wrap.go", Line: 29, Severity: finding.Major, Confidence: 97,
		Title: "Wrap's narrow branch returns an unclipped row, so the plain renderer lost its output bound",
	}}}
	m := feed(t, browsed(t, long), tea.WindowSizeMsg{Width: 60, Height: 40})

	var rows []string
	for _, l := range m.findingsPane() {
		if strings.Contains(l, "###") || strings.Contains(l, "output bound") {
			rows = append(rows, l)
		}
	}
	require.Greater(t, len(rows), 1, "the title is longer than the pane, so it wraps")

	for _, r := range rows {
		assert.True(t, strings.HasPrefix(r, ansiHeadOn), "every row opens the heading style, got %q", r)
		assert.True(t, strings.HasSuffix(r, ansiHeadOff), "and closes it, got %q", r)
		assert.LessOrEqual(t, lipgloss.Width(r), 60)
	}

	joined := strings.Join(strings.Fields(strings.Join(rows, " ")), " ")
	assert.Contains(t, joined, "lost its output bound", "and the tail of the title survives")
	assert.Contains(t, joined, "[97]", "with the confidence marker still on it")
}

func TestFindingsState_rowLines_spanAcrossAWrap(t *testing.T) {
	// **markdown is rendered before the wrap, not after each row.** Both patterns need their
	// delimiters on one string, so wrapping first and rendering each row leaves a span straddling the
	// boundary matching neither — and the reader sees raw asterisks in the one line that is supposed to
	// be a heading. A long emphasized title hits this immediately.
	rep := finding.Report{Findings: []finding.Finding{{
		File: "a.go", Line: 1, Severity: finding.Major, Confidence: 90,
		Title: "**abcdefghijklmnopqrstuvwxyz** and `some/long/path/that/keeps/going.go` besides",
	}}}
	m := feed(t, browsed(t, rep), tea.WindowSizeMsg{Width: 34, Height: 40})

	pane := m.findingsPane()
	joined := strings.Join(pane, "\n")
	assert.NotContains(t, joined, "**", "the emphasis is rendered, never shown as delimiters")
	assert.NotContains(t, joined, "`", "and neither is the code span")

	var rows []string
	for _, l := range pane {
		if strings.Contains(l, "###") || strings.Contains(l, "besides") || strings.Contains(l, "going.go") {
			rows = append(rows, l)
		}
	}
	require.Greater(t, len(rows), 1, "the title is longer than the pane, so it wraps")
	for _, r := range rows {
		assert.True(t, strings.HasPrefix(r, ansiHeadOn), "every row opens the heading style, got %q", r)
		assert.True(t, strings.HasSuffix(r, ansiHeadOff),
			"and closes it — SGR survives a newline, so an unclosed row paints everything after it, got %q", r)
		assert.LessOrEqual(t, lipgloss.Width(r), 34)
	}
}
