package ui

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMDRenderer() *mdRenderer {
	return newMDRenderer(termenv.Ascii, true)
}

// mdProfiles is every profile a real terminal can hand the renderer. Ascii is what the test surface
// reports, so a case asserting on rendered text runs there; anything measuring or wrapping runs
// across all of them, since a colored profile puts escapes inside the line lipgloss has to measure.
// A slice rather than a map: subtest names generated from a map come out in a different order on
// every run, so a failure is not reproducible and -run does not select stably.
var mdProfiles = []struct {
	name    string
	profile termenv.Profile
}{
	{"ascii", termenv.Ascii}, {"ansi", termenv.ANSI}, {"ansi256", termenv.ANSI256}, {"truecolor", termenv.TrueColor},
}

// pastBound puts the cache over its limit by charging the excess to one entry, so the next pass
// boundary evicts with the map and the counter still agreeing. Forcing cached alone leaves the two
// apart, and eviction subtracts what the entries actually hold.
func pastBound(r *mdRenderer, k mdCacheKey) {
	over := mdMaxCache + 1
	e := r.cache[k]
	r.cached -= e.size
	e.size = over - r.cached
	r.cache[k] = e
	r.cached = over
}

// pass is one pane laid out whole: it opens, reads the documents that pane holds, and closes. That is
// the shape reviewPaneLines and inputLinesAt give the renderer, and passing no document is the pane
// that renders none.
func pass(r *mdRenderer, docs ...mdDoc) {
	r.beginFrame()
	defer r.endFrame()
	for _, d := range docs {
		r.lines(d)
	}
}

// sumLen is the bytes a rendered slice holds, which is what the cache bounds itself by.
func sumLen(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	return n
}

// plainMD joins rendered lines with their ANSI stripped, so a structural assertion does not pin a
// glamour patch version's exact escape sequences.
func plainMD(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(xansi.Strip(l), " "))
	}
	return strings.Join(out, "\n")
}

