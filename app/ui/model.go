package ui

import (
	"io"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
)

const (
	// scrollbackLimit bounds one agent's retained lines. A long review emits thousands of them and a
	// pane only ever shows a screenful.
	scrollbackLimit = 2000

	// timeFormat keeps a log line short: a run is minutes long, so the date adds nothing.
	timeFormat = "15:04:05"

	// sizes used until the first tea.WindowSizeMsg arrives, and whenever the terminal reports none.
	defaultCols = 100
	defaultRows = 30

	// chromeLines is what the frame spends on everything that is not the pane or an agent row: the
	// header, the rule under it, the column heading, the rule closing the table, the tab bar, and the
	// rule under that. Miscount it and the pane runs past the bottom of the terminal.
	chromeLines = 6
)

// ProgressInterval is how often a tool call earns a line of its own in a log.
//
// Neither extreme works. Logging every one buries the model's reasoning under pages of "Read";
// logging none leaves an agent that opens by reading twenty files looking dead for minutes. One
// heartbeat every few seconds says the run is alive without competing with the prose, which is never
// throttled.
//
// Exported because the plain renderer in package main throttles on the same cadence. Two constants
// would drift, and a reviewer switching renderers would get two different ideas of how busy a run is.
const ProgressInterval = 10 * time.Second

// agent states as the status table shows them.
const (
	stateWaiting  = "waiting"
	stateRunning  = "running"
	stateRetrying = "retrying"
	stateLimited  = "limited"
	stateDone     = "done"
	stateDegraded = "degraded"
)

// ModelConfig is what package main hands the model.
//
// Roster is the same resolved slice the plain renderer takes, and it is where agent colors come from:
// pipeline.Event names the agent and nothing else, so a color picked here would exist in one renderer
// only and the two would disagree about the same agent.
type ModelConfig struct {
	Roster []prompt.AgentSpec
	Events <-chan pipeline.Event
	Task   string
	Run    string
	Inputs []InputDocument

	// AutoExit closes the browser on its own that long after the report arrives, counting down in the
	// header, and any key cancels it. Zero waits for the reader instead, which is the default: a run
	// left unattended should still be readable when someone comes back to it.
	AutoExit time.Duration

	// Output is the surface the frame is written to, and it is what the palette is profiled against.
	// It has to be the tty rather than stdout: see newStyles. Nil is allowed and falls back to
	// lipgloss's default renderer, which is what a test wants.
	Output io.Writer
}

// InputDocument is one caller-provided input captured before the review starts. Package main owns
// filesystem access and hands immutable values over; app/ui only renders them.
type InputDocument struct {
	Label    string
	Path     string
	Content  string
	Notice   string
	Markdown bool
}

// Model is the bubbletea model: a status table over one focused detail pane.
//
// Receivers are mixed on purpose. tea.Model requires value receivers on Init, Update and View, and
// Update returns the mutated model rather than mutating in place, so those and their render helpers
// take values while the internal mutators Update calls take pointers. The state sub-structs have no
// such constraint and are pointer-only, since a value receiver there would copy scrollback on every
// render.
type Model struct {
	cfg      ModelConfig
	style    styles
	view     viewState
	agents   []*agentState
	combined *combinedState
	findings *findingsState
	md       *mdRenderer // documents rendered for the input viewer and the findings browser
	stage    string
	now      time.Time // the newest event time seen, which is what every row's elapsed is measured against
	found    tally
	done     bool          // the run is over and the report is in
	exitIn   time.Duration // what is left of the auto-exit countdown, zero when nothing is counting
}

type viewMode int

const (
	modeReview viewMode = iota
	modeInputs
)

// viewState is where the reader is looking: the focused tab, the scroll offset within it and the
// terminal size. Each mode keeps its own navigation so inspecting inputs never loses the reader's
// place in an agent log or the findings.
type viewState struct {
	navState
	cols           int
	rows           int
	mode           viewMode
	review         navState
	inputs         navState
	reviewFindings bool
}

type navState struct {
	tab    int
	scroll int
}

