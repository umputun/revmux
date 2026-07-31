package ui

import (
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The palette. Adaptive rather than fixed: a reviewer running a light terminal gets legible muted
// text instead of the near-invisible grey a dark-only palette produces.
var (
	colAccent = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}   // titles, the focused tab
	colMuted  = lipgloss.AdaptiveColor{Light: "247", Dark: "242"} // timestamps, rules, chrome
	colOK     = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	colWarn   = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	colErr    = lipgloss.AdaptiveColor{Light: "124", Dark: "203"}
	colBg     = lipgloss.AdaptiveColor{Light: "231", Dark: "235"} // text laid over an accent band
)

// styles are built per model rather than kept in package vars, because a lipgloss style carries the
// renderer that decides whether it emits color at all.
type styles struct {
	title  lipgloss.Style
	muted  lipgloss.Style
	tabOn  lipgloss.Style
	tabOff lipgloss.Style
	stage  lipgloss.Style
	ok     lipgloss.Style
	warn   lipgloss.Style
	bad    lipgloss.Style
	run    lipgloss.Style

	// what the surface can do, read from the same renderer the styles above are built against. The
	// document renderer is handed both rather than profiling the terminal a second time: two answers to
	// "what can this terminal do" can disagree, and then the frame and the panes inside it disagree.
	profile termenv.Profile
	dark    bool
}

// newStyles builds the palette against the surface the frame is actually written to.
//
// **The renderer has to be the tty, never the default one.** lipgloss's default renderer profiles
// termenv.DefaultOutput, which is os.Stdout — and `revmux --task pr-1 > findings.json` is one of the most
// common invocations, so stdout is a pipe while the reader sits at a terminal watching the run. Left
// on the default, every style here would render colorless in exactly that case while AgentSpec.Paint
// kept emitting raw ANSI regardless, and the frame would come out half painted.
//
// A nil writer falls back to the default renderer, which is what a test wants.
func newStyles(w io.Writer) styles {
	r := lipgloss.DefaultRenderer()
	if w != nil {
		r = lipgloss.NewRenderer(w)
	}
	return styles{
		title:  r.NewStyle().Bold(true).Foreground(colAccent),
		muted:  r.NewStyle().Foreground(colMuted),
		tabOn:  r.NewStyle().Bold(true).Foreground(colAccent),
		tabOff: r.NewStyle().Foreground(colMuted),
		stage:  r.NewStyle().Bold(true).Foreground(colBg).Background(colAccent),
		ok:     r.NewStyle().Foreground(colOK),
		warn:   r.NewStyle().Foreground(colWarn),
		bad:    r.NewStyle().Foreground(colErr),
		run:    r.NewStyle().Foreground(colAccent),

		profile: r.ColorProfile(),
		dark:    r.HasDarkBackground(),
	}
}

// countStyle colors the header's findings count by the worst severity in it. A fixed green there reads
// as a verdict on the run rather than as an accent, and a review that turned up a critical is exactly
// the run a reader must not skim past. Green is reserved for a count with nothing above minor in it.
//
// The bold is applied here rather than carried on the three palette styles, which are also the row
// states and are not bold.
func (m Model) countStyle() lipgloss.Style {
	switch {
	case m.found.critical > 0:
		return m.style.bad.Bold(true)
	case m.found.major > 0:
		return m.style.warn.Bold(true)
	}
	return m.style.ok.Bold(true)
}

// stateStyle colors a row by what its state means rather than by how it is spelled, so a degraded
// agent is findable without reading every row.
func (m Model) stateStyle(state string) lipgloss.Style {
	switch state {
	case stateDone:
		return m.style.ok
	case stateRetrying, stateLimited:
		return m.style.warn
	case stateDegraded:
		return m.style.bad
	case stateRunning:
		return m.style.run
	}
	return m.style.muted
}

// rule draws a divider the full width of the frame.
func (m Model) rule() string {
	w := m.view.width()
	if w < 1 {
		return ""
	}
	return m.style.muted.Render(strings.Repeat("─", w))
}
