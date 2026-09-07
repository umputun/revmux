package ui

import (
	"regexp"
	"strings"
)

// inline markdown a model writes into a line of prose: paths and flags arrive in backticks, emphasis in
// asterisks, and left raw the reader sees the punctuation instead of the effect.
var (
	mdCode = regexp.MustCompile("`([^`\n]+)`")
	mdBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
)

// ANSI rather than lipgloss: these are inline spans inside a line clip() later renders through
// lipgloss, and a nested lipgloss render ends in a full reset that clears the enclosing style. Each
// sequence therefore closes with the narrow "back to default" code rather than a reset.
const (
	ansiBoldOn  = "\x1b[1m"
	ansiBoldOff = "\x1b[22m"
	ansiCodeOn  = "\x1b[36m"
	ansiCodeOff = "\x1b[39m"
	ansiHeadOn  = "\x1b[1m\x1b[36m"
	ansiHeadOff = "\x1b[39m\x1b[22m"

	ansiUnderlineOn  = "\x1b[4m"
	ansiUnderlineOff = "\x1b[24m"
)

// heading renders a markdown heading the way the report writes it — "## Major", "### title" — bold and
// accented, with the hashes kept: a reader with the report open beside the pane should see the same.
func heading(level int, text string) string {
	return ansiHeadOn + strings.Repeat("#", level) + " " + markdownWithin(text, ansiHeadOn) + ansiHeadOff
}

// markdown renders the inline markdown in one line of model prose. Block constructs are deliberately
// not handled: every caller left here holds a single line. A document goes to mdRenderer instead.
func markdown(s string) string { return inline{codeOn: ansiCodeOn, codeOff: ansiCodeOff}.render(s) }

// markdownWithin renders inline markdown inside an enclosing style, re-opening that style after each
// span it closes. Without the re-open a span inside a heading turns the heading off from that point on:
// a code span closes with "back to default foreground" and emphasis with "bold off", the same two
// attributes a heading opens with.
func markdownWithin(s, reopen string) string {
	return inline{codeOn: ansiCodeOn, codeOff: ansiCodeOff, reopen: reopen}.render(s)
}

// inline is one rendering of the inline markdown in a line: what a code span opens and closes with,
// and what re-opens an enclosing style after each span closes it. The combined pane builds its own
// with underlined code spans, since its text is already painted one color.
type inline struct{ codeOn, codeOff, reopen string }

func (r inline) render(s string) string {
	s = mdBold.ReplaceAllString(s, ansiBoldOn+"$1"+ansiBoldOff+r.reopen)
	return mdCode.ReplaceAllString(s, r.codeOn+"$1"+r.codeOff+r.reopen)
}
