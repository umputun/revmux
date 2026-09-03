package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// statusTable is one row per agent — name, state, elapsed, model, last activity — under the run header
// and a column heading, all of it inside a full-width rule. MODEL is dropped first when the pane is too
// narrow, the same way the header degrades: it is resolved once for the whole table rather than per row,
// or two rows in the same run could disagree about whether the column exists.
func (m Model) statusTable() string {
	showModel := m.showModel()
	rows := make([]string, 0, len(m.agents)+4)
	rows = append(rows, m.clip(m.header()), m.rule(), m.clip(m.columns(showModel)))
	for _, a := range m.agents {
		rows = append(rows, m.clip(m.agentRow(a, showModel)))
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

// modelWidth is the widest resolved model string among the roster. A process the roster does not name —
// synthesis, verify, a verify group — carries no model here, so it does not enter the width and renders
// as a blank cell rather than widening the column for a value nothing in it will ever show.
func (m Model) modelWidth() int {
	width := 0
	for _, a := range m.agents {
		width = max(width, lipgloss.Width(a.spec.Model))
	}
	return width
}

// showModel reports whether the MODEL column fits. It fits nothing to show if no agent resolved a model
// at all — an empty column is chrome with no information in it — and it is dropped the same way the
// header degrades: measured against the column heading, since that is the fixed part of the row, while
// ACTIVITY is free-width and already clipped by the caller.
func (m Model) showModel() bool {
	if m.modelWidth() == 0 {
		return false
	}
	return lipgloss.Width(m.columns(true)) <= m.view.width()
}

// columns labels the table. Muted, because it is chrome a reader stops seeing after the first glance.
func (m Model) columns(showModel bool) string {
	if showModel {
		return m.style.muted.Render(fmt.Sprintf("%-*s  %-9s  %7s  %-*s  %s",
			m.nameWidth(), "AGENT", "STATE", "TIME", m.modelWidth(), "MODEL", "ACTIVITY"))
	}
	return m.style.muted.Render(fmt.Sprintf("%-*s  %-9s  %7s  %s", m.nameWidth(), "AGENT", "STATE", "TIME", "ACTIVITY"))
}

// agentRow is one agent's line. Each cell is painted separately and padded before it is painted: a
// color sequence has no display width, so padding a painted string counts the escape bytes as if they
// did and the columns drift apart by exactly the length of the sequence.
func (m Model) agentRow(a *agentState, showModel bool) string {
	name := a.spec.Paint(a.spec.Name + strings.Repeat(" ", m.nameWidth()-lipgloss.Width(a.spec.Name)))
	state := m.stateStyle(a.state).Render(fmt.Sprintf("%-9s", a.state))
	elapsed := m.style.muted.Render(fmt.Sprintf("%7s", a.runtime(m.now)))
	if !showModel {
		return name + "  " + state + "  " + elapsed + "  " + a.last
	}
	model := m.style.muted.Render(fmt.Sprintf("%-*s", m.modelWidth(), a.spec.Model))
	return name + "  " + state + "  " + elapsed + "  " + model + "  " + a.last
}

// header names the run and what it is doing. The findings count is the one number worth finding at a
// glance, so it carries the only accent on the line.
func (m Model) header() string {
	// degrades rather than being clipped: statusTable clips this line and the completion notice is the
	// rightmost thing on it, so the breakdown would push "complete, closing in 5s" off the edge
	for _, level := range m.headerLevels() {
		if line := m.headerLine(level); lipgloss.Width(line) <= m.view.width() {
			return line
		}
	}
	// narrower than the notice itself; clipping is the backstop under everything
	return m.clip(m.headerLine(headerParts{count: countNone}))
}

// headerLevels is the degrade ladder, widest first. The order is stated in .claude/rules/tui.md, and
// there is no breakdown-without-hint rung: it would only be reached at a width where the rung above it
// already fits. The hint rungs are built only for a live run rather than dropped later by headerLine —
// a rung whose distinguishing part its renderer discards is the rung below it spelled differently.
func (m Model) headerLevels() []headerParts {
	top := headerParts{identity: true, profile: true, stage: true, count: countFull}
	levels := []headerParts{top}
	if !m.done {
		levels[0].hint = true
		levels = append(levels, headerParts{identity: true, profile: true, stage: true, hint: true, count: countTotal})
	}
	return append(levels,
		headerParts{identity: true, profile: true, stage: true, count: countTotal},
		headerParts{profile: true, stage: true, count: countTotal},
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
	identity, profile, stage, hint bool
	count                          int
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
	if p.profile && m.cfg.Profile != "" {
		head += m.style.muted.Render(" · " + m.visibleMetadata(m.cfg.Profile))
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
// review otherwise looks identical to a stalled one — every row reads "done" and nothing moves. It sits
// outside the degrade ladder, where the live run's hint is droppable.
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
