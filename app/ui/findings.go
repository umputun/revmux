package ui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/umputun/revmux/app/finding"
)

// CompletedMsg carries the finished report into the model. It arrives as a bubbletea message rather than
// a pipeline event because the event channel drops under load, and a dropped completion would park the
// reader on the agent panes forever. The model holds the report only to render it.
type CompletedMsg struct {
	Report finding.Report
}

// findingsState is the report as the browser holds it: the run's findings ordered by severity, and which
// of them the filter admits. It renders the report and nothing more — folding, a cursor and per-row
// expansion were all built here and removed, each having put part of the review behind a keystroke.
type findingsState struct {
	rows    []finding.Finding
	matches []int
	query   string
	typing  bool
	md      *mdRenderer
}

// mdIndent is what the browser puts in front of a rendered document. glamour's own document margin
// supplies two more columns, so a body lines up with the four-column pad indent gives the single-line
// rows beside it.
const mdIndent = 2

// findingRow is a finding's index into rows, and the one place its cache-key format is written down.
// That index is the finding's own, never its position in the filtered slice: keyed on the latter, typing
// a filter serves the previous finding's body at the same width.
type findingRow int

func (row findingRow) key(part string) mdKey {
	return mdKey("finding:" + strconv.Itoa(int(row)) + ":" + part)
}

// severityRank orders the browser's groups; anything unrecognized sorts last rather than being
// dropped, so a typo in a model's answer stays visible.
var severityRank = map[finding.Severity]int{finding.Critical: 0, finding.Major: 1, finding.Minor: 2}

// newFindings builds the browser over a finished report, worst findings first. Open questions,
// pre-existing and immaterial findings are deliberately not listed: mixing them in would put unranked
// material above a critical bug. The browser holds no Model reference and must not gain one.
func newFindings(rep finding.Report, md *mdRenderer) *findingsState {
	f := &findingsState{rows: slices.Clone(rep.Findings), md: md}
	slices.SortStableFunc(f.rows, func(a, b finding.Finding) int { return f.rank(a.Severity) - f.rank(b.Severity) })
	f.filter("")
	return f
}

// filter narrows the list to findings whose title, file or body carry q, case-insensitively. It holds
// no position of its own: the pane owns the scrolling, and editFilter is what returns it to the top of
// a narrowed report.
func (f *findingsState) filter(q string) {
	f.query = q
	needle := strings.ToLower(q)
	f.matches = make([]int, 0, len(f.rows))
	for i, r := range f.rows {
		if needle == "" || strings.Contains(strings.ToLower(r.Title+" "+r.File+" "+r.Body), needle) {
			f.matches = append(f.matches, i)
		}
	}
}

// findingsPane is the findings browser, which holds nothing until the report arrives.
func (m Model) findingsPane() []string {
	if m.findings == nil {
		return []string{"the review is still running..."}
	}
	return m.findings.render(m.view.width())
}

// render lays the report out the way the rendered report does: a severity heading, then each finding as
// its title, where it is, its body, its fix and its attribution. Nothing is clipped. It walks every
// finding it is showing on every repaint, and those documents are that pass's working set; the pass
// itself is run by reviewPaneLines, since a pane that renders no document has to close this one.
func (f *findingsState) render(width int) []string {
	lines := []string{}
	if f.typing || f.query != "" {
		lines = append(lines, f.prompt())
	}

	if len(f.matches) == 0 {
		return append(lines, f.empty())
	}
	// walking matches rather than a second slice built beside it, so what reaches detail is the
	// finding's own index into rows — see findingRow
	var prev finding.Severity
	for n, row := range f.matches {
		v := f.rows[row]
		if n == 0 || v.Severity != prev {
			lines = append(lines, f.heading(v.Severity))
		}
		prev = v.Severity
		lines = append(lines, f.rowLines(v, width)...)
		lines = append(lines, f.detail(v, findingRow(row), width)...)
	}
	return lines
}