// agentState is one agent's status row plus its full scrollback.
type agentState struct {
	spec    prompt.AgentSpec
	state   string
	started time.Time
	updated time.Time
	beat    time.Time // when this agent last put anything in the log, heartbeat or prose
	pending string    // the newest tool call, held for the next heartbeat
	last    string
	lines   []combinedEntry
}

// eventsDone says the pipeline closed its channel, so the run is over and no further events arrive.
type eventsDone struct{}

// exitTick is one second of the auto-exit countdown.
type exitTick struct{}

// tick schedules the next second of the countdown.
func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return exitTick{} })
}

// New builds the model over the resolved roster, one row per agent in roster order.
func New(cfg ModelConfig) Model {
	style := newStyles(cfg.Output)
	m := Model{cfg: cfg, style: style, combined: &combinedState{},
		md:   newMDRenderer(style.profile, style.dark),
		view: viewState{cols: defaultCols, rows: defaultRows, inputs: navState{scroll: -1}}}
	m.agents = make([]*agentState, 0, len(cfg.Roster))
	for _, spec := range cfg.Roster {
		m.agents = append(m.agents, &agentState{spec: spec, state: stateWaiting})
	}
	return m
}

// Init starts watching the event channel.
func (m Model) Init() tea.Cmd { return m.next() }

// Update folds one message in and asks for the next event. Reading the channel happens inside a
// command, off the update loop, so a slow render never stalls the pipeline.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// only when the width actually moved. Both renderer maps are keyed by width, so a resize already
		// renders at the new width and this bounds memory rather than fixing anything — while a blanket
		// drop on every message would throw the cache away on a height-only resize, and maxScroll below
		// re-renders every document through glamour on the spot.
		if msg.Width != m.view.cols {
			m.md.reset()
		}
		m.view.cols, m.view.rows = msg.Width, msg.Height
		m.view.scroll = min(m.view.scroll, m.maxScroll())
		return m, nil
	case tea.KeyMsg:
		// any key cancels the countdown: a reader who reached for the keyboard is reading, and closing
		// the report out from under him is the one thing the countdown must not do
		m.exitIn = 0
		return m.key(msg)
	case exitTick:
		if m.exitIn <= 0 {
			return m, nil // canceled by a keypress between ticks
		}
		if m.exitIn -= time.Second; m.exitIn <= 0 {
			return m, tea.Quit
		}
		return m, m.tick()
	case pipeline.Event:
		m.apply(msg)
		return m, m.next()
	case eventsDone:
		return m, nil
	case CompletedMsg:
		m.complete(msg.Report)
		if m.exitIn > 0 {
			return m, m.tick()
		}
		return m, nil
	}
	return m, nil
}

// complete opens the findings browser over the finished report. The run being over is not a reason
// to quit: package main writes the report once the reader is done with the terminal, and the agent
// tabs stay reachable so he can check why a finding was raised.
func (m *Model) complete(rep finding.Report) {
	m.findings = newFindings(rep, m.md)
	// **the header's count is rebuilt from the report, not left as the last event's.** Two stages after
	// synthesis change the set without emitting a findings event — verify moves rejected findings into
	// Immaterial and PreExisting, and --min-confidence filters before the report is handed over — so a
	// tally fed only by events names severities the browser below it does not list.
	m.found = tally{}
	m.found.add(rep.Findings)
	m.done = true
	m.exitIn = m.cfg.AutoExit
	if m.view.mode == modeInputs {
		m.view.reviewFindings = true
		return
	}
	m.focus(m.findingsTab())
	// focus opens a pane at its newest line, which is right for a log and wrong for a report: a reader
	// starts at the worst finding, not at the tail of the last one's body
	m.view.scroll = m.maxScroll()
}

// findingsTab is the browser's tab, one past the last agent, or -1 while there is no report to
// browse. Nothing ever focuses -1, so the pane router cannot reach the browser early.
func (m Model) findingsTab() int {
	if m.findings == nil {
		return -1
	}
	return len(m.agents) + 1
}

// next reads one event in a command goroutine and delivers it as a message.
func (m Model) next() tea.Cmd {
	events := m.cfg.Events
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return eventsDone{}
		}
		return ev
	}
}

