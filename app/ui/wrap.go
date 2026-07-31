package ui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// minWrapCols is the narrowest text column worth wrapping into: below it a wrapped entry is more rows
// of indent than of text, and clipping reads better.
const minWrapCols = 20

// Wrap lays one entry across as many rows as its text needs, continuing under the text column rather
// than under whatever head names it. The first row carries head; the rest are indented to head's width
// so the entry reads as one thing and the columns stay scannable.
//
// **There is one of these, and there were three.** Everything on the inline path — the combined log,
// an agent's scrollback, the findings browser's title, location and attribution rows, and the plain
// `--no-tui` renderer — wraps the same way, and three separate copies of this loop had already drifted
// — two measured display width and one counted runes, so the same text broke in different places
// depending on which pane it landed in. Two implementations of one rule diverging is this project's
// most reliable defect; the fix is for there to be one. It is exported for `app/progress.go`, which
// lives in `package main`.
//
// Document panes do not come here. The input viewer and a finding's body and fix are markdown
// documents rendered by mdRenderer, which wraps them itself; this stays the line-breaker for text that
// is one line per entry.
//
// Clipping instead loses the end of exactly the lines worth reading: a narrated step, a command, a
// finding's body. A pane is where a reader went looking for detail, so it is the last place to throw
// the end of it away.
func Wrap(head, text string, width int) []string {
	// **the narrow branch clips rather than passing the row through.** Every row this returns has to
	// fit the width, whoever called it: the TUI's panes happen to run their rows through clipAll
	// afterwards, but the plain renderer writes them straight to stderr, so a bound that lives in the
	// caller is a bound one caller does not have. Below the floor a wrapped entry is more rows of
	// indent than of text, so that case clips instead of wrapping — but it still fits.
	avail := width - lipgloss.Width(head)
	if avail < minWrapCols || lipgloss.Width(text) <= avail {
		return []string{lipgloss.NewStyle().MaxWidth(width).Render(head + text)}
	}

	indent := strings.Repeat(" ", lipgloss.Width(head))
	body := strings.TrimLeftFunc(text, unicode.IsSpace)
	leading := text[:len(text)-len(body)]
	bodyWidth := avail - lipgloss.Width(leading)
	if bodyWidth < 1 {
		body, leading, bodyWidth = text, "", avail
	}

	wrapped := strings.Split(ansi.Wrap(body, bodyWidth, ""), "\n")
	out := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		for bounded := range strings.SplitSeq(ansi.Hardwrap(line, bodyWidth, true), "\n") {
			prefix := head
			if len(out) > 0 {
				prefix = indent
			}
			out = append(out, prefix+leading+bounded)
		}
	}
	return out
}
