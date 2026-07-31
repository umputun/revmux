package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The two tab stops are not interchangeable, and which one a caller wants follows from what it is
// reproducing. A terminal draws a tab as eight columns, so a verbatim row is measured against
// termTabStop. CommonMark computes block indentation on four-column stops, so a document expanded at
// eight is reparsed with a different block structure than the file has — a tab-indented nested list
// becomes an indented code block and its marker comes back on screen as literal text.
const (
	termTabStop = 8
	mdTabStop   = 4
)

// expandTabs replaces one line's tabs with the spaces its caller's tab stop draws for them. It takes
// a single line and starts at column zero: the running column is never reset on a newline, so a
// caller with a document splits it first. Shared by the verbatim input path and the document
// renderer; see the tab stops above for which stop belongs to which.
//
// It measures whole segments between tabs rather than one rune at a time. Per rune it would both cost
// a display-width parse per character on input that carries no escapes at all, and count a grapheme
// cluster once per component — a ZWJ emoji sequence is two columns and would be counted as several,
// putting the tab stop after it in the wrong place.
func expandTabs(line string, stop int) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var out strings.Builder
	out.Grow(len(line) + stop)
	column := 0
	for i, seg := range strings.Split(line, "\t") {
		if i > 0 {
			spaces := stop - column%stop
			out.WriteString(strings.Repeat(" ", spaces))
			column += spaces
		}
		out.WriteString(seg)
		column += ansi.StringWidth(seg)
	}
	return out.String()
}
