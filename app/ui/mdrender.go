package ui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	gstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gparser "github.com/yuin/goldmark/parser"
	gtext "github.com/yuin/goldmark/text"
)

// mdKey names a cached document. A named type rather than a bare string because lines takes the key
// and the source next to each other: swapped, they cache under the document text and render the key,
// which fails silently in the pane instead of at the call.
type mdKey string

type mdCacheKey struct {
	key   mdKey
	width int
}

// mdDoc is one request for a rendered document: what names it, what it says, the pane width it has to
// fit, and the left pad the pane puts in front of it.
//
// The pad is part of the request rather than something the caller applies afterwards because it is
// part of what is cached. The findings browser re-lays the whole report on every frame and only the
// pane slices to the visible window, so padding outside the cache is one allocation per line of the
// report per repaint.
type mdDoc struct {
	key    mdKey
	src    string
	width  int
	indent int
}

// mdCacheEntry is one rendered document: its pane lines, the bytes they hold, and the frame that last
// asked for it. The frame stamp is what eviction reads, and the reason it is here rather than derived
// is that a pane asks for its documents one at a time — nothing else knows which of them belong
// together.
type mdCacheEntry struct {
	lines []string
	size  int
	frame uint64
}

// mdRenderer turns markdown documents into pane lines through glamour.
//
// One glamour renderer per width, because WithWordWrap bakes the width in and two panes ask for two
// different widths — a single renderer rebuilt on every width change would thrash on every keypress
// that switches panes.
type mdRenderer struct {
	style     ansi.StyleConfig
	profile   termenv.Profile
	parser    gparser.Parser
	renderers map[int]*glamour.TermRenderer
	cache     map[mdCacheKey]mdCacheEntry
	cached    int    // bytes of rendered output the cache is holding
	frame     uint64 // the layout pass in progress, stamped onto every entry it touches
}

// newMDRenderer takes the two terminal facts the renderer needs. Both are read from the same lipgloss
// renderer newStyles builds against the tty, so the document panes and the frame never disagree about
// what the terminal can do.
func newMDRenderer(profile termenv.Profile, dark bool) *mdRenderer {
	return &mdRenderer{
		style:   mdStyle(dark),
		profile: profile,
		// the same construction glamour makes in NewTermRenderer, so escapeHTML sees the document
		// glamour will see and the offsets it collects address the same bytes
		parser: goldmark.New(
			goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
			goldmark.WithParserOptions(gparser.WithAutoHeadingID()),
		).Parser(),
		renderers: make(map[int]*glamour.TermRenderer),
		cache:     make(map[mdCacheKey]mdCacheEntry),
	}
}

// mdStyle picks glamour's base style for the terminal's background and puts h1's markdown hash back.
//
// glamour renders h1 as a padded band with a background instead of a prefix, which breaks the rule
// that a pane shows the same heading a reader sees in the file open beside it — h2 and h3 keep their
// hashes, so the rule would break at exactly one level. Restoring the prefix also drops a purple band
// that has nothing to do with this palette.
//
// **The foreground goes with the band.** Both of glamour's styles spell h1 as a pale yellow (228) on
// that purple, which is legible only against it: dropping the band alone leaves near-white text on a
// light terminal's white. Cleared, h1 inherits the document's own color and is marked by its bold and
// its hash, exactly as h2 and h3 are.
func mdStyle(dark bool) ansi.StyleConfig {
	s := gstyles.LightStyleConfig
	if dark {
		s = gstyles.DarkStyleConfig
	}
	s.H1.Prefix = "# "
	s.H1.Suffix = ""
	s.H1.BackgroundColor = nil
	s.H1.Color = nil
	return s
}

