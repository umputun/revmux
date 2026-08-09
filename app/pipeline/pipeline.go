// Package pipeline orchestrates the three review stages — find, synthesize, verify — over a roster of
// supervised model processes.
//
// It is headless: it emits typed events on a channel and never touches a terminal, a repository or a
// concrete executor. Everything that reaches the outside world arrives through Config.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

//go:generate moq -out mocks/runner.go -pkg mocks -skip-ensure -fmt goimports . Runner
//go:generate moq -out mocks/archiver.go -pkg mocks -skip-ensure -fmt goimports . archiver:ArchiverMock

// stage names, used for the stage events, the recorded timings and the stage prompt lookup.
const (
	stageFind      = "find"
	stageSynthesis = "synthesis"
	stageVerify    = "verify"
)

// the top-level key each stage's answer carries, checked by answered once the payload has decoded.
const (
	keyFindings = "findings"
	keyVerdicts = "verdicts"
)

// answered reports whether a decoded payload is the shape the stage asked for. Decoding alone does not
// say so: an object carrying some other key unmarshals leaving the list nil and no error, so a process
// that answered something else reads as one that found nothing. The gap is codex's alone — it has no
// --json-schema, and extraction takes the first decodable object anywhere in its stdout. An empty list
// is still an answer: {"findings": []} carries the key and passes.
func answered(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

// archivedPrompt is the prompt the process actually receives, which is the only version worth storing.
// Each executor appends something of its own — codex its output contract, claude its narration contract
// — so a prompt archived as composed describes a review that did not happen.
func archivedPrompt(exec, text string, schema json.RawMessage) string {
	if exec != executorCodex {
		return text + executor.ClaudeNarrationContract(schema)
	}
	return text + executor.CodexOutputContract(schema)
}

// Runner runs one supervised process. It is declared here, by the consumer, and exported only so
// package main can name it when it supplies the factory.
type Runner interface {
	Run(ctx context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error)
}

// RunnerSpec selects which binary runs a piece of work, and how. Both a roster entry and a stage
// prompt produce one, so a stage never has to fabricate a fake roster entry to pick a runner.
type RunnerSpec struct {
	Executor string
	Model    string
	Effort   string
}

// archiver is the run archive as the pipeline needs it: one writer per relative artifact path.
type archiver interface {
	Writer(name string) (io.WriteCloser, error)
}

// Config is everything the pipeline takes from its surroundings. Idle and hard timeouts are
// deliberately absent — they reach the executors through executor.Opts, built by the composition
// root, because the pipeline never constructs an executor.
type Config struct {
	NewRunner func(RunnerSpec) Runner
	Archive   archiver
	Clock     executor.Clock

	Set     *prompt.Set
	Profile *prompt.Profile
	Roster  []prompt.AgentSpec
	Vars    prompt.Vars
	History string

	Task      string
	Run       string
	ScopePath string

	NoSynthesis bool
	NoVerify    bool

	StaggerDelay  time.Duration
	MaxParallel   int
	VerifyGroups  int
	VerifyGroupBy string // "dir", the default, or "source" to key verifier groups by the agent that raised the finding
}

// Pipeline runs one review. Run is a thin stage orchestrator: the stages own their own logic so no
// single type accumulates all three plus event fan-out plus I/O.
type Pipeline struct {
	cfg     Config
	events  chan Event
	stagger *stagger

	mu  sync.Mutex
	log io.WriteCloser
	enc *json.Encoder
	err error // first archive failure, sticky: a half-written archive fails the run
}

// New builds a pipeline over cfg. The event channel is buffered and lossy by design. The stagger is
// built here and handed to each stage rather than owned by one: its gate latches open on the first
// release, so a later stage does not pay a second delay to re-prove what the first already proved.
func New(cfg Config) *Pipeline {
	return &Pipeline{
		cfg: cfg, events: make(chan Event, eventBuffer),
		stagger: newStagger(cfg.StaggerDelay, cfg.MaxParallel, cfg.Clock),
	}
}

// Events returns the event channel. It has exactly one reader — the active renderer — because a Go
// channel distributes rather than broadcasts, so a second subscriber would take an arbitrary half of
// the events. The archive is written from emit instead.
func (p *Pipeline) Events() <-chan Event { return p.events }

// Run executes the stages and returns the report. A failed archive write comes back as an error
// rather than a logged warning: a report next to a half-written archive reads as complete.
func (p *Pipeline) Run(ctx context.Context) (rep finding.Report, err error) {
	defer func() {
		p.closeEvents()
		close(p.events)
		if archiveErr := p.archiveErr(); archiveErr != nil && err == nil {
			rep, err = finding.Report{}, archiveErr
		}
	}()

	p.openEvents()
	started := p.cfg.Clock.Now()

	rep, sources, err := p.runFind(ctx)
	if err != nil {
		return finding.Report{}, err
	}
	if rep, err = p.runSynthesis(ctx, rep, sources); err != nil {
		return finding.Report{}, err
	}
	if rep, err = p.runVerify(ctx, rep); err != nil {
		return finding.Report{}, err
	}

	rep.Scope = finding.Scope{Task: p.cfg.Task, Run: p.cfg.Run, ScopePath: p.cfg.ScopePath}
	rep.Stats.StartedAt = started
	rep.Stats.FinishedAt = p.cfg.Clock.Now()
	rep.Stats.DurationMS = rep.Stats.FinishedAt.Sub(started).Milliseconds()
	return rep, nil
}

// runFind runs the find stage and appends its own timing to the report it returns. The per-source
// results travel out alongside it because synthesis needs the true roster — which process emitted which
// finding, and which degraded — as data rather than as an inference.
func (p *Pipeline) runFind(ctx context.Context) (finding.Report, []sourceResult, error) {
	p.emit(Event{Kind: EventStage, Stage: stageFind})
	at := p.cfg.Clock.Now()

	f := &finder{cfg: p.cfg, emit: p.emit, save: p.save, stagger: p.stagger}
	sources, err := f.run(ctx)
	if err != nil {
		return finding.Report{}, nil, err
	}

	// find finishing is a stronger proof than the gate's own signal: at least one process ran to
	// completion, so no later stage may wait on a leader that has already come and gone
	p.stagger.leaderStarted()

	rep := f.report(sources)
	// find records no runner: it has one per roster entry, and those are the manifest's agent rows
	rep.Stats.Stages = append(rep.Stats.Stages,
		finding.StageRun{Name: stageFind, DurationMS: p.cfg.Clock.Now().Sub(at).Milliseconds()})
	p.saveStage(task.FoundFile, rep)
	return rep, sources, nil
}

// runSynthesis merges the roster's findings into one set. --no-synthesis passes them through with
// their sources and lenses attribution intact, since find already stamped both.
func (p *Pipeline) runSynthesis(ctx context.Context, rep finding.Report, sources []sourceResult) (finding.Report, error) {
	if p.cfg.NoSynthesis {
		return rep, nil
	}

	p.emit(Event{Kind: EventStage, Stage: stageSynthesis})
	at := p.cfg.Clock.Now()

	s := &synthesizer{cfg: p.cfg, emit: p.emit, save: p.save}
	out, err := s.run(ctx, sources)
	if err != nil {
		return finding.Report{}, err
	}

	rep.Findings, rep.OpenQuestions, rep.PreExisting = out.Findings, out.OpenQuestions, out.PreExisting
	rep.Stats.Tokens += out.Stats.Tokens
	rep.Stats.Stages = append(rep.Stats.Stages, p.stageRun(stageSynthesis, s.stage, p.cfg.Clock.Now().Sub(at)))
	p.saveStage(task.SynthesizedFile, rep)
	return rep, nil
}

// runVerify checks each finding against the code and routes it by the verdict that came back.
// --no-verify marks every finding unverified rather than letting an empty verdict read as checked.
func (p *Pipeline) runVerify(ctx context.Context, rep finding.Report) (finding.Report, error) {
	v := &verifier{cfg: p.cfg, emit: p.emit, save: p.save, stagger: p.stagger}
	if p.cfg.NoVerify {
		return v.unverified(rep), nil
	}

	p.emit(Event{Kind: EventStage, Stage: stageVerify})
	at := p.cfg.Clock.Now()

	out, err := v.run(ctx, rep)
	if err != nil {
		return finding.Report{}, err
	}
	out.Stats.Stages = append(out.Stats.Stages, p.stageRun(stageVerify, v.stage, p.cfg.Clock.Now().Sub(at)))
	p.saveStage(task.VerifiedFile, out)
	return out, nil
}

// stageRun records what a stage took and which runner it resolved to. The runner is recorded because a
// profile can override it, and nothing else in a finished round says which binary produced that stage.
// A nil stage dispatched nothing and resolved no runner — verify returns early when synthesis left
// nothing to check — and the entry stays, since the stage did run and took time.
func (p *Pipeline) stageRun(name string, st *prompt.Stage, took time.Duration) finding.StageRun {
	out := finding.StageRun{Name: name, DurationMS: took.Milliseconds()}
	if st != nil {
		out.Executor, out.Model, out.Effort = st.Executor, st.Model, st.Effort
	}
	return out
}

// emit records the event in the archive first and synchronously, then offers it to the channel. That
// ordering is what makes events.jsonl complete while the display stays droppable.
func (p *Pipeline) emit(ev Event) {
	if ev.At.IsZero() {
		ev.At = p.cfg.Clock.Now()
	}
	p.archive(ev)
	select {
	case p.events <- ev:
	default:
	}
}

func (p *Pipeline) archive(ev Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enc == nil || p.err != nil {
		return
	}
	if err := p.enc.Encode(ev); err != nil {
		p.err = fmt.Errorf("archive event: %w", err)
	}
}

func (p *Pipeline) openEvents() {
	w, err := p.cfg.Archive.Writer(task.EventsFile)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.err = fmt.Errorf("open %s: %w", task.EventsFile, err)
		return
	}
	p.log, p.enc = w, json.NewEncoder(w)
}

// closeEvents folds a deferred write failure into the same sticky error: a close is where one
// surfaces, and nothing outside the pipeline holds this handle.
func (p *Pipeline) closeEvents() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log == nil {
		return
	}
	if err := p.log.Close(); err != nil && p.err == nil {
		p.err = fmt.Errorf("close %s: %w", task.EventsFile, err)
	}
	p.log, p.enc = nil, nil
}

func (p *Pipeline) archiveErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