func TestMDRenderer_linesCaches(t *testing.T) {
	t.Run("a second call returns the cached lines", func(t *testing.T) {
		r := testMDRenderer()
		first := r.lines(mdDoc{key: "input:0", src: "hello there", width: 40})
		require.NotEmpty(t, first)
		require.Len(t, r.cache, 1)

		r.cache[mdCacheKey{key: "input:0", width: 40}] = mdCacheEntry{lines: []string{"sentinel"}}
		assert.Equal(t, []string{"sentinel"}, r.lines(mdDoc{key: "input:0", src: "hello there", width: 40}),
			"the second call must not re-render")
	})

	t.Run("the same key at a second width does not evict the first", func(t *testing.T) {
		r := testMDRenderer()
		narrow := r.lines(mdDoc{key: "input:0", src: "the quick brown fox jumps over the lazy dog", width: 30})
		wide := r.lines(mdDoc{key: "input:0", src: "the quick brown fox jumps over the lazy dog", width: 90})
		assert.NotEqual(t, narrow, wide, "two widths render differently")
		assert.Len(t, r.cache, 2)
		assert.Len(t, r.renderers, 2)
		assert.Equal(t, narrow, r.lines(mdDoc{key: "input:0", src: "the quick brown fox jumps over the lazy dog", width: 30}))
	})

	t.Run("two keys at one width do not collide", func(t *testing.T) {
		r := testMDRenderer()
		body := r.lines(mdDoc{key: "finding:0:body", src: "the body", width: 40})
		fix := r.lines(mdDoc{key: "finding:0:fix", src: "the fix", width: 40})
		assert.Contains(t, plainMD(body), "the body")
		assert.Contains(t, plainMD(fix), "the fix")
		assert.Len(t, r.cache, 2)
	})

	t.Run("a padded document is cached padded", func(t *testing.T) {
		r := testMDRenderer()
		out := r.lines(mdDoc{key: "finding:0:body", src: "the body", width: 40, indent: mdIndent})
		require.NotEmpty(t, out)
		assert.Equal(t, out, r.cache[mdCacheKey{key: "finding:0:body", width: 40}].lines,
			"the browser re-lays the whole report every frame, so the pad cannot sit outside the cache")
		for i, l := range out {
			assert.True(t, strings.HasPrefix(l, strings.Repeat(" ", mdIndent)), "line %d: %q", i, l)
			assert.LessOrEqual(t, lipgloss.Width(l), 40, "the pad comes off the width, line %d", i)
		}
	})

	t.Run("what a pass did not read is dropped when that pass ends", func(t *testing.T) {
		r := testMDRenderer()
		pass(r, mdDoc{key: "input:0", src: "first document", width: 40})
		require.Positive(t, r.cached)
		pastBound(r, mdCacheKey{key: "input:0", width: 40})

		pass(r, mdDoc{key: "input:1", src: "second document", width: 40})
		out := r.cache[mdCacheKey{key: "input:1", width: 40}].lines
		assert.Len(t, r.cache, 1, "the entry the finished pass did not ask for is gone")
		assert.NotEmpty(t, out, "and the one it read is what is held")
		assert.Equal(t, r.cached, sumLen(out), "the accounting follows what was actually dropped")
	})

	// pins the browser thrash: a report larger than mdMaxCache cleared the entries the same pass had
	// just made, so every repaint re-rendered all of it
	t.Run("the pass in progress keeps what it has already rendered", func(t *testing.T) {
		r := testMDRenderer()
		r.beginFrame()
		body := r.lines(mdDoc{key: "finding:0:body", src: "the body", width: 40})
		require.Len(t, r.cache, 1)

		pastBound(r, mdCacheKey{key: "finding:0:body", width: 40})
		r.lines(mdDoc{key: "finding:0:fix", src: "the fix", width: 40})
		r.endFrame()
		assert.Len(t, r.cache, 2, "one pass is one working set and no part of it is evicted from under it")
		assert.Equal(t, body, r.cache[mdCacheKey{key: "finding:0:body", width: 40}].lines)
		assert.Greater(t, r.cached, mdMaxCache, "the pass is the floor, so the bound gives way to it")
	})

	t.Run("a hit is what keeps an entry through the pass that read it", func(t *testing.T) {
		r := testMDRenderer()
		doc := mdDoc{key: "input:0", src: "first document", width: 40}
		pass(r, doc)
		first := r.cache[mdCacheKey{key: "input:0", width: 40}].lines
		pastBound(r, mdCacheKey{key: "input:0", width: 40})

		r.beginFrame()
		assert.Equal(t, first, r.lines(doc), "still a hit")
		require.Equal(t, r.frame, r.cache[mdCacheKey{key: "input:0", width: 40}].frame, "restamped by the read")
		r.endFrame()
		assert.Len(t, r.cache, 1, "so an entry read by the pass survives its own sweep")
	})

	// pins the hole the first fix left: eviction ran only when a miss was about to be inserted, so a
	// pass that was all hits — the browser narrowing a filter to findings it has already rendered —
	// left the rest of an oversized report resident with nothing to trigger the sweep
	t.Run("a pass of nothing but hits still evicts what it did not read", func(t *testing.T) {
		r := testMDRenderer()
		alpha := mdDoc{key: "finding:0:body", src: "the alpha body", width: 40}
		pass(r, alpha, mdDoc{key: "finding:1:body", src: "the beta body", width: 40})
		pastBound(r, mdCacheKey{key: "finding:1:body", width: 40})

		pass(r, alpha)
		assert.Equal(t, []mdCacheKey{{key: "finding:0:body", width: 40}}, slices.Collect(maps.Keys(r.cache)),
			"the filtered-out finding goes even though the pass rendered nothing")
	})

	// pins what round 4 found: eviction ran on the way into a pass, so the first pane to render no
	// document kept the whole report and only a second layout would have dropped it — and after a
	// completed run one keypress produces one View, with no second layout scheduled
	t.Run("a pass that reads nothing at all evicts before it returns", func(t *testing.T) {
		r := testMDRenderer()
		pass(r, mdDoc{key: "finding:0:body", src: "the alpha body", width: 40})
		pastBound(r, mdCacheKey{key: "finding:0:body", width: 40})

		pass(r)
		assert.Empty(t, r.cache, "one pane laid out with no document in it is enough")
		assert.Zero(t, r.cached)
	})

	t.Run("reset drops both maps and the accounting", func(t *testing.T) {
		r := testMDRenderer()
		r.lines(mdDoc{key: "input:0", src: "hello", width: 40})
		r.lines(mdDoc{key: "input:1", src: "hello", width: 90})
		require.Len(t, r.cache, 2)
		require.Len(t, r.renderers, 2)

		r.reset()
		assert.Empty(t, r.cache)
		assert.Empty(t, r.renderers)
		assert.Zero(t, r.cached)
		assert.NotEmpty(t, r.lines(mdDoc{key: "input:0", src: "hello", width: 40}), "and it still renders afterwards")
	})
}

