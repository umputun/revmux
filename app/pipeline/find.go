package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

// executorCodex names the one roster executor whose output is prose rather than stream-json, so its
// verbatim tee gets a different extension.
const executorCodex = "codex"

// maxAttempts is one launch plus one retry. A second failure degrades the source and the run
// continues, because one flaky agent must not waste every other agent's work.
const maxAttempts = 2

// errNoSources is what a run with nothing reporting returns. A clean empty report would tell a
// scripted caller the code is fine.
var errNoSources = errors.New("every source degraded, no review to report")

// finder owns the find stage: every roster entry runs its own process and returns structured
// findings. The stagger is handed in rather than owned, since the verify stage runs through the same
// instance and one owned by a single stage could not be reached from another.
type finder struct {
	cfg     Config
	emit    func(Event)
	save    func(name string, data []byte)
	stagger *stagger
}

// attemptOpts is one launch of one agent. n is the attempt number, which decides where the verbatim
// tee goes: a retry writes its own file, since appending would splice its first line onto the
// stalled attempt's partial one and truncating would discard the stream most worth having.
type attemptOpts struct {
	spec   prompt.AgentSpec
	prompt string
	leader bool
	n      int
}

// sourceResult is one agent's outcome. It carries the spec because the report needs the roster entry
// alongside what the process actually did.
type sourceResult struct {
	spec     prompt.AgentSpec
	findings []finding.Finding
	stat     finding.SourceStat
	err      error
}

// ok reports whether the agent delivered. A source that did not is degraded, and degraded is the
// only thing the report and the synthesis prompt need to know about it.
func (r sourceResult) ok() bool { return r.err == nil }

func (f *finder) run(ctx context.Context) ([]sourceResult, error) {
	if len(f.cfg.Roster) == 0 {
		return nil, errors.New("roster is empty")
	}

	// results are indexed rather than appended, so the report stays in roster order however the
	// agents finish and two runs of one roster do not produce diff-noisy reports
	out := make([]sourceResult, len(f.cfg.Roster))
	var wg sync.WaitGroup
	for i, spec := range f.cfg.Roster {
		wg.Go(func() { out[i] = f.runAgent(ctx, spec, i) })
	}
	wg.Wait()

	for _, r := range out {
		if r.ok() {
			return out, nil
		}
	}
	return nil, errNoSources
}

// runAgent runs one roster entry, retrying once when the first launch fails in a way a second might
// survive, after a jittered pause so the retry does not land in the same window that killed the launch.
// A second failure degrades this source alone and the run continues.
func (f *finder) runAgent(ctx context.Context, spec prompt.AgentSpec, index int) sourceResult {
	res := sourceResult{spec: spec, stat: finding.SourceStat{
		Name: spec.Name, Lenses: spec.Lenses, Executor: spec.Executor,
		RequestedModel: spec.Model, Effort: spec.Effort,
	}}
	text, err := f.cfg.Profile.Compose(f.cfg.Set, spec, prompt.ComposeOpts{Vars: f.cfg.Vars, History: f.cfg.History})
	if err != nil {
		return f.degrade(res, err)
	}
	// archived post-substitution, exactly the bytes the process receives — the codex output contract
	// included: a reflection agent cannot judge a lens it cannot read, and a paraphrase is worse than
	// no data
	f.save(f.promptName(spec), []byte(archivedPrompt(spec.Executor, text, finding.FinderSchema())))

	if slotErr := f.stagger.acquire(ctx, index); slotErr != nil {
		return f.degrade(res, slotErr)
	}
	defer f.stagger.release()

	// started means running, and is emitted after the stagger slot is acquired for that reason:
	// announcing it on entry starts every agent's clock at once and every row reads the same elapsed
	f.emit(Event{Kind: EventAgentStarted, Agent: spec.Name, Text: strings.Join(spec.Lenses, ", ")})

	opts := attemptOpts{spec: spec, prompt: text, leader: index == 0}
	var fault error
	for opts.n = 0; opts.n < maxAttempts; opts.n++ {
		result, runErr := f.attempt(ctx, opts)
		res.stat.Tokens += result.Tokens
		if result.ActualModel != "" {
			res.stat.ActualModel = result.ActualModel
		}

		if fault = f.fault(spec, result, runErr); fault != nil {
			if ctx.Err() != nil || opts.n == maxAttempts-1 {
				break
			}
			// the wait comes before the event because app/archive counts agent_retried entries as
			// retries: a cancellation while waiting would otherwise record one that never relaunched
			if pauseErr := f.retryPause(ctx); pauseErr != nil {
				break
			}
			f.emit(Event{Kind: EventAgentRetried, Agent: spec.Name, Text: fault.Error()})
			continue
		}

		res.findings, err = f.parse(spec, result.StructuredOutput)
		if err != nil {
			return f.degrade(res, err)
		}
		f.emit(Event{Kind: EventFindings, Agent: spec.Name, Findings: res.findings})
		f.emit(Event{Kind: EventAgentDone, Agent: spec.Name, Text: strconv.Itoa(len(res.findings)) + " findings"})
		return res
	}
	return f.degrade(res, fault)
}

