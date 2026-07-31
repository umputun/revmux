package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExpandTabs(t *testing.T) {
	tests := []struct {
		name string
		line string
		stop int
		want string
	}{
		{"no tab is returned untouched", "plain prose", mdTabStop, "plain prose"},
		{"a leading tab fills the first stop", "\ta", mdTabStop, "    a"},
		{"a tab runs to the next stop, not a fixed width", "ab\tc", mdTabStop, "ab  c"},
		{"a tab on a stop moves a whole one", "abcd\te", mdTabStop, "abcd    e"},
		{"consecutive tabs each take their own stop", "a\t\tb", mdTabStop, "a       b"},
		{"the terminal stop is eight", "ab\tc", termTabStop, "ab      c"},
		{"a verbatim row reproduces what the terminal draws", "make\ttarget", termTabStop, "make    target"},
		{"a wide rune counts the columns it draws", "日本\tx", mdTabStop, "日本    x"},
		{"an empty line has nothing to expand", "", mdTabStop, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, expandTabs(tt.line, tt.stop))
		})
	}

	t.Run("a grapheme cluster is one measurement, not one per component", func(t *testing.T) {
		// a ZWJ family is two columns; counted component by component it measures as several and the
		// stop after it lands in the wrong place
		assert.Equal(t, "👨‍👩‍👧‍👦  after", expandTabs("👨‍👩‍👧‍👦\tafter", mdTabStop))
	})

	t.Run("a line without a tab is returned rather than rebuilt", func(t *testing.T) {
		// every line of every document comes through here and almost none of them carry a tab
		line := strings.Repeat("prose ", 20)
		var sink string
		assert.Zero(t, testing.AllocsPerRun(100, func() { sink = expandTabs(line, mdTabStop) }))
		assert.Equal(t, line, sink)
	})
}