func TestMDRenderer_renderBlockConstructs(t *testing.T) {
	r := testMDRenderer()

	t.Run("an ordered list marker is one space from its item", func(t *testing.T) {
		out := plainMD(r.render("1. `agtermTests` covers the `.idle` route\n2. plain item\n", 70))
		assert.Contains(t, out, "1. agtermTests covers the .idle route",
			"an item opening with a code span is still one space from the marker")
		assert.Contains(t, out, "2. plain item")
		assert.NotContains(t, out, "1.  ", "glamour spells the marker as the number plus a two-character block prefix")

		// the marker is the item's own number rather than its position, so a wider one still takes one space
		wide := plainMD(r.render("10. tenth item\n11. eleventh item\n", 70))
		assert.Contains(t, wide, "10. tenth item")
		assert.Contains(t, wide, "11. eleventh item")
	})

	t.Run("a GFM table renders as a table", func(t *testing.T) {
		out := plainMD(r.render("| flag | meaning |\n|---|---|\n| --task | the task id |\n", 60))
		assert.Contains(t, out, "│", "cells are separated by a column rule")
		assert.Contains(t, out, "┼", "and the header row by a box-drawing separator")
		assert.NotContains(t, out, "|---|---|", "the raw separator row is gone")
		assert.NotContains(t, out, "| flag |", "and so are the raw pipes")
		assert.Contains(t, out, "flag")
		assert.Contains(t, out, "the task id")
	})

	t.Run("a horizontal rule renders", func(t *testing.T) {
		out := plainMD(r.render("above\n\n---\n\nbelow", 60))
		assert.Contains(t, out, "--------", "the rule is drawn, not left as three literal dashes")
		assert.Contains(t, out, "above")
		assert.Contains(t, out, "below")
	})

	t.Run("inline code, italics, links and strikethrough render", func(t *testing.T) {
		lines := r.render("call `run()` with *care*, see [the docs](http://example.com/d), ~~not~~ now.", 70)
		out := plainMD(lines)
		assert.Contains(t, out, "run()")
		assert.Contains(t, out, "care")
		assert.NotContains(t, out, "*care*", "the emphasis markers are consumed")
		assert.Contains(t, out, "the docs")
		assert.Contains(t, out, "http://example.com/d", "the link target is shown")
		assert.NotContains(t, out, "[the docs]", "not as raw markdown")
		assert.Contains(t, out, "not")
		assert.NotContains(t, out, "~~not~~")
		assert.Contains(t, strings.Join(lines, "\n"), "\x1b[", "and each carries its own styling")
	})

	t.Run("a list renders with bullets", func(t *testing.T) {
		out := plainMD(r.render("- one\n- two\n", 60))
		assert.Contains(t, out, "• one")
		assert.Contains(t, out, "• two")
	})

	t.Run("a blockquote renders", func(t *testing.T) {
		assert.Contains(t, plainMD(r.render("> quoted prose\n", 60)), "│ quoted prose")
	})

	t.Run("a fenced block keeps its body and drops its delimiters", func(t *testing.T) {
		out := plainMD(r.render("```go\nfunc main() {}\n```\n", 60))
		assert.Contains(t, out, "func main() {}")
		assert.NotContains(t, out, "```")
	})

	t.Run("the same structure survives a colored profile", func(t *testing.T) {
		// every other case here runs at the profile the test surface reports, which emits no escapes at
		// all — so nothing else says the structure holds once glamour is actually painting
		for _, tt := range mdProfiles {
			cr := newMDRenderer(tt.profile, true)
			lines := cr.render("| flag | meaning |\n|---|---|\n| --task | the task id |\n\n- one\n", 60)
			out := plainMD(lines)
			assert.Contains(t, out, "│", tt.name)
			assert.NotContains(t, out, "|---|---|", tt.name)
			assert.Contains(t, out, "the task id", tt.name)
			assert.Contains(t, out, "• one", tt.name)
		}
	})

	t.Run("leading and trailing blank lines are dropped", func(t *testing.T) {
		out := r.render("only line", 60)
		require.NotEmpty(t, out)
		assert.NotEmpty(t, strings.TrimSpace(xansi.Strip(out[0])), "no blank line on top")
		assert.NotEmpty(t, strings.TrimSpace(xansi.Strip(out[len(out)-1])), "and none at the bottom")
	})
}

