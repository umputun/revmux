package ui

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// combinedLimit bounds the compact log the same way scrollbackLimit bounds a pane.
const combinedLimit = 2000

// combinedEntry is one compact line. It takes a struct rather than adjacent strings because agent and
// text are both in scope where it is pushed, which is where a transposition would go unnoticed.
type combinedEntry struct {
	agent string
	text  string
	at    time.Time
}

// combinedState is the interleaved compact log behind tab 0.
type combinedState struct {
	entries []combinedEntry
}

func (c *combinedState) push(e combinedEntry) {
	c.entries = append(c.entries, e)
	if len(c.entries) > combinedLimit {
		c.entries = c.entries[len(c.entries)-combinedLimit:]
	}
}

// combinedLines renders the compact log in arrival order, each line prefixed with its agent and
// painted, prefix and text alike, in the agent's own color: with several agents interleaving, a
// block in one color is what lets a reader follow one of them without reading names. A process the
// roster does not name — a stage, a verify group — is painted the same way in its derived color.
func (m Model) combinedLines() []string {
	if len(m.combined.entries) == 0 {
		return []string{"waiting for the first agent..."}
	}
	out := make([]string, 0, len(m.combined.entries))
	for _, e := range m.combined.entries {
		head := m.style.muted.Render(e.at.Format(timeFormat)) + " " + m.prefix(e.agent)
		if e.agent == "" {
			// a stage change is the coarsest thing that happens in a run and the one line worth finding
			// while scrolling, so it is banded rather than left to read as another agent line
			out = append(out, head+m.style.stage.Render(" "+e.text+" "))
			continue
		}
		out = append(out, m.textRows(head, e.agent, e.text)...)
	}
	return out
}

// textRows lays one entry out under its head with the text in the agent's color on every row. The
// color is opened and closed per row rather than once around the text: the wrapper leaves a sequence
// open across the rows it produces, and a continuation row at the top of a scrolled pane reaches the
// screen alone. A code span is underlined rather than colored: the span color is cyan, which is also
// the first roster color in every shipped profile, so a colored span vanishes into exactly the agent
// that writes the most of them. A row that ends inside a span closes the underline and the next row
// re-opens it. Leading whitespace stays ahead of the paint so the wrapper still measures it on the
// plain text.
func (m Model) textRows(head, agent, text string) []string {
	seq := m.textColor(agent)
	if seq == "" {
		return Wrap(head, markdown(text), m.view.width())
	}
	body := strings.TrimLeftFunc(text, unicode.IsSpace)
	lead := text[:len(text)-len(body)]
	painted := inline{codeOn: ansiUnderlineOn, codeOff: ansiUnderlineOff}.render(body)
	rows := Wrap(head, lead+seq+painted+ansiCodeOff, m.view.width())
	indent := strings.Repeat(" ", lipgloss.Width(head))
	open := seq
	for i, r := range rows {
		if i > 0 {
			r = indent + open + strings.TrimPrefix(r, indent)
		}
		open = seq
		if strings.LastIndex(r, ansiUnderlineOn) > strings.LastIndex(r, ansiUnderlineOff) {
			open, r = seq+ansiUnderlineOn, r+ansiUnderlineOff
		}
		if !strings.HasSuffix(r, ansiCodeOff) {
			r += ansiCodeOff
		}
		rows[i] = r
	}
	return rows
}

// textColor is the sequence an agent's log text is painted in, the same one its name is painted in,
// derived or rostered alike. It is empty on a surface that reports no color, where painting is left
// to the name alone rather than spread over every row; the empty return for a name with no state at
// all is a guard, since every entry pushed to the log carries the name of a state that exists.
func (m Model) textColor(agent string) string {
	if m.style.profile == termenv.Ascii {
		return ""
	}
	if a := m.find(agent); a != nil {
		return a.spec.SGR()
	}
	return ""
}

// prefix is the agent column: the name padded to the widest in the roster and colored. Padding happens
// before painting and is measured on the plain name, since a color sequence has no display width.
func (m Model) prefix(agent string) string {
	if agent == "" {
		return strings.Repeat(" ", m.nameWidth()+2)
	}
	pad := strings.Repeat(" ", max(0, m.nameWidth()-lipgloss.Width(agent)))
	return m.paint(agent) + pad + "  "
}

// paint colors an agent's name from its own resolved spec, which is the same value the plain renderer
// reads, so one agent is one color in both.
func (m Model) paint(agent string) string {
	if a := m.find(agent); a != nil {
		return a.spec.Paint(agent)
	}
	return agent
}