// retryPause waits out Config.RetryDelay before a relaunch, so whatever transient condition killed the
// launch has time to clear: a retry firing in the same millisecond band reproduces the timing that
// killed the first attempt. The jitter separates agents that failed together and would otherwise
// relaunch in lockstep into the same collision. There is no growth factor because maxAttempts allows a
// single retry, so there is only ever one interval. A non-positive delay relaunches at once, the same
// convention the stagger reads a non-positive timeout under.
func (f *finder) retryPause(ctx context.Context) error {
	d := f.cfg.RetryDelay
	if d <= 0 {
		return nil
	}
	done := make(chan struct{})
	//nolint:gosec // the jitter separates colliding relaunches, it guards nothing
	timer := f.cfg.Clock.AfterFunc(d+rand.N(d), func() { close(done) })
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		timer.Stop()
		return fmt.Errorf("wait before retry: %w", ctx.Err())
	}
}

// attempt runs one process under its own cancellable context, so a retry tears the stalled attempt's
// process group down rather than leaving it alive alongside its replacement.
func (f *finder) attempt(ctx context.Context, opts attemptOpts) (executor.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	raw, err := f.cfg.Archive.Writer(f.rawName(opts.spec, opts.n))
	if err != nil {
		return executor.Result{}, fmt.Errorf("open raw output for %s: %w", opts.spec.Name, err)
	}

	var first func()
	if opts.leader {
		first = f.stagger.leaderStarted
	}

	spec := opts.spec
	result, runErr := f.cfg.NewRunner(RunnerSpec{Executor: spec.Executor, Model: spec.Model, Effort: spec.Effort}).
		Run(ctx, executor.Request{
			Prompt: opts.prompt, Model: spec.Model, Effort: spec.Effort,
			Schema: finding.FinderSchema(), RawOutput: raw,
		}, newSink(spec.Name, f.emit, first))

	if closeErr := raw.Close(); closeErr != nil && runErr == nil {
		runErr = fmt.Errorf("close raw output for %s: %w", spec.Name, closeErr)
	}
	return result, runErr
}

// fault judges one attempt. A nil return means the process delivered; anything else is what a retry
// would survive — a stall, a rate limit, a dead process, a transport error, or a clean exit carrying
// nothing, which is the codex path's own failure mode since its output contract is prompt-driven.
//
// Three things are not faults. A payload that arrived but is not the answer: the process delivered, so
// parse rejects it and degrades this source alone. A stall or rate limit that nonetheless carried
// structured output: that payload only exists once the terminal result event has been read. And the
// exit code follows the same carve-out — a watchdog kill reaps by signal, so the code is -1, not 0.
func (f *finder) fault(spec prompt.AgentSpec, res executor.Result, err error) error {
	switch {
	case res.IdleTimedOut && len(res.StructuredOutput) == 0:
		return fmt.Errorf("agent %s stalled", spec.Name)
	case res.RateLimited && len(res.StructuredOutput) == 0:
		return fmt.Errorf("agent %s rate limited: %s", spec.Name, res.RateLimit.Status)
	case err != nil:
		return err
	case res.ExitCode != 0 && len(res.StructuredOutput) == 0:
		return fmt.Errorf("agent %s exited %d", spec.Name, res.ExitCode)
	case len(res.StructuredOutput) == 0:
		return fmt.Errorf("agent %s returned no structured output", spec.Name)
	}
	return nil
}