// glamour hands every raw-HTML node to a bluemonday StrictPolicy, which deletes it and leaves nothing
// behind — so `<task>` and `<T>`, which CommonMark reads as raw HTML, vanished mid-sentence with no
// marker that anything had been there.
func TestMDRenderer_renderKeepsAngleBracketText(t *testing.T) {
	r := testMDRenderer()

	t.Run("an unbackticked angle token survives in prose", func(t *testing.T) {
		out := plainMD(r.render("the round is <tasks-dir>/<task>/<run>/input/scope.md here", 78))
		assert.Contains(t, out, "<tasks-dir>/<task>/<run>/input/scope.md")
	})

	t.Run("a grammar of bracketed placeholders survives", func(t *testing.T) {
		out := plainMD(r.render("the grammar is <binary>[/<model>][:<effort>] and nothing else", 78))
		assert.Contains(t, out, "<binary>[/<model>][:<effort>]")
	})

	t.Run("a generic type in prose survives", func(t *testing.T) {
		assert.Contains(t, plainMD(r.render("the type is List<String> in java terms", 78)), "List<String>")
	})

	t.Run("an inline code span keeps its angle brackets", func(t *testing.T) {
		out := plainMD(r.render("call `f(x <T>)` with care", 78))
		assert.Contains(t, out, "f(x <T>)")
		assert.NotContains(t, out, "&lt;", "a code span is not a raw-HTML node, so nothing escaped it")
	})

	t.Run("a fenced block renders its angle brackets literally", func(t *testing.T) {
		out := plainMD(r.render("```go\nfunc f(x <T>) {}\n```\n", 78))
		assert.Contains(t, out, "func f(x <T>) {}")
		assert.NotContains(t, out, "&lt;", "an entity inside a fence is text, so the escape must not reach here")
		assert.NotContains(t, out, "&gt;")
	})

	t.Run("an indented block renders its angle brackets literally", func(t *testing.T) {
		out := plainMD(r.render("prose:\n\n    raw <T> here\n", 78))
		assert.Contains(t, out, "raw <T> here")
		assert.NotContains(t, out, "&lt;")
	})

	t.Run("a table cell keeps its angle token", func(t *testing.T) {
		out := plainMD(r.render("| a | b |\n|---|---|\n| <x> | y |\n", 60))
		assert.Contains(t, out, "<x>")
		assert.Contains(t, out, "│", "and it is still a table")
	})

	t.Run("an autolink is still a link", func(t *testing.T) {
		lines := r.render("see <http://example.com/d> for more", 78)
		assert.Contains(t, plainMD(lines), "http://example.com/d")
		assert.NotContains(t, plainMD(lines), "<http://example.com/d>",
			"an autolink is not a raw-HTML node either, so it is left to render as a link")
	})

	t.Run("a blockquote is still a blockquote", func(t *testing.T) {
		assert.Contains(t, plainMD(r.render("> quoted <T> line\n", 60)), "│ quoted <T> line",
			"the escape reaches raw HTML only, never a line-leading marker")
	})

	t.Run("an html block is shown rather than deleted", func(t *testing.T) {
		out := plainMD(r.render("<div>\n- item\n</div>\n", 60))
		assert.Contains(t, out, "<div>")
		assert.Contains(t, out, "</div>")
		assert.Contains(t, out, "• item")
	})

	t.Run("an entity already in the source is not double-escaped", func(t *testing.T) {
		out := plainMD(r.render("write &lt;T&gt; and &amp; too", 78))
		assert.Contains(t, out, "<T>")
		assert.Contains(t, out, "&", "the bare ampersand entity still resolves")
		assert.NotContains(t, out, "&amp;")
	})

	t.Run("an entity inside a raw-html span is shown as it was written", func(t *testing.T) {
		out := plainMD(r.render(`see <a href="?a=1&amp;b=2">it</a> there`, 78))
		assert.Contains(t, out, `<a href="?a=1&amp;b=2">`, "the ampersand is escaped ahead of the angle brackets")
		assert.Contains(t, out, "</a>")
	})

	t.Run("the two paths agree on what survives", func(t *testing.T) {
		src := "the round is <tasks-dir>/<task> here"
		assert.Contains(t, plainMD(r.render(src, 78)), "<tasks-dir>/<task>")
		assert.Contains(t, plainMD(r.inline(src, 78)), "<tasks-dir>/<task>",
			"the fallback never stripped it, so the document path must not either")
	})

	t.Run("the width invariant holds on angle-heavy prose", func(t *testing.T) {
		// the escaped form is longer than the rendered one, so a renderer wrapping the entities rather
		// than the text would break early — and one wrapping neither would overflow
		src := strings.Repeat("<tasks-dir>/<task>/<run> ", 12)
		for _, tt := range mdProfiles {
			for _, width := range []int{40, 60, 78} {
				lines := newMDRenderer(tt.profile, true).render(src, width)
				require.NotEmpty(t, lines)
				for i, l := range lines {
					assert.LessOrEqual(t, lipgloss.Width(l), width, "%s, line %d at width %d: %q", tt.name, i, width, l)
				}
			}
		}
	})

	t.Run("a document with no angle brackets is handed over untouched", func(t *testing.T) {
		src := "plain **prose** with `code` and a - list\n"
		assert.Equal(t, src, r.escapeHTML(src))
	})
}