// apply folds one event into the model. An event naming an agent the roster does not carry opens a
// row for it rather than panicking, and one arriving after that agent finished is just another line:
// ordering across concurrent agents is not guaranteed.
func (m *Model) apply(ev pipeline.Event) {
	if ev.At.After(m.now) {
		m.now = ev.At
	}
	if ev.Kind == pipeline.EventStage {
		m.stage = ev.Stage
		m.combined.push(combinedEntry{at: ev.At, text: ev.Stage})
		return
	}

	a := m.agent(ev.Agent)
	if a == nil {
		return
	}
	text := a.track(ev)
	if ev.Kind == pipeline.EventFindings {
		m.count(ev)
	}
	if ev.Kind == pipeline.EventAgentProgress {
		// track has already put this in the status cell, where it belongs. It earns a line only when
		// the agent has been quiet long enough that the log would otherwise look stalled.
		a.note(ev.Text)
		if !a.due(ev.At) {
			return
		}
		text = a.takeNote()
	}
	if text == "" {
		return
	}
	a.push(combinedEntry{at: ev.At, text: text})
	m.combined.push(combinedEntry{agent: a.spec.Name, at: ev.At, text: text})
	// **any** line counts as life, not just a heartbeat. An agent that narrates steadily — codex
	// streams a reasoning headline every few seconds — is visibly alive already, so charging it a
	// tool-call line every interval on top just buries what it is saying under "exec".
	a.beat = ev.At
}

// agent returns the named agent's state, opening a row for one the roster never mentioned.
func (m *Model) agent(name string) *agentState {
	if name == "" {
		return nil
	}
	if a := m.find(name); a != nil {
		return a
	}
	// a stage or verify group is not in the roster, so its color comes from prompt.DerivedSpec — this
	// package never picks one, or the plain --no-tui renderer would color the same agent differently
	a := &agentState{spec: prompt.DerivedSpec(name), state: stateRunning}
	m.agents = append(m.agents, a)
	return a
}

// count folds a findings event into the header total. A roster agent adds to it, a stage process
// replaces it: synthesis merges what the finders already reported, so summing the two counts every
// finding twice and shows a number that is neither the raw total nor the merged one.
func (m *Model) count(ev pipeline.Event) {
	if m.rostered(ev.Agent) {
		m.found.add(ev.Findings)
		return
	}
	m.found = tally{}
	m.found.add(ev.Findings)
}

// tally is the header's findings count broken down by severity. A bare total says how much there is to
// read and nothing about whether it is worth reading now — one critical among twenty is a different
// run from twenty minor, and the header is where that difference is cheapest to show.
type tally struct {
	total, critical, major, minor int
}

func (t *tally) add(fs []finding.Finding) {
	for _, f := range fs {
		t.total++
		switch f.Severity {
		case finding.Critical:
			t.critical++
		case finding.Major:
			t.major++
		case finding.Minor:
			t.minor++
		}
	}
}

// String is the same shape the prior-round inventory uses, so a reader meets one vocabulary rather
// than two. A severity the model invented is counted in the total and named nowhere, which is what
// keeps the parts from ever exceeding it.
func (t tally) String() string {
	return strconv.Itoa(t.total) + " findings (" +
		strconv.Itoa(t.critical) + " critical, " +
		strconv.Itoa(t.major) + " major, " +
		strconv.Itoa(t.minor) + " minor)"
}