// prompt is the filter line, showing the caret while the query is being typed and what it kept once
// it is not.
func (f *findingsState) prompt() string {
	if f.typing {
		return "filter: " + f.query + "_"
	}
	return "filter: " + f.query + " (" + strconv.Itoa(len(f.matches)) + " of " + strconv.Itoa(len(f.rows)) + ")"
}

func (f *findingsState) empty() string {
	if len(f.rows) == 0 {
		return "no findings."
	}
	return "nothing matches " + strconv.Quote(f.query) + "."
}

// heading is the report's own "## Major", so the pane and the file read the same. The title-casing is
// Severity.Heading's, not a second copy: a codex source is not schema-constrained, so a severity can
// arrive as any string and a byte index into it can split a rune.
func (f *findingsState) heading(s finding.Severity) string {
	return heading(2, s.Heading())
}

// rowLines is one finding's headline, wrapped, with each row self-contained. Render first, wrap second,
// then close every row: wrapping plain text and rendering each row afterwards splits a span across the
// boundary and the delimiters print literally, and SGR state survives a newline, so a row that opens the
// heading style and does not close it paints everything after it.
func (f *findingsState) rowLines(v finding.Finding, width int) []string {
	rows := Wrap("", heading(3, v.Title+"  ["+strconv.Itoa(v.Confidence)+"]"), width)
	for i, r := range rows {
		if !strings.HasPrefix(r, ansiHeadOn) {
			r = ansiHeadOn + r
		}
		if !strings.HasSuffix(r, ansiHeadOff) {
			r += ansiHeadOff
		}
		rows[i] = r
	}
	return rows
}

// detail is the rest of the report's entry, in the report's own order: where it is, the body, the fix,
// the verifier's reason, then the attribution, all on screen without a keystroke. The body and the fix are rendered as
// documents and separately, never composed into one: an unbalanced fence in a body would otherwise
// swallow the fix and the attribution under it.
func (f *findingsState) detail(v finding.Finding, row findingRow, width int) []string {
	out := f.indent(v.Location(), width)
	if v.Body != "" {
		out = append(out, "")
		out = append(out, f.document(row.key("body"), v.Body, width)...)
	}
	if v.Fix != "" {
		// the label is its own markdown line rather than a "fix: " prefix: prefixed, a fix opening with
		// a fence or a bullet stops being a block and renders as literal punctuation
		out = append(out, "")
		out = append(out, f.document(row.key("fix"), "fix:\n"+v.Fix, width)...)
	}
	if v.Reason != "" {
		out = append(out, "")
		out = append(out, f.indent("verifier: "+v.Reason, width)...)
	}
	if meta := f.meta(v); meta != "" {
		out = append(out, "")
		out = append(out, f.indent(meta, width)...)
	}
	return append(out, "")
}

// document renders one of a finding's markdown parts, padded so it lines up with the single-line rows
// around it. The pad goes to the renderer rather than being applied here: it comes off the pane width so
// the two together still fit, and it is part of what the renderer caches.
func (f *findingsState) document(key mdKey, src string, width int) []string {
	return f.md.lines(mdDoc{key: key, src: src, width: width, indent: mdIndent})
}

func (f *findingsState) meta(v finding.Finding) string {
	parts := []string{}
	if len(v.Sources) > 0 {
		parts = append(parts, "sources: "+strings.Join(v.Sources, ", "))
	}
	if len(v.Lenses) > 0 {
		parts = append(parts, "lenses: "+strings.Join(v.Lenses, ", "))
	}
	if v.Verdict != "" {
		parts = append(parts, "verdict: "+string(v.Verdict))
	}
	return strings.Join(parts, " | ")
}

// indent lays a paragraph under its heading, wrapped to the pane rather than clipped at its edge. A
// finding's body is prose several sentences long — the part a reader is actually here to read — and
// cutting it at the terminal's width leaves the half that says least.
func (f *findingsState) indent(text string, width int) []string {
	const pad = "    "
	out := []string{}
	for para := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, Wrap(pad, markdown(para), width)...)
	}
	return out
}

func (f *findingsState) rank(s finding.Severity) int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return len(severityRank)
}