func TestMDRenderer_renderKeepsHeadingHashes(t *testing.T) {
	r := testMDRenderer()
	out := plainMD(r.render("# One\n\n## Two\n\n### Three\n", 60))
	assert.Contains(t, out, "# One", "h1 keeps its hash rather than becoming a padded band")
	assert.Contains(t, out, "## Two")
	assert.Contains(t, out, "### Three")
}

func TestMDStyle(t *testing.T) {
	t.Run("h1 carries the markdown hash and no band, foreground included", func(t *testing.T) {
		for _, dark := range []bool{true, false} {
			s := mdStyle(dark)
			assert.Equal(t, "# ", s.H1.Prefix)
			assert.Nil(t, s.H1.BackgroundColor)
			// glamour spells h1 as pale yellow on purple in both styles, legible only against that band:
			// dropping the band alone leaves near-white text on a light terminal's white
			assert.Nil(t, s.H1.Color, "the foreground goes with the band, so h1 takes the document's own")
			assert.Equal(t, "## ", s.H2.Prefix, "the levels glamour already spells with hashes are untouched")
		}
	})

	t.Run("h1 is still marked out, by the hash and the bold", func(t *testing.T) {
		s := mdStyle(true)
		require.NotNil(t, s.H1.Bold)
		assert.True(t, *s.H1.Bold)
	})

	t.Run("an inline code span carries no padding and keeps its color", func(t *testing.T) {
		for _, dark := range []bool{true, false} {
			s := mdStyle(dark)
			assert.Empty(t, s.Code.Prefix, "glamour pads the span in its own style, which reads as a double space")
			assert.Empty(t, s.Code.Suffix)
			assert.NotNil(t, s.Code.Color, "the color is what marks the span once the pad is gone")
		}
	})

	t.Run("a rendered code span sits one space from its prose", func(t *testing.T) {
		r := testMDRenderer()
		out := plainMD(r.render("the guard is `err != nil` here", 60))
		assert.Contains(t, out, "the guard is err != nil here",
			"one space either side, where glamour's own pad would put two")
	})

	t.Run("the light style is not the dark one", func(t *testing.T) {
		assert.NotEqual(t, mdStyle(true), mdStyle(false))
		require.NotNil(t, mdStyle(true).Document.Color)
		require.NotNil(t, mdStyle(false).Document.Color)
		assert.NotEqual(t, *mdStyle(true).Document.Color, *mdStyle(false).Document.Color)
	})
}

