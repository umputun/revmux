package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jessevdk/go-flags"

	"github.com/umputun/revmux/app/archive"
	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
	"github.com/umputun/revmux/app/ui"
)

var revision = "unknown"

// executorCodex is the one roster executor that is not claude, which is also the default.
const executorCodex = "codex"

// runOpts is what run needs from its surroundings. Every one of them is injected so the whole entry
// point is drivable from a test: no real terminal, no real clock, no writes to the process streams.
type runOpts struct {
	opts      options
	clock     executor.Clock
	stdout    io.Writer
	stderr    io.Writer
	openTTY   func() (*os.File, error)
	newRunner func(pipeline.RunnerSpec) pipeline.Runner
	snapshot  func(reviewContext) []ui.InputDocument
}

// configuredReview is everything one review resolved before it starts. Keeping the context beside the
// pipeline configuration lets the renderer snapshot the caller's inputs without putting presentation
// data into app/pipeline.
type configuredReview struct {
	pipeline pipeline.Config
	archive  *archive.Archive
	context  reviewContext
}

func main() {
	o, err := parseArgs(os.Args[1:])
	if err != nil {
		var fe *flags.Error
		if errors.As(err, &fe) && fe.Type == flags.ErrHelp {
			fmt.Fprintln(os.Stderr, fe.Message)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	os.Exit(run(runOpts{
		opts: o, clock: executor.NewClock(),
		stdout: os.Stdout, stderr: os.Stderr, openTTY: openTTY,
	}))
}

func run(o runOpts) int {
	switch {
	case o.opts.Version:
		if err := printVersion(o.stdout, revision); err != nil {
			return o.fail(err)
		}
		return 0
	// --init is the flag spelling of `revmux init` and shares its implementation: two materializations of
	// one tree would drift, and the one that drifted would be the one nobody re-read
	case o.opts.Init || o.opts.showInit:
		if err := o.writeInitPaths(); err != nil {
			return o.fail(err)
		}
		return 0
	case o.opts.DumpDefaults != "":
		if err := o.opts.dumpDefaults(o.stderr); err != nil {
			return o.fail(err)
		}
		return 0
	case o.opts.showConfig:
		if err := o.writeCatalog(); err != nil {
			return o.fail(err)
		}
		return 0
	case o.opts.showNew:
		if err := o.writeTaskPaths(); err != nil {
			return o.fail(err)
		}
		return 0
	case o.opts.showStats:
		if err := o.writeStats(); err != nil {
			return o.fail(err)
		}
		return 0
	case o.opts.showCleanup:
		if err := o.writeCleanup(); err != nil {
			return o.fail(err)
		}
		return 0
	}

	review, err := o.pipelineConfig()
	if err != nil {
		return o.fail(err)
	}
	defer review.archive.Close() // every artifact is already on disk, only the directory handles are left

	// the pipeline runs under a signal-canceled context so an interrupt tears the agent process groups
	// down: children are started with Setsid, so the terminal never signals them and dying without
	// canceling would leave every model CLI and everything it spawned running unsupervised
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep, err := o.review(ctx, review)
	if err != nil {
		return o.fail(err)
	}
	// the report goes to stdout only here, after review returned and with it the bubbletea program:
	// writing it while the TUI still owns the terminal interleaves it with the final frame
	if err := o.write(rep); err != nil {
		return o.fail(err)
	}
	return rep.ExitCode()
}

// writeCatalog prints the resolved configuration as JSON, one of the carve-outs in "stdout belongs to
// the report" — no pipeline, archive or TUI exists yet. The prompt tree is loaded directly rather than
// through promptSet: a caller discovering which profiles exist is the one whose --profile does not
// resolve.
func (o runOpts) writeCatalog() error {
	set, err := prompt.Load(o.opts.promptOpts())
	if err != nil {
		return fmt.Errorf("load prompts: %w", err)
	}
	return o.writeJSON(o.opts.catalog(set), "catalog")
}

// writeJSON puts one subcommand's payload on stdout, indented, since a human reads these occasionally.
// The indentation is set in one place so two payloads cannot come back shaped differently, and the name
// the failure carries is the only thing the callers differ in.
func (o runOpts) writeJSON(payload any, what string) error {
	enc := json.NewEncoder(o.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("write %s: %w", what, err)
	}
	return nil
}

// pipelineConfig resolves everything the pipeline needs, plus the archive package main writes its own
// artifacts through. The roster is resolved exactly once, here: the archive manifest and both
// renderers take that same slice rather than re-deriving it.
func (o runOpts) pipelineConfig() (configuredReview, error) {
	if o.opts.Task == "" {
		return configuredReview{}, errors.New("--task is required")
	}

	rc, err := o.opts.resolveContext()
	if err != nil {
		return configuredReview{}, err
	}
	set, err := o.opts.promptSet()
	if err != nil {
		return configuredReview{}, err
	}
	profile, err := set.Profile(o.opts.Profile)
	if err != nil {
		return configuredReview{}, fmt.Errorf("resolve profile: %w", err)
	}
	roster, err := profile.Roster(o.opts.Lenses, set.LensNames())
	if err != nil {
		return configuredReview{}, fmt.Errorf("resolve roster: %w", err)
	}

	// resolved before this round is claimed, so the round being written is never in its own inventory
	history, err := archive.History(rc.TaskDir)
	if err != nil {
		return configuredReview{}, fmt.Errorf("read prior rounds: %w", err)
	}
	// the tasks root and the task name rather than the joined path resolveContext already checked: a
	// path string handed over here is one archive.New would have to reopen by name, which is the
	// check-then-open window its os.Root chain exists to remove
	arc, err := archive.New(task.Round{TasksDir: o.opts.TasksDir, Task: o.opts.Task, Run: o.opts.Run})
	if err != nil {
		return configuredReview{}, fmt.Errorf("open run archive: %w", err)
	}

	cfg := pipeline.Config{
		NewRunner: o.runnerFactory(rc),
		Archive:   arc,
		Clock:     o.clock,
		Set:       set, Profile: profile, Roster: roster, Vars: rc.vars(), History: history,
		Task: o.opts.Task, Run: o.opts.Run, ScopePath: rc.Scope,
		NoSynthesis: o.opts.NoSynthesis, NoVerify: o.opts.NoVerify,
		StaggerDelay: o.opts.StaggerDelay, MaxParallel: o.opts.MaxParallel,
		VerifyGroups: o.opts.VerifyGroups, VerifyGroupBy: o.opts.VerifyGroupBy,
	}
	return configuredReview{pipeline: cfg, archive: arc, context: rc}, nil
}

// review runs the pipeline with a renderer subscribed to its events, writes the run's own artifacts,
// hands the finished report to that renderer, and returns once the renderer has let go of the terminal.
//
// The archive lands between the pipeline finishing and the report reaching the renderer, because finish
// blocks until the reader closes the browser: archiving afterwards leaves a run killed there holding
// every pipeline-owned artifact and none of package main's, and a round with no findings.json is
// invisible to archive.History. --min-confidence is applied before the renderer sees the report, since
// the browser is a rendering path like stdout is.
func (o runOpts) review(ctx context.Context, review configuredReview) (finding.Report, error) {
	cfg, arc := review.pipeline, review.archive
	// the snapshot is the first artifact written into the round, so a signal that arrived between the
	// claim and here must stop before it: writing one burns a round nothing has reviewed
	if err := ctx.Err(); err != nil {
		return finding.Report{}, fmt.Errorf("review canceled: %w", err)
	}
	if err := o.materializeProfile(arc, review.context); err != nil {
		return finding.Report{}, err
	}
	p := pipeline.New(cfg)
	r := o.render(renderConfig{
		roster: cfg.Roster, events: p.Events(), context: review.context,
		task: cfg.Task, run: cfg.Run, profile: cfg.Profile.Name,
	})

	rep, err := p.Run(ctx)
	rep = rep.Above(o.opts.MinConfidence)

	// a failed run has no report to archive, and its own error is the one worth returning
	var arcErr error
	if err == nil {
		arcErr = o.archiveRun(arc, cfg, rep)
	}
	r.finish(rep, err, arcErr)

	switch {
	case err != nil:
		return finding.Report{}, fmt.Errorf("review failed: %w", err)
	case arcErr != nil:
		return finding.Report{}, arcErr
	}
	return rep, nil
}

// renderer is the active event subscriber, held so the finished report can be handed to it and the
// run can wait for it to release the terminal. Exactly one of the two fields is set: prog under the
// TUI, which browses the report, and plain under the stderr renderer, which summarizes it.
type renderer struct {
	prog  *tea.Program
	plain *progress
	done  chan struct{}
}

// renderConfig keeps every TUI-only input together. The plain renderer uses only roster and events;
// the remaining fields are read only after a tty opens.
type renderConfig struct {
	roster  []prompt.AgentSpec
	events  <-chan pipeline.Event
	context reviewContext
	task    string
	run     string
	profile string
}

// render subscribes the active renderer to the run's events — the TUI when the tty opens, the plain
// stderr renderer otherwise. Exactly one of them reads the channel: a Go channel distributes rather
// than broadcasts, so a second reader would take an arbitrary half of the events.
func (o runOpts) render(cfg renderConfig) *renderer {
	r := &renderer{done: make(chan struct{})}

	tty := o.tty()
	if tty == nil {
		r.plain = &progress{w: o.stderr, roster: cfg.roster}
		go func() {
			defer close(r.done)
			r.plain.run(cfg.events)
		}()
		return r
	}

	// the tty goes in as Output as well as to the program: the palette is profiled against the surface
	// the frame lands on, and profiling stdout instead loses every color whenever the report is piped
	documents := o.inputDocuments(cfg.context)
	m := ui.New(ui.ModelConfig{
		Roster: cfg.roster, Events: cfg.events, Output: tty, AutoExit: o.opts.AutoExit,
		Task: cfg.task, Run: cfg.run, Profile: cfg.profile, Inputs: documents,
	})
	r.prog = tea.NewProgram(m, tea.WithInput(tty), tea.WithOutput(tty), tea.WithAltScreen())
	go func() {
		defer close(r.done)
		// the tty is deliberately not closed here. bubbletea starts its initial size check as a bare
		// goroutine that reads the descriptor (tea.go handleResize -> checkResize), and that one is in
		// neither p.handlers nor p.finished, so Wait cannot join it: closing after Wait still raced the
		// read whenever Run returned fast, which is the failed-run path. The opener owns the file — the
		// process exits with it, and the pty in tests is closed by its own cleanup.
		// Wait stays, since it is what guarantees the terminal is restored before the run reports.
		defer r.prog.Wait()
		if _, err := r.prog.Run(); err != nil {
			_, _ = fmt.Fprintf(o.stderr, "warning: terminal ui: %v\n", err)
		}
	}()
	return r
}

func (o runOpts) inputDocuments(rc reviewContext) []ui.InputDocument {
	if o.snapshot != nil {
		return o.snapshot(rc)
	}
	return (&inputSnapshotter{limits: inputLimits{
		fileBytes: inputFileLimit, totalBytes: inputTotalLimit,
		contexts: inputCountLimit, entries: inputEntryLimit,
	}}).load(rc)
}

// finish hands the report to the findings browser and waits for the reader to close it. A failed run has
// nothing to browse, so the program is asked to quit instead. The wait keeps the report off stdout until
// the terminal is free. The stderr renderer is summarized after that wait, since its own goroutine
// writes the event lines. arcErr gates only the stderr summary — the browser still opens, because the
// findings are sound and are all that survives a round nothing can be read back from.
func (r *renderer) finish(rep finding.Report, err, arcErr error) {
	switch {
	case r.prog == nil:
	case err != nil:
		r.prog.Quit()
	default:
		r.prog.Send(ui.CompletedMsg{Report: rep})
	}
	<-r.done
	if r.plain != nil {
		r.plain.finish(rep, cmp.Or(err, arcErr))
	}
}

// tty opens the terminal the TUI renders to, nil when there is none to render on. The gate is the tty
// opening, never stdout being a terminal — that is false whenever the report is redirected, which is
// one of the most common invocations.
func (o runOpts) tty() *os.File {
	if o.opts.NoTUI || o.openTTY == nil {
		return nil
	}
	f, err := o.openTTY()
	if err != nil {
		return nil
	}
	return f
}

// runnerFactory builds the per-spec executor factory. A caller-supplied one wins, so a test drives
// the whole slice without spawning a model CLI. Both executors are built once and shared: each holds
// immutable configuration only, and the roster runs them concurrently.
func (o runOpts) runnerFactory(rc reviewContext) func(pipeline.RunnerSpec) pipeline.Runner {
	if o.newRunner != nil {
		return o.newRunner
	}
	runner, eo := executor.NewRunner(), o.opts.executorOpts(rc, o.clock)
	claude, codex := executor.NewClaude(runner, eo), executor.NewCodex(runner, eo)
	return func(spec pipeline.RunnerSpec) pipeline.Runner {
		if spec.Executor == executorCodex {
			return codex
		}
		return claude
	}
}

// write puts the report on stdout, as JSON unless a human asked for the rendered form. JSON is the
// default because the caller is a program; the two human-facing renderings are the TUI and report.md.
func (o runOpts) write(rep finding.Report) error {
	render := rep.JSON
	if o.opts.Markdown {
		render = rep.Markdown
	}
	if err := render(o.stdout); err != nil {
		return fmt.Errorf("report to stdout: %w", err)
	}
	return nil
}

func (o runOpts) fail(err error) int {
	_, _ = fmt.Fprintf(o.stderr, "error: %v\n", err)
	return 2
}

// openTTY opens the controlling terminal for both input and output. The TUI is gated on this
// succeeding, never on stdout being a terminal, which is false whenever the report is redirected.
func openTTY() (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open tty: %w", err)
	}
	return f, nil
}

func printVersion(w io.Writer, rev string) error {
	if _, err := fmt.Fprintf(w, "revmux %s\n", rev); err != nil {
		return fmt.Errorf("write version: %w", err)
	}
	return nil
}