// mdMaxCache is the rendered-byte threshold at which a closing pass drops what it did not read. It is
// not a ceiling: the pass's own working set stays whatever it comes to, so the cache sits above this
// value for as long as one pane holds that much.
//
// Without a threshold at all the cache only ever shrinks on a width change, so at a stable width it
// grows for as long as the reader keeps opening things. mdMaxDoc bounds one document and not the sum:
// the snapshotter admits 128 context files, every non-empty markdown one of them at or under mdMaxDoc
// takes the document path, and a rendered document runs many times its source — so the markdown tabs
// of one snapshot alone can retain an order of magnitude more than the snapshot itself.
//
// There is nothing here worth an LRU: a miss costs one render of one document, and the entries worth
// keeping are the ones the closing pass read. evict draws that line.
const mdMaxCache = 32 << 20

// beginFrame opens a layout pass, and endFrame closes it, evicting what that pass did not read if the
// cache has gone past mdMaxCache.
//
// **A pass is one pane laid out whole, and every pane runs one — not only the two that render
// documents.** reviewPaneLines and inputLinesAt are where the pair is called, so the log panes, an
// agent's scrollback, a verbatim file and an empty tab all open a pass and read nothing. Scoping it to
// the document renderers instead leaves the browser's own entries looking current for as long as the
// reader stays on a log, with no boundary coming to sweep them. A pane is sometimes laid out twice for
// one repaint, since maxScroll measures it by building it; a pass that measured a pane is as complete
// as one that drew it, so each is its own.
//
// **The two are separate calls because a pass has to be swept at its own end, not at the next one's
// start.** Evicting on the way in keeps whatever the previous pass read, so the first log layout after
// a report retains the whole report and only a second one would drop it — and a second layout is not
// something to rely on: after a completed run with no auto-exit, a keypress can be the last message
// the model sees. Sweeping on the way out drops it during that first layout instead.
func (r *mdRenderer) beginFrame() { r.frame++ }

// endFrame is the only place the bound evicts anything — reset is the other way an entry leaves, and
// it drops the whole cache on a width change rather than weighing it against mdMaxCache. It is
// deferred by each pane entry point, so a pane that returns early is still a closed pass.
//
// **Eviction belongs at a pass boundary rather than at an insertion.** A pass is not made of inserts:
// the browser re-lays the same report on every repaint, so a pass that narrows the filter to findings
// already rendered is entirely cache hits, and one whose filter matches nothing asks for no document
// at all. Neither reaches an insert, so a cache left over the bound would sit there until some
// unrelated miss.
func (r *mdRenderer) endFrame() {
	if r.cached > mdMaxCache {
		r.evict()
	}
}

// lines renders one document to pane lines, caching by key and width. The width in the key is the
// pane's, and the document is rendered into what is left of it once the pad is taken off.
//
// A hit is restamped with the current frame, which is how a pass tells eviction what it read. That is
// all it says: the next pass may narrow a filter, or belong to another pane and read none of it.
func (r *mdRenderer) lines(d mdDoc) []string {
	ck := mdCacheKey{key: d.key, width: d.width}
	if e, ok := r.cache[ck]; ok {
		if e.frame != r.frame {
			e.frame = r.frame
			r.cache[ck] = e
		}
		return e.lines
	}

	out := r.render(d.src, d.width-d.indent)
	if d.indent > 0 {
		pad := strings.Repeat(" ", d.indent)
		for i, l := range out {
			out[i] = pad + l
		}
	}

	size := 0
	for _, l := range out {
		size += len(l)
	}
	r.cache[ck] = mdCacheEntry{lines: out, size: size, frame: r.frame}
	r.cached += size
	return out
}

// evict drops every entry the pass that has just ended did not ask for, and keeps every entry it did.
// Only endFrame calls it, and only over the bound.
//
// **That pass's own working set is the floor, and it may carry the cache past mdMaxCache.** Neither
// the number of findings nor the length of one body is bounded, so a report whose rendered bodies and
// fixes come to more than the threshold is reachable — and dropping one of its entries drops one the
// next pass asks for immediately, since the browser re-lays the whole report on every repaint.
// Cleared whole, that report re-renders in full on every frame, keypress and scroll, which is the
// browser becoming unusable rather than a slow first paint. One pane's working set is bounded by that
// pane's own content; re-rendering it forever is bounded by nothing.
func (r *mdRenderer) evict() {
	for k, e := range r.cache {
		if e.frame == r.frame {
			continue
		}
		delete(r.cache, k)
		r.cached -= e.size
	}
}