func TestMDRenderer_renderFitsWidth(t *testing.T) {
	long := strings.Repeat("a", 300)
	src := "# A heading long enough to need breaking across more than one row of the pane\n\n" +
		"paragraph text that runs well past any of the widths under test and has to be broken up.\n\n" +
		"| a very wide column header | another very wide column header | and a third one |\n" +
		"|---|---|---|\n" +
		"| " + strings.Repeat("x", 60) + " | " + strings.Repeat("y", 60) + " | " + strings.Repeat("z", 60) + " |\n\n" +
		"```go\nfunc main() { println(\"" + long + "\") }\n```\n\n" +
		"- a list item that is also longer than the narrowest width under test by a good margin\n"

	// across every color profile, not only the test surface's: a colored profile puts escapes inside the
	// line, which is what lipgloss.Width and the hard wrap have to see past
	for _, tt := range mdProfiles {
		t.Run(tt.name, func(t *testing.T) {
			for _, width := range []int{60, 80, 200} {
				r := newMDRenderer(tt.profile, true)
				lines := r.render(src, width)
				require.NotEmpty(t, lines)
				for i, l := range lines {
					assert.LessOrEqual(t, lipgloss.Width(l), width, "line %d at width %d: %q", i, width, l)
				}
				assert.Contains(t, plainMD(lines), "a"+strings.Repeat("a", 20),
					"the code line survives, it is only broken up")
			}
		})
	}
}

// glamour's own document margin, two columns each side, which it deducts from the wrap width
const mdDocMargin = 2

// pins what glamour v1 got wrong: it wrapped a paragraph twice, and reflow's wrapper did not count a
// hyphen toward the line length, so the second wrap orphaned the last word onto a row of its own.
func TestMDRenderer_renderKeepsHyphenatedParagraphsWhole(t *testing.T) {
	src := "A Go CLI that runs a structured multi-agent code review by spawning and supervising " +
		"`claude --print` and `codex exec` subprocesses, then returns findings on stdout. It exists " +
		"because agent fan-out driven from inside an AI coding session is unobservable and " +
		"unrecoverable, so the caller has no timeout, no kill, no retry and no progress.\n"

	for _, width := range []int{60, 76, 80, 86, 100} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			r := newMDRenderer(termenv.Ascii, true)
			lines := r.render(src, width)
			require.NotEmpty(t, lines)
			for i := range len(lines) - 1 {
				cur := strings.TrimSpace(xansi.Strip(lines[i]))
				next := strings.Fields(xansi.Strip(lines[i+1]))
				if cur == "" || len(next) == 0 {
					continue
				}
				// a greedy wrapper breaks only where the next word no longer fits, so a line that
				// still has room for the first word of the line below it was broken early
				assert.Greater(t, len(cur)+1+len(next[0]), width-2*mdDocMargin,
					"line %d had room for %q at width %d: %q", i, next[0], width, cur)
			}
		})
	}
}

func TestMDRenderer_renderJoinsSoftBreaks(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
		notWant   []string
	}{
		{name: "tight item", src: "- one item written across\n  two source lines\n",
			want: []string{"one item written across two source lines"}},
		{name: "continuation opening with a code span", src: "- the caller, which post-processes in\n  `trim`\n",
			want: []string{"the caller, which post-processes in trim"}},
		{name: "loose item", src: "- one item across\n  two lines\n\n- second\n",
			want: []string{"one item across two lines", "second"}},
		{name: "nested item", src: "- outer across\n  two lines\n  - inner across\n    two lines\n",
			want: []string{"outer across two lines", "inner across two lines"}},
		{name: "hard break stays", src: "- first half  \n  second half\n",
			want: []string{"first half\n", "second half"}},
		{name: "fence keeps its lines", src: "- item\n\n  ```go\n  a := 1\n  b := 2\n  ```\n",
			want: []string{"a := 1\n", "b := 2"}},
		{name: "table keeps its rows", src: "| a | b |\n|---|---|\n| 1 | 2 |\n", want: []string{"1", "2"}},
		{name: "crlf item", src: "- one item written across\r\n  two source lines\r\n",
			want: []string{"one item written across two source lines"}},
		// a blockquote continuation opens with `>`, which is syntax: joining across it splices the
		// marker into the text and the pane shows a character the document does not have
		{name: "tight definition description", src: "Term\n: description across\n  two source lines\n",
			want: []string{"description across two source lines"}},
		{name: "blockquoted definition list is left alone", src: "> Term\n> : description across\n>   two source lines\n",
			want: []string{"description across"}, notWant: []string{"across >"}},
		{name: "blockquote is left alone", src: "> first line\n> second line\n",
			want: []string{"first line"}, notWant: []string{"first line >"}},
		{name: "blockquoted item is left alone", src: "> - one item across\n>   two source lines\n",
			want: []string{"one item across"}, notWant: []string{"across >"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newMDRenderer(termenv.Ascii, true)
			lines := r.render(tt.src, 60)
			for _, want := range tt.want {
				assert.Contains(t, plainMD(lines), want)
			}
			for _, no := range tt.notWant {
				assert.NotContains(t, plainMD(lines), no, "a marker the document does not carry")
			}
		})
	}
}