// rostered reports whether the name is a roster entry rather than a stage process. The roster is the
// authority, not the status rows, which grow to cover both.
func (m Model) rostered(name string) bool {
	for _, spec := range m.cfg.Roster {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// find looks a rostered agent up without creating one, so rendering never grows the table.
func (m Model) find(name string) *agentState {
	for _, a := range m.agents {
		if a.spec.Name == name {
			return a
		}
	}
	return nil
}

// track folds the event into the agent's row and returns the line for the log panes. An unrecognized
// kind returns empty and is dropped: a blank row would read as an agent that went quiet.
func (a *agentState) track(ev pipeline.Event) string {
	if a.started.IsZero() {
		a.started = ev.At
	}
	if ev.At.After(a.updated) {
		a.updated = ev.At
	}

	switch ev.Kind {
	case pipeline.EventAgentStarted:
		a.state, a.started = stateRunning, ev.At
		a.last = "started"
		if ev.Text != "" {
			a.last += " [" + ev.Text + "]"
		}
	case pipeline.EventAgentActivity, pipeline.EventAgentState:
		a.state, a.last = stateRunning, ev.Text
	case pipeline.EventAgentProgress:
		// the one kind that updates the row without writing a line: it is liveness, and the next
		// tool call replaces it a second later. Returning it would put "Read" in the scrollback
		// dozens of times and drown the prose the reader is actually there for.
		a.state, a.last = stateRunning, ev.Text
		return ""
	case pipeline.EventAgentDone:
		a.state, a.last = stateDone, "done"
		if ev.Text != "" {
			a.last += ", " + ev.Text
		}
	case pipeline.EventAgentRetried:
		a.state, a.last = stateRetrying, "retrying: "+ev.Text
	case pipeline.EventAgentDegraded:
		a.state, a.last = stateDegraded, "degraded: "+ev.Text
	case pipeline.EventFindings:
		// the status cell only. The done event lands a moment later carrying the same count, so
		// logging both prints the number twice in consecutive lines
		a.last = strconv.Itoa(len(ev.Findings)) + " findings emitted"
		return ""
	case pipeline.EventRateLimit:
		a.state, a.last = stateLimited, "rate limited: "+ev.Text
	default:
		return ""
	}
	return a.last
}

// due reports whether this agent has been quiet long enough that a tool call is worth a line. It only
// asks — the clock is reset by whatever line actually gets logged, so prose counts as life too.
func (a *agentState) due(at time.Time) bool {
	return a.beat.IsZero() || at.Sub(a.beat) >= ProgressInterval
}

// note remembers the newest tool call, which is what the next heartbeat reports.
// Whichever call lands last before the interval boundary is what the line says, which is the most
// recent thing the agent was doing.
func (a *agentState) note(text string) {
	if text == "" {
		return
	}
	a.pending = text
}

// takeNote hands over the remembered tool call and forgets it, so the next heartbeat reports what
// happened since rather than repeating this one.
func (a *agentState) takeNote() string {
	text := a.pending
	a.pending = ""
	return text
}

// push appends one scrollback entry, dropping the oldest once the pane's budget is spent. It keeps the
// timestamp and the text apart rather than joined, so the pane can wrap the text under its own column
// when the terminal is too narrow for it — a joined string cannot be re-split without parsing it back.
func (a *agentState) push(line combinedEntry) {
	a.lines = append(a.lines, line)
	if len(a.lines) > scrollbackLimit {
		a.lines = a.lines[len(a.lines)-scrollbackLimit:]
	}
}

// runtime is the agent's elapsed time as the status table shows it, measured between event timestamps
// rather than off a clock so the table renders the same in a test as in a terminal.
// finished reports whether this agent has reached a state it will not leave.
func (a *agentState) finished() bool { return a.state == stateDone || a.state == stateDegraded }

// runtime is how long this agent ran: from its own first event to its own last one once it has
// finished, and to the newest event time anywhere in the run while it is still going.
//
// **Both halves matter and the obvious implementation gets one of them wrong.** Measuring against the
// agent's own last event freezes a running agent's clock the moment it goes quiet — exactly when a
// reader wants to know how long it has been quiet for. Measuring against the run instead makes a
// finished agent's elapsed climb for the rest of the run, until every completed row converges on the
// run's age and a reader can no longer tell the finder that took forty seconds from the one that took
// four minutes.
//
// This still takes no clock. `.claude/rules/tui.md` forbids one here, and the reason holds: the
// elapsed a reader sees has to be the elapsed the archive recorded, and a clock read in this package
// would drift from the timestamps the pipeline stamped.
func (a *agentState) runtime(now time.Time) string {
	end := a.updated
	if !a.finished() && now.After(end) {
		end = now
	}
	if a.started.IsZero() || !end.After(a.started) {
		return "-"
	}
	d := end.Sub(a.started).Round(time.Second)
	if d == 0 {
		return "-"
	}
	return d.String()
}

func (v viewState) width() int {
	if v.cols <= 0 {
		return defaultCols
	}
	return v.cols
}

func (v viewState) height() int {
	if v.rows <= 0 {
		return defaultRows
	}
	return v.rows
}