// mdMaxDoc caps the source a document render is attempted on, in bytes.
//
// The ceiling exists because glamour's cost is structural rather than content-dependent: its Document
// style carries a color, so every emitted row is padded to the wrap width with one SGR pair per
// trailing space, and the output runs 2x to 80x the source. The render is synchronous on the
// bubbletea goroutine, and the result is held by the lines cache — so a large input both stalls
// Update, during which the pipeline's event channel drops, and is then retained.
//
// The value is measured against this package's own style at width 100. Fenced code and tables are the
// worst class: 64 KiB of it renders in ~110ms into ~3.4 MB, 1 MiB into ~2.8s and ~84 MB. The
// snapshotter allows 1 MiB per file, so without a cap here that limit alone permits the second case.
// 64 KiB keeps the worst case to roughly one dropped frame, and leaves every realistic review input —
// a scope, a goal, a diff excerpt, this repo's own 53 KiB README — on the document path.
const mdMaxDoc = 64 << 10

// render is the uncached path, and the one place tabs are expanded — behind the cache, so a large
// document pays for it once, and on every caller's behalf, so no pane can forget it. A tab is one
// cell to lipgloss and up to eight to the terminal, which is what lets an unexpanded row escape the
// frame; a tab-indented snippet inside a fence is the common case, since that is how a model writes
// Go. It expands at mdTabStop rather than the terminal's stop for the reason recorded on the two
// constants, and per line because expandTabs never resets its column on a newline.
//
// It is also where the raw HTML a model never meant as HTML is escaped — see escapeHTML — so the two
// paths agree on what text survives.
//
// A source over mdMaxDoc, a construction failure or a render failure all fall back to the inline
// renderer, which is the same one the log panes use — so an oversized input is still fully readable
// markdown, one line at a time, rather than blank or truncated. The oversized case is decided before
// anything is split or expanded: the ceiling exists to keep a large document off the costly path, and
// preparing it first would charge the whole cost to exactly the input that is not going to use it.
func (r *mdRenderer) render(src string, width int) []string {
	if len(src) <= mdMaxDoc {
		if tr := r.build(width); tr != nil {
			lines := strings.Split(src, "\n")
			for i, line := range lines {
				lines[i] = expandTabs(line, mdTabStop)
			}
			if out, err := tr.Render(r.escapeHTML(strings.Join(lines, "\n"))); err == nil {
				return r.trim(out, width)
			}
		}
	}
	return r.inline(src, width)
}

// mdEscaper turns the three characters that make a span parse as raw HTML into the entities that
// survive the sanitizer. Ampersand first is what a single-pass replacer gives for free: it never
// revisits what it has written, so an ampersand already in the text is escaped once.
var mdEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// escapeHTML entity-escapes exactly the spans goldmark reads as raw HTML, so glamour prints them
// instead of deleting them.
//
// glamour hands every raw-HTML node to a bluemonday StrictPolicy, which strips it whole and leaves
// nothing in its place. CommonMark reads `<task>`, `<T>` and `<binary>` as raw HTML, so a path
// template or a type parameter written into a finding body or a caller's scope.md disappears
// mid-sentence with no marker that anything was there — the inline path keeps that text verbatim, and
// a forensic pane silently dropping it is worse than clipping it.
//
// **The spans come from a parse, not from a scan for angle brackets.** Escaping every `<` would reach
// into fenced and indented code, where an entity is literal text and `&lt;` is what a reader would
// see; it would also break autolinks and, escaping `>` with it, blockquotes. A raw-HTML node is none
// of those by construction: goldmark has already decided that a code span, a code block, an autolink
// and a blockquote marker are not it.
//
// Escaping makes glamour reparse the span as text rather than HTML, so an HTML *block* no longer
// swallows the lines under it. That changes the block structure of exactly the input whose lines are
// invisible today, and in the direction of showing them.
func (r *mdRenderer) escapeHTML(src string) string {
	if !strings.Contains(src, "<") {
		return src
	}

	var spans [][2]int
	add := func(s gtext.Segment) {
		if s.Start >= 0 && s.Stop > s.Start {
			spans = append(spans, [2]int{s.Start, s.Stop})
		}
	}
	doc := r.parser.Parse(gtext.NewReader([]byte(src)))
	err := gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *gast.RawHTML:
			for i := range n.Segments.Len() {
				add(n.Segments.At(i))
			}
		case *gast.HTMLBlock:
			lines := n.Lines()
			for i := range lines.Len() {
				add(lines.At(i))
			}
			if n.HasClosure() {
				add(n.ClosureLine)
			}
		}
		return gast.WalkContinue, nil
	})
	if err != nil || len(spans) == 0 {
		return src
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })

	var out strings.Builder
	out.Grow(len(src) + 4*len(spans))
	last := 0
	for _, sp := range spans {
		if sp[0] < last || sp[1] > len(src) {
			continue
		}
		out.WriteString(src[last:sp[0]])
		out.WriteString(mdEscaper.Replace(src[sp[0]:sp[1]]))
		last = sp[1]
	}
	out.WriteString(src[last:])
	return out.String()
}

