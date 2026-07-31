package ui

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func (m Model) inputLines() []string {
	return m.inputLinesAt(m.view.tab)
}

func (m Model) inputLinesAt(tab int) []string {
	if tab < 0 || tab >= len(m.cfg.Inputs) {
		return []string{"no such input"}
	}
	doc := m.cfg.Inputs[tab]
	out := []string{m.style.muted.Render(m.visibleMetadata(doc.Path)), ""}

	switch {
	case doc.Content == "" && doc.Notice == "":
		out = append(out, m.style.muted.Render("(empty file)"))
	case doc.Markdown:
		out = append(out, m.markdownInput(tab)...)
	default:
		out = append(out, m.verbatimInput(doc.Content)...)
	}
	if doc.Notice != "" {
		if doc.Content != "" {
			out = append(out, "")
		}
		out = append(out, Wrap("", m.style.warn.Render(m.visibleMetadata(doc.Notice)), m.view.width())...)
	}
	return out
}

func (m Model) verbatimInput(text string) []string {
	out := []string{}
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, m.verbatimLine(line)...)
	}
	return out
}

// markdownInput renders one input document through glamour, cached per tab and width. It reads the
// content off the tab rather than taking it: the tab is what names the cache entry, so a caller
// handing over the two separately can name one tab's entry and fill it with another's. Tab expansion
// is the renderer's, behind that cache and shared with the findings browser.
func (m Model) markdownInput(tab int) []string {
	return m.md.lines(mdDoc{key: mdKey("input:" + strconv.Itoa(tab)), src: m.cfg.Inputs[tab].Content, width: m.view.width()})
}

// verbatimLine hard-wraps one row of a non-Markdown input without the word wrapper's whitespace
// folding. Markdown files, fenced code included, go to the document renderer instead and never arrive
// here. Tabs are expanded first because terminal tab stops consume columns that lipgloss and ansi do
// not count, which otherwise lets a valid input row escape the frame.
func (m Model) verbatimLine(line string) []string {
	return strings.Split(ansi.Hardwrap(expandTabs(line, termTabStop), max(1, m.view.width()), true), "\n")
}

// visibleMetadata replaces every control character before caller-provided identity or filesystem
// metadata reaches the terminal. Content is validated separately by the snapshotter.
func (Model) visibleMetadata(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, text)
}
