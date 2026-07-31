package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View assembles the frame: the status table on top, the tab bar under it, one focused detail pane
// filling the rest.
func (m Model) View() string {
	return m.statusTable() + "\n" + m.tabBar() + "\n" + m.rule() + "\n" + strings.Join(m.detailPane(), "\n")
}

// detailPane is the focused tab's window into its own log, scrolled by the reader. It is always
// exactly the pane's height, padded out, so the frame keeps its shape as the log grows.
func (m Model) detailPane() []string {
	lines, height := m.paneLines(), m.paneHeight()
	if len(lines) > height {
		end := min(max(len(lines)-m.view.scroll, height), len(lines))
		lines = lines[end-height : end]
	}

	out := m.clipAll(lines)
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// paneLines is what the focused tab shows: the compact combined log on tab 0, the findings browser
// on the tab the report opens, one agent's full scrollback on the rest.
func (m Model) paneLines() []string {
	if m.view.mode == modeInputs {
		return m.inputLines()
	}
	return m.reviewPaneLines(m.view.tab)
}

// reviewPaneLines is one review pane laid out whole, and it runs the document renderer's layout pass
// around it.
//
// The pass belongs here rather than in the browser, which is the only review pane that renders
// documents at all: the log and agent panes render none, and a pass they never run is a pass that
// never sweeps what the browser left. An oversized report would then stay resident for as long as the
// reader stayed on a log.
func (m Model) reviewPaneLines(tab int) []string {
	m.md.beginFrame()
	defer m.md.endFrame()
	switch tab {
	case 0:
		return m.combinedLines()
	case m.findingsTab():
		return m.findingsPane()
	}
	return m.agentLinesAt(tab)
}

// paneHeight is what the chrome leaves for the pane: one row per agent plus the fixed lines counted by
// chromeLines — the header, the rule under it, the column heading, the rule closing the table, the tab
// bar and the rule under that.
func (m Model) paneHeight() int { return max(1, m.view.height()-len(m.agents)-chromeLines) }

// maxScroll is how far back the focused pane can be scrolled, zero when it all fits.
func (m Model) maxScroll() int { return max(0, len(m.paneLines())-m.paneHeight()) }

func (m Model) reviewMaxScroll(tab int) int {
	return max(0, len(m.reviewPaneLines(tab))-m.paneHeight())
}

// tabBar names every pane, the focused one accented and marked. The browser is keyed f rather than by
// its number: it appears only once the report has arrived, so a fixed digit would name nothing for
// most of the run.
func (m Model) tabBar() string {
	names := m.tabNames()
	if len(names) == 0 {
		return ""
	}
	if m.view.mode == modeInputs {
		return m.inputTabBar()
	}
	return m.compactTabBar(names)
}

func (m Model) compactTabBar(names []string) string {
	// clipping alone is not enough: it cuts the right-hand tabs off mid-word, so a reader cannot tell
	// how many panes exist or what is hiding past the edge. There is no horizontal scroll on this line.
	//
	// **Collapse the fewest names that fit, and at each count keep the padding if it still fits.**
	// That ordering is what makes the padding go before the first name, since a padded bar one name
	// down is tried only after the tight bar with every name has failed — and it lets the padding come
	// back once a further name has been spent, which is cheaper than it looks: two spaces per tab buys
	// less than a word. Dropping every name the moment the bar is one column over throws away
	// information nothing asked for.
	//
	// Names go from the left because panes fill left to right as a run goes on, so the right-hand end
	// carries the recent work and the focused tab. Counting starts at tab two rather than tab one:
	// "all" is four columns and it is the view a reader falls back to from anywhere, so spending it
	// buys almost nothing and costs the one name most worth keeping.
	for n := range names {
		for _, tight := range []bool{false, true} {
			if bar := m.tabRow(names, n, tight); lipgloss.Width(bar) <= m.view.width() {
				return bar
			}
		}
	}
	// every name is a token, the padding is gone, and it still does not fit; clipping is the backstop
	return m.clip(m.tabRow(names, len(names), true))
}

// inputTabBar windows the potentially 128-document input list around the focused document. Rendering
// every token cannot fit once the list outgrows the terminal, even after every name collapses, and the
// generic clip backstop would then hide a focused document near the right edge.
func (m Model) inputTabBar() string {
	if m.view.tab < 0 || m.view.tab >= len(m.cfg.Inputs) {
		return ""
	}
	left, right := m.view.tab, m.view.tab
	for {
		expanded := false
		if left > 0 {
			if row := m.inputTabRow(left-1, right); lipgloss.Width(row) <= m.view.width() {
				left--
				expanded = true
			}
		}
		if right+1 < len(m.cfg.Inputs) {
			if row := m.inputTabRow(left, right+1); lipgloss.Width(row) <= m.view.width() {
				right++
				expanded = true
			}
		}
		if !expanded {
			break
		}
	}
	return m.clip(m.inputTabRow(left, right))
}

func (m Model) inputTabRow(left, right int) string {
	names := make([]string, 0, right-left+2)
	for i := left; i <= right; i++ {
		names = append(names, m.tabToken(i)+" "+m.visibleMetadata(m.cfg.Inputs[i].Label))
	}
	names = append(names, "i back")

	window := m
	window.view.tab -= left
	if row := window.tabRow(names, 0, false); lipgloss.Width(row) <= m.view.width() {
		return row
	}
	return window.tabRow(names, 0, true)
}

func (m Model) tabNames() []string {
	if m.view.mode == modeInputs {
		names := make([]string, 0, len(m.cfg.Inputs)+1)
		for i, doc := range m.cfg.Inputs {
			names = append(names, m.tabToken(i)+" "+m.visibleMetadata(doc.Label))
		}
		return append(names, "i back")
	}

	// numbered from one, because that is what the keys are: nobody reaches for 0 first, and the
	// combined view being the leftmost tab makes it tab one
	names := make([]string, 0, len(m.agents)+2)
	names = append(names, m.tabToken(0)+" all")
	for i, a := range m.agents {
		names = append(names, m.tabToken(i+1)+" "+a.spec.Name)
	}
	if m.findings != nil {
		names = append(names, "f findings")
	}
	if len(m.cfg.Inputs) > 0 {
		names = append(names, "i inputs")
	}
	return names
}

// tabRow renders the bar with collapse tabs reduced to their leading token — a digit for the first
// nine panes and a letter after that — taken from tab two rightward, never from tab one.
//
// The focused tab always keeps its name, whatever its position: its content is what fills the pane
// below, so a bare token there names nothing a reader can see. tight drops the padding around the
// separator and the padding inside every cell with it.
func (m Model) tabRow(names []string, collapse int, tight bool) string {
	// padded, the marker and the blank are the same width, so a name sits in the same column whichever
	// tab is focused. The tight row trades that alignment away for the columns it saves: there the
	// marker is one cell against no padding at all, so moving focus shifts everything to its right.
	mark, pad, sep := "▸ ", "  ", "  │"
	if tight {
		mark, pad, sep = "▸", "", "│"
	}
	labels := make([]string, 0, len(names))
	for i, n := range names {
		if i == m.view.tab {
			labels = append(labels, m.style.tabOn.Render(mark+n))
			continue
		}
		// counted from tab two: index zero is "all", four columns and the view a reader falls back to
		// from anywhere, so it is never the name that gets spent
		if i > 0 && i <= collapse {
			n, _, _ = strings.Cut(n, " ")
		}
		labels = append(labels, m.style.tabOff.Render(pad+n))
	}
	return strings.Join(labels, m.style.muted.Render(sep))
}

// tabLetters carry on where the digits stop. **Every letter already bound to something is missing
// from this list on purpose** — f opens the findings browser, h and l switch panes, j and k scroll, g
// goes to the top and q quits. Those keys are matched before the token lookup is ever reached, so a
// tab assigned one of them would simply be unreachable, and only on a run with enough panes to get
// that far.
const tabLetters = "abcdemnoprstuvwxyz"

// tabToken is the single character that selects a pane: 1-9, then the letters above.
//
// One character, always. A two-digit token costs a column in every tab on the bar and reads as two
// numbers beside a name that may itself end in one. Panes past the tokens have none — they are still
// reachable with tab and the arrows, and a run with that many panes has bigger problems.
func (m Model) tabToken(idx int) string {
	switch {
	case idx < 9:
		return strconv.Itoa(idx + 1)
	case idx-9 < len(tabLetters):
		return string(tabLetters[idx-9])
	}
	return " "
}

// tabIndex is tabToken read back: the pane a keystroke selects, or -1 for a key that names none.
func (m Model) tabIndex(key string) int {
	if len(key) != 1 {
		return -1
	}
	if c := key[0]; c >= '1' && c <= '9' {
		return int(c - '1')
	}
	if i := strings.IndexByte(tabLetters, key[0]); i >= 0 {
		return i + 9
	}
	return -1
}

// clip cuts a line to the pane width. lipgloss does the measuring because a line carries color
// sequences and slicing runes would cut one in half.
func (m Model) clip(s string) string {
	return lipgloss.NewStyle().MaxWidth(m.view.width()).Render(s)
}

func (m Model) clipAll(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, m.clip(l))
	}
	return out
}
