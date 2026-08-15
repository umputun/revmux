package main

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/ui"
)

// progressTimeFormat keeps each line short: a run is minutes long, so the date adds nothing.
const progressTimeFormat = "15:04:05"

// minCols is the narrowest terminal worth clipping to; anything less and a line is all prefix.
const minCols = 40

// fallbackCols is what an undetectable terminal gets — a redirected stderr, a pipe, a CI log.
const fallbackCols = 120

// progress is the plain event renderer, used with --no-tui and whenever the tty cannot be opened. It
// writes to stderr, never to stdout. It takes the resolved roster for the same reason the TUI does: the
// color is on the agent's spec, so both renderers show one agent in one color.
type progress struct {
	w       io.Writer
	roster  []prompt.AgentSpec
	beats   map[string]time.Time // when each agent last put anything in the log
	pending map[string]string    // each agent's newest tool call, held for its next heartbeat
}

// due reports whether this agent has been quiet long enough that a tool call is worth a line. It only
// asks — the clock is reset by whatever line actually gets printed, so prose counts as life too. No
// lock: run drains the channel on one goroutine, which is the only caller.
func (pr *progress) due(agent string, at time.Time) bool {
	last, ok := pr.beats[agent]
	return !ok || at.Sub(last) >= ui.ProgressInterval
}

// beat records that this agent just printed something, whatever kind of line it was.
func (pr *progress) beat(agent string, at time.Time) {
	if pr.beats == nil {
		pr.beats = map[string]time.Time{}
	}
	pr.beats[agent] = at
}

// note remembers the newest tool call, which is what the next heartbeat reports.
func (pr *progress) note(agent, text string) {
	if text == "" {
		return
	}
	if pr.pending == nil {
		pr.pending = map[string]string{}
	}
	pr.pending[agent] = text
}

// takeNote hands over the remembered tool call and forgets it.
func (pr *progress) takeNote(agent string) string {
	text := pr.pending[agent]
	delete(pr.pending, agent)
	return text
}

// run drains the event channel until the pipeline closes it.
func (pr *progress) run(events <-chan pipeline.Event) {
	for ev := range events {
		if line := pr.line(ev); line != "" {
			_, _ = fmt.Fprintln(pr.w, line)
		}
	}
}

// finish closes the log with what the run came to. The pipeline emits no completion event, so without
// this the last line on stderr is whichever agent finished last. It reports the outcome, never the
// findings. err is any failure making the run's own record unsound — the pipeline's, and the archive's —
// and either prints nothing, since a summary saying the review completed above an error saying it did
// not is the reading this exists to prevent. A report that reached the archive and failed only on its
// way to stdout is not one of those and keeps its summary: the round is intact, and the error beneath
// says only that delivery was not.
func (pr *progress) finish(rep finding.Report, err error) {
	if err != nil {
		return
	}
	at := rep.Stats.FinishedAt.Format(progressTimeFormat)
	for _, what := range []string{"── complete ──", pr.sourceLine(rep), pr.findingLine(rep)} {
		for _, line := range ui.Wrap(at+" "+pr.prefix(""), what, pr.cols()) {
			_, _ = fmt.Fprintln(pr.w, line)
		}
	}
}

// sourceLine is the run's duration and how many sources reported. A degraded run names what is missing:
// a partial review that reads like a complete one is the worst failure this tool has, and the log is
// where someone watching it live would look.
func (pr *progress) sourceLine(rep finding.Report) string {
	out := fmt.Sprintf("%s, sources %d/%d", time.Duration(rep.Stats.DurationMS)*time.Millisecond,
		rep.Sources.Reported, rep.Sources.Expected)
	names := rep.Sources.DegradedNames()
	if len(names) == 0 {
		return out + ", degraded none"
	}
	return out + ", DEGRADED: " + strings.Join(names, ", ")
}

// findingLine counts the findings by severity, worst first, in the same words the report uses. A
// severity outside the three constants is counted under its own name rather than dropped, so the parts
// always sum to the total: only the claude path has a schema constraining that vocabulary.
func (pr *progress) findingLine(rep finding.Report) string {
	if len(rep.Findings) == 0 {
		return "no findings"
	}
	counts := map[finding.Severity]int{}
	for _, f := range rep.Findings {
		counts[f.Severity]++
	}
	parts := []string{}
	for _, sev := range []finding.Severity{finding.Critical, finding.Major, finding.Minor} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
			delete(counts, sev)
		}
	}
	for _, sev := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[sev], cmp.Or(string(sev), "unlabeled")))
	}
	return fmt.Sprintf("%d findings: %s", len(rep.Findings), strings.Join(parts, ", "))
}

