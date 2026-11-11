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

// Wrap lays one entry across as many rows as its text needs, continuing under the text column. The
// first row carries head; the rest are indented to head's width. It is the one line-breaker for every
// caller on the inline path, and exported for app/revmux/progress.go, which lives in package main.
// Document panes do not come here — mdRenderer wraps those itself.
func Wrap(head, text string, width int) []string {
	// the narrow branch clips rather than passing the row through: every row this returns has to fit
	// the width, and the plain renderer writes them straight to stderr with no clip of its own
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