func TestMDRenderer_renderDownsamplesToProfile(t *testing.T) {
	src := "# heading\n\nsome **bold** prose and `a code span` in it.\n"
	for _, tt := range mdProfiles {
		t.Run(tt.name, func(t *testing.T) {
			out := strings.Join(newMDRenderer(tt.profile, true).render(src, 60), "\n")
			assert.Contains(t, xansi.Strip(out), "a code span", "the text survives every profile")
			// glamour's styles are spelled in 256-color codes, so a profile below that must not carry one
			if tt.profile == termenv.Ascii || tt.profile == termenv.ANSI {
				assert.NotContains(t, out, "38;5;", "a 256-color code reached a profile that cannot show it")
				assert.NotContains(t, out, "48;5;")
				return
			}
			assert.Contains(t, out, "38;5;", "256-color and truecolor keep the style's own colors")
		})
	}
}

// the nested-list case pins expansion running ahead of the parser: at the terminal's eight-column stop
// a list nested under a tab lands past the four-space code-block threshold CommonMark measures against,
// and the nested item comes back as literal "- nested" text with its own bullet gone.
func TestMDRenderer_renderExpandsTabs(t *testing.T) {
	// a tab is one cell to lipgloss and up to eight to the terminal, so an unexpanded row is measured
	// short and drawn wide — straight past the edge of the pane. Models write Go tab-indented.
	src := "```go\nif err != nil {\n\t\t\t\t\t\treturn fmt.Errorf(\"write: %w\", err)\n}\n```\n"
	for _, tt := range mdProfiles {
		t.Run(tt.name, func(t *testing.T) {
			r := newMDRenderer(tt.profile, true)
			lines := r.lines(mdDoc{key: "finding:0:fix", src: src, width: 58})
			require.NotEmpty(t, lines)
			for i, l := range lines {
				assert.NotContains(t, l, "\t", "line %d still carries a raw tab: %q", i, l)
				assert.LessOrEqual(t, lipgloss.Width(l), 58, "line %d: %q", i, l)
			}
			assert.Contains(t, plainMD(lines), "return fmt.Errorf", "and the snippet itself survives")
		})
	}

	t.Run("a tab-indented nested list keeps its nesting", func(t *testing.T) {
		r := testMDRenderer()
		out := plainMD(r.render("- first\n\t- nested\n", 40))
		assert.Equal(t, plainMD(r.render("- first\n    - nested\n", 40)), out,
			"a tab indents exactly as the four spaces CommonMark reads it as")

		rows := []string{}
		for l := range strings.SplitSeq(out, "\n") {
			if strings.TrimSpace(l) != "" {
				rows = append(rows, l)
			}
		}
		require.Len(t, rows, 2)
		assert.Contains(t, rows[0], "• first")
		assert.Contains(t, rows[1], "• nested", "the nested item is a bullet, not literal markdown")
		assert.NotContains(t, out, "- nested", "and its marker never reaches the screen")
		assert.Greater(t, len(rows[1])-len(strings.TrimLeft(rows[1], " ")),
			len(rows[0])-len(strings.TrimLeft(rows[0], " ")), "and it is indented under the first")
	})

	t.Run("each line's tab stops start at its own column", func(t *testing.T) {
		r := testMDRenderer()
		out := plainMD(r.render("    a\tb\n    c\td\n", 60))
		assert.Contains(t, out, "a   b", "a tab at column 5 runs to the stop at column 8")
		// the discriminating half: with the column carried across the newline the second line would
		// start at 9 and its tab would run only to 16, leaving two spaces instead of three
		assert.Contains(t, out, "c   d", "and the second line is measured from its own start")
		assert.NotContains(t, out, "c  d", "not from where the first one ended")
	})
}