// inline renders a document through the one-line-at-a-time path, expanding each line's tabs as it
// reaches it. **No element it returns carries an embedded newline**, which is what the pane model
// addresses a document by — Wrap over a whole document returns a single element with newlines inside
// it whenever the longest line already fits, so this goes line by line. A source line too wide for
// the pane still becomes several elements.
func (r *mdRenderer) inline(src string, width int) []string {
	out := []string{}
	for line := range strings.SplitSeq(src, "\n") {
		out = append(out, Wrap("", markdown(expandTabs(line, mdTabStop)), width)...)
	}
	return out
}

// build gets or creates the renderer for width. It returns nil rather than an error because every
// caller treats a construction failure as "fall back" rather than propagating it — and it returns nil
// for a width below one before glamour is asked for anything, which is the only nil a caller can
// bring about and therefore the branch that makes the fallback reachable outside a forced failure.
//
// The style and the color profile are always explicit: glamour reads os.Stdout and the terminal's
// background only on its AutoStyle path, and stdout is a pipe whenever the report is redirected.
func (r *mdRenderer) build(width int) *glamour.TermRenderer {
	if width < 1 {
		return nil
	}
	if tr, ok := r.renderers[width]; ok {
		return tr
	}
	tr, err := glamour.NewTermRenderer(
		glamour.WithStyles(r.style),
		glamour.WithWordWrap(width),
		glamour.WithColorProfile(r.profile),
	)
	if err != nil {
		return nil
	}
	r.renderers[width] = tr
	return tr
}

// trim splits a rendered document into pane lines, drops the blank lines glamour brackets a document
// with, and hard-wraps whatever is still too wide — a long line inside a fence is not wrapped by
// glamour, and clipping it would cut the tail of exactly the line worth reading.
//
// The document margin is left alone: glamour already deducts it from the wrap width, and stripping it
// would cut into a code row that opens with a background sequence.
func (r *mdRenderer) trim(out string, width int) []string {
	lines := strings.Split(out, "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(xansi.Strip(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(xansi.Strip(lines[end-1])) == "" {
		end--
	}

	res := make([]string, 0, end-start)
	for _, line := range lines[start:end] {
		if lipgloss.Width(line) <= width {
			res = append(res, line)
			continue
		}
		res = append(res, strings.Split(xansi.Hardwrap(line, width, true), "\n")...)
	}
	return res
}

// reset drops the renderers and the cache, bounding memory by the widths in use rather than by every
// width the terminal has ever been. It is not a correctness mechanism: both maps are keyed by width,
// so a resize renders at the new width without it.
func (r *mdRenderer) reset() {
	r.renderers = make(map[int]*glamour.TermRenderer)
	r.cache = make(map[mdCacheKey]mdCacheEntry)
	r.cached = 0
}
