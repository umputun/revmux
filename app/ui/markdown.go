package ui

import (
	"regexp"
	"strings"
)

// Inline markdown a model writes into a line of prose — a log entry, a finding's title, its location
// or its attribution. Both spans are common there: paths, identifiers and flags arrive in backticks,
// and the emphasis a model puts on the one sentence that matters arrives in asterisks. Left raw, the
// reader sees the punctuation instead of the effect.
var (
	mdCode = regexp.MustCompile("`([^`\n]+)`")
	mdBold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
)

// ANSI rather than lipgloss, deliberately. These are inline spans inside a line that clip() later
// renders through lipgloss, and a nested lipgloss render ends in a full reset that would clear the
// enclosing style — the trap .claude/rules/tui.md records. Each sequence therefore closes with the
// narrow "back to default" code rather than a reset, which is what AgentSpec.Paint does too.
const (
	ansiBoldOn  = "\x1b[1m"
	ansiBoldOff = "\x1b[22m"
	ansiCodeOn  = "\x1b[36m"
	ansiCodeOff = "\x1b[39m"
	ansiHeadOn  = "\x1b[1m\x1b[36m"
	ansiHeadOff = "\x1b[39m\x1b[22m"
)

// heading renders a markdown heading the way the report writes it — "## Major", "### title" — bold and
// accented, with the hashes kept rather than stripped. Keeping them is deliberate: the pane is showing
// a markdown document, and a reader who has the report open beside it should see the same thing.
//
// Raw ANSI for the same reason markdown is, and closing on the narrow "back to default" codes rather
// than a reset, which would clear the style of whatever clip() renders around it.
func heading(level int, text string) string {
	return ansiHeadOn + strings.Repeat("#", level) + " " + markdownWithin(text, ansiHeadOn) + ansiHeadOff
}

// markdown renders the inline markdown in one line of model prose. Block constructs are deliberately
// not handled: every caller left here holds a single line — a log event, an agent's scrollback row,
// the browser's title, location and attribution rows, and the document renderer's own per-line
// fallback. What is a document rather than a line goes to mdRenderer instead.
func markdown(s string) string { return markdownWithin(s, "") }

// markdownWithin renders inline markdown that sits inside an enclosing style, re-opening that style
// after each span it closes.
//
// **Without the re-open, a span inside a heading turns the heading off from that point on.** A heading
// is bold and accented; a code span closes with "back to default foreground" and emphasis closes with
// "bold off" — the same two attributes. So `### fix **the** retry budget` rendered its first two words
// as a heading and the rest as plain text, on the line whose whole job is to be a heading. Passing the
// enclosing sequence back in is what keeps the nesting honest, and it stays raw ANSI rather than
// lipgloss for the reason the constants above give.
func markdownWithin(s, reopen string) string {
	s = mdBold.ReplaceAllString(s, ansiBoldOn+"$1"+ansiBoldOff+reopen)
	return mdCode.ReplaceAllString(s, ansiCodeOn+"$1"+ansiCodeOff+reopen)
}