func TestMDRenderer_renderCapsDocumentSize(t *testing.T) {
	// one unit is a table plus a fence plus a paragraph — the construct mix whose rendered ANSI runs
	// tens of times its source, which is what the cap is measured against
	unit := "| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {\n\tprintln(\"x\")\n}\n```\n\nsome **prose** with `code` in it.\n\n"
	grow := func(n int) string {
		var b strings.Builder
		for b.Len() <= n {
			b.WriteString(unit)
		}
		return b.String()
	}

	t.Run("under the cap renders as a document", func(t *testing.T) {
		src := grow(mdMaxDoc - len(unit) - 1)
		require.LessOrEqual(t, len(src), mdMaxDoc)
		out := plainMD(testMDRenderer().render(src, 60))
		assert.Contains(t, out, "│", "the table is drawn")
		assert.NotContains(t, out, "|---|---|", "and its separator row is consumed")
		assert.NotContains(t, out, "**prose**", "and inline emphasis is consumed too")
	})

	t.Run("over the cap falls through to the inline path", func(t *testing.T) {
		src := grow(mdMaxDoc)
		require.Greater(t, len(src), mdMaxDoc)
		out := plainMD(testMDRenderer().render(src, 60))
		assert.Contains(t, out, "|---|---|", "the document renderer never saw it")
		assert.NotContains(t, out, "│", "so no table is drawn")
		assert.Contains(t, out, "prose", "but the text is all still there, one line at a time")
		assert.NotContains(t, out, "**prose**", "with the inline renderer's own spans applied")
		assert.Contains(t, out, `println("x")`, "and the fenced snippet survives rather than being cut")
	})

	t.Run("over the cap the output stays the size of the source", func(t *testing.T) {
		// the cost the cap exists to bound, asserted on its cause rather than on a clock: glamour pads
		// every row to the wrap width with one escape pair per trailing space, so its output is a
		// multiple of the source while the inline path is roughly the source itself
		r := newMDRenderer(termenv.TrueColor, true)

		under := grow(mdMaxDoc - len(unit) - 1)
		doc := sumLen(r.render(under, 100))
		assert.Greater(t, doc, 4*len(under), "a document render multiplies its source several times over")

		over := grow(mdMaxDoc)
		inline := sumLen(r.render(over, 100))
		assert.Less(t, inline, 2*len(over), "the inline path does not, which is what caps the retained cache")
	})

	t.Run("the width invariant holds on the fallback", func(t *testing.T) {
		long := strings.Repeat("z", 300)
		src := grow(mdMaxDoc) + "\n```go\nfunc main() { println(\"" + long + "\") }\n```\n\n\tdeep\ttabs\there\n"
		require.Greater(t, len(src), mdMaxDoc)
		for _, tt := range mdProfiles {
			t.Run(tt.name, func(t *testing.T) {
				for _, width := range []int{60, 80, 200} {
					lines := newMDRenderer(tt.profile, true).render(src, width)
					require.NotEmpty(t, lines)
					for i, l := range lines {
						assert.LessOrEqual(t, lipgloss.Width(l), width, "line %d at width %d: %q", i, width, l)
						assert.NotContains(t, l, "\t", "line %d still carries a raw tab: %q", i, l)
						assert.NotContains(t, l, "\n", "line %d carries an embedded newline: %q", i, l)
					}
				}
			})
		}
	})
}

func TestMDRenderer_renderFallsBackPerLine(t *testing.T) {
	r := testMDRenderer()
	require.Nil(t, r.build(0), "no renderer can be built without a width")

	src := "alpha\n\nbeta gamma\n- delta"
	out := r.render(src, 0)
	require.Len(t, out, 4, "the fallback returns one slice element per source line")
	assert.Equal(t, []string{"alpha", "", "beta gamma", "- delta"}, out,
		"and each carries its own line's text rather than an empty placeholder")
	for _, l := range out {
		assert.NotContains(t, l, "\n", "and never a single element with embedded newlines")
	}
}