// line renders one event. Every EventKind needs a case here and in the TUI, or it is invisible in
// one of the two renderers.
func (pr *progress) line(ev pipeline.Event) string {
	var what string
	switch ev.Kind {
	case pipeline.EventStage:
		// the TUI bands this line, which is what lets it drop the label. There is no band on stderr, so
		// a bare stage name would read as another agent's text — the rule is separated here instead
		what = "── " + ev.Stage + " ──"
	case pipeline.EventAgentStarted:
		what = "started"
		if ev.Text != "" {
			what += " [" + ev.Text + "]"
		}
	case pipeline.EventDropped:
		// printed rather than folded into the stage line: synthesis is the pipeline's largest filter and
		// this is the only place a caller learns a finding left the run without reaching the report
		what = ev.Text
	case pipeline.EventAgentActivity, pipeline.EventAgentState:
		what = ev.Text
	case pipeline.EventAgentProgress:
		// throttled rather than printed or dropped: this renderer has no status row and no elapsed
		// counter, so printing every call buries the reasoning and dropping them all looks dead
		pr.note(ev.Agent, ev.Text)
		if !pr.due(ev.Agent, ev.At) {
			return ""
		}
		what = pr.takeNote(ev.Agent)
	case pipeline.EventAgentDone:
		what = "done"
		if ev.Text != "" {
			what += ", " + ev.Text
		}
	case pipeline.EventAgentRetried:
		what = "retrying: " + ev.Text
	case pipeline.EventAgentDegraded:
		what = "degraded: " + ev.Text
	case pipeline.EventFindings:
		// dropped: the done event a moment later carries the same count, and printing both puts the
		// number on two consecutive lines
		return ""
	case pipeline.EventRateLimit:
		what = "rate limited: " + ev.Text
	default:
		return ""
	}
	if what == "" {
		return ""
	}

	// **any** line counts as life, not just a heartbeat: an agent narrating steadily is visibly alive
	// already, and charging it a tool-call line every interval buries what it is saying
	pr.beat(ev.Agent, ev.At)
	return strings.Join(ui.Wrap(ev.At.Format(progressTimeFormat)+" "+pr.prefix(ev.Agent), what, pr.cols()), "\n")
}

// cols is the terminal width, COLUMNS first so a caller can pin it and a test can be deterministic.
// A width that cannot be detected is not an error: stderr is redirected as often as not, and a pipe
// has no width to ask for.
func (pr *progress) cols() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && n >= minCols {
		return n
	}
	if f, ok := pr.w.(*os.File); ok {
		if n, _, err := term.GetSize(int(f.Fd())); err == nil && n >= minCols {
			return n
		}
	}
	return fallbackCols
}

// prefix is the agent column: the name padded to the widest in the roster and colored. Padding is
// measured on the plain name and applied before painting, since a color sequence has no display width.
func (pr *progress) prefix(agent string) string {
	width := pr.nameWidth()
	if agent == "" {
		if width == 0 {
			return "" // no roster means no column to line up with, so nothing to indent past
		}
		return strings.Repeat(" ", width+2)
	}
	return pr.paint(agent) + strings.Repeat(" ", max(0, width-lipgloss.Width(agent))) + "  "
}

// minNameWidth is the floor the column holds whatever the roster is called, so the derived
// `verify <group>` rows fit beside a short one — the focused roster is `bugs` and `codex`, five cells.
// It covers `verify root` and a group named after a short directory; a longer one runs past it.
const minNameWidth = len("verify root")

// nameWidth is the widest name in the roster, floored so derived names fit. Measured in display cells
// rather than runes, the way the TUI measures it, so the two renderers cannot pad one agent differently.
// A verify group named after a long directory still runs past it, which beats clipping the name.
func (pr *progress) nameWidth() int {
	width := 0
	for _, spec := range pr.roster {
		width = max(width, lipgloss.Width(spec.Name))
	}
	if width == 0 {
		return 0
	}
	return max(width, minNameWidth)
}

// paint colors the agent's name from its own roster entry. An agent the roster does not name — a
// stage, a verify group — takes its color from prompt.DerivedSpec, the same place the TUI takes it,
// so one agent is one color whichever renderer a reviewer is watching. Neither picks one itself.
func (pr *progress) paint(agent string) string {
	for _, spec := range pr.roster {
		if spec.Name == agent {
			return spec.Paint(agent)
		}
	}
	return prompt.DerivedSpec(agent).Paint(agent)
}