// parse turns one agent's structured output into findings, assigning both attribution fields in Go.
// sources is overwritten with the executing agent's name: a source is a process, and one agent naming
// itself twice is self-corroboration. id is rewritten for the same reason — four agents on one schema
// each emit "1", and synthesis derives the sources union from the ids it merged.
func (f *finder) parse(spec prompt.AgentSpec, raw json.RawMessage) ([]finding.Finding, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("agent %s returned no structured output", spec.Name)
	}

	var out struct {
		Findings []finding.Finding `json:"findings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode findings from %s: %w", spec.Name, err)
	}
	if !answered(raw, keyFindings) {
		return nil, fmt.Errorf("agent %s returned no findings object", spec.Name)
	}

	for i := range out.Findings {
		out.Findings[i].ID = spec.Name + "-" + strconv.Itoa(i+1)
		out.Findings[i].Sources = []string{spec.Name}
		out.Findings[i].Lenses = f.lenses(spec, out.Findings[i].Lenses)
	}
	return out.Findings, nil
}

// lenses keeps only lens names the agent actually carries. A model naming one it was never given is
// informational noise, and an empty result falls back to the agent's full set, which raised it by
// definition.
func (f *finder) lenses(spec prompt.AgentSpec, named []string) []string {
	out := make([]string, 0, len(named))
	for _, l := range named {
		if slices.Contains(spec.Lenses, l) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return slices.Clone(spec.Lenses)
	}
	return out
}

// rawName is where the attempt's verbatim tee goes. Per-agent streams live under agents/ so an agent
// named events cannot collide with events.jsonl, and a retry gets its own file so neither attempt is
// spliced onto or overwritten by the other.
func (f *finder) rawName(spec prompt.AgentSpec, attempt int) string {
	ext := ".jsonl"
	if spec.Executor == executorCodex {
		ext = ".log"
	}
	if attempt > 0 {
		ext = ".retry" + ext
	}
	return path.Join(task.AgentsDir, spec.Name+ext)
}

// promptName is where this agent's composed prompt goes. Agent prompts live under their own directory
// so an agent named synthesis or verify cannot overwrite a stage's prompt.
func (f *finder) promptName(spec prompt.AgentSpec) string {
	return path.Join(task.AgentPromptDir, spec.Name+".md")
}

func (f *finder) degrade(res sourceResult, err error) sourceResult {
	res.err = err
	f.emit(Event{Kind: EventAgentDegraded, Agent: res.spec.Name, Text: err.Error()})
	return res
}

// report assembles the passthrough report, which is what makes --no-synthesis work at this stage
// rather than waiting for the synthesis one.
func (f *finder) report(sources []sourceResult) finding.Report {
	rep := finding.Report{Sources: finding.SourceStatus{Expected: len(sources)}}
	for _, s := range sources {
		stat := s.stat
		stat.Raised = len(s.findings)
		stat.Degraded = !s.ok()
		rep.Sources.Agents = append(rep.Sources.Agents, stat)
		rep.Stats.Tokens += stat.Tokens
		if stat.Degraded {
			rep.Sources.DegradedSources = append(rep.Sources.DegradedSources, s.spec.Name)
			continue
		}
		rep.Sources.Reported++
		rep.Findings = append(rep.Findings, s.findings...)
	}
	return rep
}
