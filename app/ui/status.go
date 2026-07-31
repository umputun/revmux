package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// statusTable is one row per agent — name, state, elapsed, last activity — under the run header and
// a column heading, all of it inside a full-width rule.
func (m Model) statusTable() string {
	rows := make([]string, 0, len(m.agents)+4)
	rows = append(rows, m.clip(m.header()), m.rule(), m.clip(m.columns()))
	for _, a := range m.agents {
		rows = append(rows, m.clip(m.agentRow(a)))
	}
	rows = append(rows, m.rule())
	return strings.Join(rows, "\n")
}

// nameWidth is the widest agent name, so the columns line up whatever the roster is called.
func (m Model) nameWidth() int {
	width := len("AGENT")
	for _, a := range m.agents {
		width = max(width, lipgloss.Width(a.spec.Name))
	}
	return width
}

// columns labels the table. Muted, because it is chrome a reader stops seeing after the first glance.
func (m Model) columns() string {
	return m.style.muted.Render(fmt.Sprintf("%-*s  %-9s  %7s  %s", m.nameWidth(), "AGENT", "STATE", "TIME", "ACTIVITY"))
}

// agentRow is one agent's line. Each cell is painted separately and padded before it is painted: a
// color sequence has no display width, so padding a painted string counts the escape bytes as if they
// did and the columns drift apart by exactly the length of the sequence.
func (m Model) agentRow(a *agentState) string {
	name := a.spec.Paint(a.spec.Name + strings.Repeat(" ", m.nameWidth()-lipgloss.Width(a.spec.Name)))
	state := m.stateStyle(a.state).Render(fmt.Sprintf("%-9s", a.state))
	elapsed := m.style.muted.Render(fmt.Sprintf("%7s", a.runtime(m.now)))
	return name + "  " + state + "  " + elapsed + "  " + a.last
}

// header names the run and what it is doing. The findings count is the one number worth finding at a
// glance, so it carries the only accent on the line.
func (m Model) header() string {
	// **it degrades rather than being clipped, for the same reason the tab bar does.** statusTable clips
	// this line, and the completion notice is the rightmost thing on it — so the severity breakdown,
	// which is the longest thing on it, would push exactly the "complete, closing in 5s" off the edge at
	// the moment it matters most. Parts are given up longest-first, and what a reader needs to know
	// about where the run is survives longest.
	for _, level := range m.headerLevels() {
		if line := m.headerLine(level); lipgloss.Width(line) <= m.view.width() {
			return line
		}
	}
	// narrower than the notice itself; clipping is the backstop under everything
	return m.clip(m.headerLine(headerParts{count: countNone}))
}

// headerLevels is the degrade ladder, widest first.
//
// Order matters and is stated in .claude/rules/tui.md: the breakdown, then the quit hint, then the
// agent count, then the stage, then the total. The count outlives the stage because a reader who has
// lost the stage still learns whether anything was found, while a stage name without a count says only
// that something is happening. The hint outlives the breakdown because the breakdown degrades and the
// hint does not: the total keeps the worst severity in its color and the report restates the split at
// the end, while a header with no hint leaves a reader who wants out with nothing on screen at all.
// That is also why there is no breakdown-without-hint rung: it would only ever be reached at a width
// where the rung above it already fits.
//
// The hint rungs are built only for a live run rather than being dropped later by headerLine. A rung
// whose one distinguishing part its renderer discards is the rung below it spelled differently, so a
// finished run would degrade through a step that changes nothing and the ladder read here would not
// be the ladder that runs.
func (m Model) headerLevels() []headerParts {
	top := headerParts{identity: true, agents: true, stage: true, count: countFull}
	levels := []headerParts{top}
	if !m.done {
		levels[0].hint = true
		levels = append(levels, headerParts{identity: true, agents: true, stage: true, hint: true, count: countTotal})
	}
	return append(levels,
		headerParts{identity: true, agents: true, stage: true, count: countTotal},
		headerParts{agents: true, stage: true, count: countTotal},
		headerParts{stage: true, count: countTotal},
		headerParts{count: countTotal},
		headerParts{count: countNone},
	)
}

// how much of the findings count the header has room for.
const (
	countFull  = iota // 6 findings (0 critical, 1 major, 5 minor)
	countTotal        // 6 findings
	countNone
)

// headerParts is one level of header detail.
type headerParts struct {
	identity, agents, stage, hint bool
	count                         int
}

// headerLine builds the header at one level of detail.
func (m Model) headerLine(p headerParts) string {
	head := m.style.title.Render("revmux")
	if m.view.mode == modeInputs {
		head += m.style.muted.Render(" · inputs")
		if p.identity && m.cfg.Task != "" && m.cfg.Run != "" {
			head += m.style.muted.Render(" · " + m.visibleMetadata(m.cfg.Task) + "/" + m.visibleMetadata(m.cfg.Run))
		}
	}
	if p.agents {
		head += m.style.muted.Render(" · " + strconv.Itoa(len(m.agents)) + " agents")
	}
	if p.stage && m.stage != "" {
		head += m.style.muted.Render(" · ") + m.stage
	}
	if m.found.total > 0 && p.count != countNone {
		count := m.found.String()
		if p.count == countTotal {
			count = strconv.Itoa(m.found.total) + " findings"
		}
		head += m.style.muted.Render(" · ") + m.countStyle().Render(count)
	}
	if p.hint {
		// the only key that ends a live run. q and esc are inert until the report is in, so a reader who
		// tries them sees a frame that does not change and has nothing on screen telling him why
		head += m.style.muted.Render(" · ctrl+c to quit")
	}
	return head + m.closing()
}

// closing is what the header says once the run is over: that it is over, and how to leave. A finished
// review otherwise looks identical to a stalled one — every row reads "done" and nothing moves — and a
// reader who does not already know the key has no way to find it. While the run is still going the same
// job belongs to the header's own hint, which is droppable where this notice is not.
func (m Model) closing() string {
	if !m.done {
		return ""
	}
	if m.exitIn > 0 {
		secs := strconv.Itoa(int(m.exitIn.Round(time.Second).Seconds()))
		return m.style.muted.Render(" · ") +
			m.style.warn.Render("complete, closing in "+secs+"s") +
			m.style.muted.Render(" · any key to stay")
	}
	return m.style.muted.Render(" · ") + m.style.ok.Render("complete") + m.style.muted.Render(" · q to quit")
}
