package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

// synthesizer owns the synthesis stage: one model call merging every source's findings into one set,
// splitting open questions and pre-existing issues out first.
type synthesizer struct {
	cfg  Config
	emit func(Event)
	save func(name string, data []byte)

	stage *prompt.Stage // resolved by run, and read back by the caller to record the runner that ran
}

// synthesized is the wire shape of one merged finding: a Finding plus the ids of the inputs it came
// from. Go derives sources and lenses from those ids, so no schema exposes sources — a field the model
// can fill is a field it will fill.
type synthesized struct {
	finding.Finding
	MergedIDs []string `json:"merged_ids"`
}

// run composes the synthesis prompt, runs the stage and returns the three merged lists. The report it
// returns carries findings and the stage's own token count, never source status or timings — those
// belong to the run and are held by the caller.
func (s *synthesizer) run(ctx context.Context, sources []sourceResult) (finding.Report, error) {
	// resolved through the profile, not the set: a profile may override which binary merges the
	// findings, and the caller reads the resolution back to record it
	stage, err := s.cfg.Profile.Stage(s.cfg.Set, stageSynthesis)
	if err != nil {
		return finding.Report{}, fmt.Errorf("resolve synthesis stage: %w", err)
	}
	s.stage = stage

	text, err := stage.Compose(prompt.ComposeOpts{Vars: s.vars(sources), History: s.cfg.History})
	if err != nil {
		return finding.Report{}, fmt.Errorf("compose synthesis prompt: %w", err)
	}
	s.save(path.Join(task.StagePromptDir, stageSynthesis+".md"),
		[]byte(archivedPrompt(stage.Executor, text, finding.SynthesisSchema())))

	// a stage announces itself and closes itself, exactly as a finder does. Without the pair its row
	// appears from nowhere on its first tool call and then never leaves "running", so it reads as
	// still going while verify is already underway — two stages apparently in flight at once.
	s.emit(Event{Kind: EventAgentStarted, Agent: stageSynthesis,
		Text: "merging " + strconv.Itoa(len(s.all(sources))) + " findings"})

	res, err := s.dispatch(ctx, stage, text)
	if err != nil {
		return finding.Report{}, err
	}

	rep, dropped, err := s.parse(res.StructuredOutput, s.inputs(sources))
	if err != nil {
		return finding.Report{}, err
	}
	rep.Stats.Tokens = res.Tokens
	s.reportDropped(dropped)
	s.emit(Event{Kind: EventFindings, Agent: stageSynthesis, Findings: rep.Findings})
	s.emit(Event{Kind: EventAgentDone, Agent: stageSynthesis,
		Text: strconv.Itoa(len(rep.Findings)) + " findings"})
	return rep, nil
}

// all is every finding the finders reported, for the line that announces what synthesis is merging.
func (s *synthesizer) all(sources []sourceResult) []finding.Finding {
	var out []finding.Finding
	for _, src := range sources {
		out = append(out, src.findings...)
	}
	return out
}

// dispatch runs the stage, retrying once when the first attempt did not deliver: this is a single call
// standing between every finder's completed work and the report. A second failure fails the run, since
// an unmerged report nobody asked for is worse than a loud error.
func (s *synthesizer) dispatch(ctx context.Context, stage *prompt.Stage, text string) (executor.Result, error) {
	spec := RunnerSpec{Executor: stage.Executor, Model: stage.Model, Effort: stage.Effort}
	req := executor.Request{
		Prompt: text, Model: stage.Model, Effort: stage.Effort, Schema: finding.SynthesisSchema(),
	}

	// a failed attempt still spent what it spent, so its tokens ride on the attempt that delivers —
	// the same accounting the find stage does across its own retry
	var fault error
	var tokens int
	for n := range maxAttempts {
		res, err := s.cfg.NewRunner(spec).Run(ctx, req, newSink(stageSynthesis, s.emit, nil))
		tokens += res.Tokens
		if fault = s.fault(res, err); fault == nil {
			res.Tokens = tokens
			return res, nil
		}
		if ctx.Err() != nil || n == maxAttempts-1 {
			break
		}
		s.emit(Event{Kind: EventAgentRetried, Agent: stageSynthesis, Text: fault.Error()})
	}
	return executor.Result{}, fmt.Errorf("synthesis stage: %w", fault)
}

// fault judges one attempt. Anything that left the stage without structured output is what a retry would
// be attempting to survive; a stall or a rate limit that nonetheless carried output is not, since that
// payload only exists once the terminal result event has been read. Output present settles it before the
// exit code is consulted at all — a watchdog kill reaps by signal, so a complete attempt exits -1.
func (s *synthesizer) fault(res executor.Result, err error) error {
	switch {
	case err != nil:
		return err
	case len(res.StructuredOutput) > 0:
		return nil
	case res.ExitCode != 0:
		return fmt.Errorf("synthesis exited %d", res.ExitCode)
	}
	return errors.New("synthesis returned no structured output")
}

// vars adds the two stage variables to the run's own. FINDINGS is what the model merges and SOURCES
// is the roster as data: the pipeline knows which process emitted what, and letting the model infer
// the source count from the findings themselves is how a single agent's two lenses become two votes.
func (s *synthesizer) vars(sources []sourceResult) prompt.Vars {
	out := prompt.Vars{}
	maps.Copy(out, s.cfg.Vars)
	out["FINDINGS"] = s.findingsBlock(sources)
	out["SOURCES"] = s.sourcesBlock(sources)
	return out
}

func (s *synthesizer) findingsBlock(sources []sourceResult) string {
	all := []finding.Finding{}
	for _, src := range sources {
		if src.ok() {
			all = append(all, src.findings...)
		}
	}
	if len(all) == 0 {
		return "No source reported a finding."
	}
	b, _ := json.MarshalIndent(all, "", "  ") // Finding is plain data, so encoding cannot fail
	return string(b)
}

// sourcesBlock states what actually ran. A degraded source has to be loud here as well as in the JSON
// and the markdown banner, since the prompt tells the model to take this roster as fact rather than infer
// the source count from the findings.
func (s *synthesizer) sourcesBlock(sources []sourceResult) string {
	reported, degraded := 0, []string{}
	lines := make([]string, 0, len(sources))
	for _, src := range sources {
		entry := "- " + src.spec.Name + " (lenses: " + strings.Join(src.spec.Lenses, ", ") + ")"
		if !src.ok() {
			degraded = append(degraded, src.spec.Name)
			lines = append(lines, entry+" DEGRADED, reported nothing")
			continue
		}
		reported++
		lines = append(lines, entry+" reported "+s.emitted(src.findings))
	}

	head := strconv.Itoa(len(sources)) + " sources ran, " + strconv.Itoa(reported) + " reported."
	if len(degraded) > 0 {
		head += " This run is DEGRADED: " + strings.Join(degraded, ", ") + " reported nothing."
	}
	return head + "\n" + strings.Join(lines, "\n")
}

func (s *synthesizer) emitted(list []finding.Finding) string {
	if len(list) == 0 {
		return "no findings"
	}
	ids := make([]string, 0, len(list))
	for _, f := range list {
		ids = append(ids, f.ID)
	}
	return strconv.Itoa(len(list)) + " findings: " + strings.Join(ids, ", ")
}

// inputs keys the pre-synthesis findings by id. Those ids are unique because find stamps them
// <agent>-<n>, which is what lets attribution survive a merge across agents.
func (s *synthesizer) inputs(sources []sourceResult) map[string]finding.Finding {
	out := map[string]finding.Finding{}
	for _, src := range sources {
		if !src.ok() {
			continue
		}
		for _, f := range src.findings {
			out[f.ID] = f
		}
	}
	return out
}

func (s *synthesizer) parse(raw json.RawMessage,
	inputs map[string]finding.Finding) (rep finding.Report, dropped []finding.Finding, err error) {
	if len(raw) == 0 {
		return finding.Report{}, nil, errors.New("synthesis returned no structured output")
	}

	var out struct {
		Findings      []synthesized `json:"findings"`
		OpenQuestions []synthesized `json:"open_questions"`
		PreExisting   []synthesized `json:"pre_existing"`
	}
	if decErr := json.Unmarshal(raw, &out); decErr != nil {
		return finding.Report{}, nil, fmt.Errorf("decode synthesis output: %w", decErr)
	}
	// the merged list is what the whole find stage funnels into, so its key must be present rather than
	// merely decodable: an object of another shape leaves all three lists nil without error, and the run
	// would report no findings and exit 0 having discarded every source's work
	if !answered(raw, keyFindings) {
		return finding.Report{}, nil, errors.New("synthesis returned no findings object")
	}

	// one claimed set across all three lists: the schema binds each input to at most one output, and the
	// lists are three halves of one merge — an input reported as both a finding and a pre-existing issue
	// is the same contract violation as one reported twice in either. An input claimed by none is not an
	// error here; reportDropped announces it
	claimed := map[string]bool{}
	findings, err := s.attribute(out.Findings, inputs, claimed)
	if err != nil {
		return finding.Report{}, nil, err
	}
	rep.Findings = findings
	if rep.OpenQuestions, err = s.attribute(out.OpenQuestions, inputs, claimed); err != nil {
		return finding.Report{}, nil, err
	}
	if rep.PreExisting, err = s.attribute(out.PreExisting, inputs, claimed); err != nil {
		return finding.Report{}, nil, err
	}
	return rep, s.unclaimed(inputs, claimed), nil
}

// unclaimed is every input finding no output took: what synthesis dropped rather than merged. attribute
// errors on a second claim, so claimed is exact. It is returned rather than emitted so parse stays pure.
func (s *synthesizer) unclaimed(inputs map[string]finding.Finding, claimed map[string]bool) []finding.Finding {
	out := make([]finding.Finding, 0, len(inputs))
	for id, f := range inputs {
		if !claimed[id] {
			out = append(out, f)
		}
	}
	// ordered by id: the map is not, and an archived event that reorders between two runs of the same
	// round is a diff that reads as a change
	slices.SortFunc(out, func(a, b finding.Finding) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// reportDropped announces what synthesis removed rather than merged. The shipped prompt drops nothing, so
// under it a non-empty list is the model departing from instructions; an overridden synthesis.md may still
// drop by design. Either way the findings never reach verify, and the event carries them so events.jsonl
// answers which ones on its own.
func (s *synthesizer) reportDropped(dropped []finding.Finding) {
	if len(dropped) == 0 {
		return
	}
	gating := 0
	for _, f := range dropped {
		if f.Severity == finding.Critical || f.Severity == finding.Major {
			gating++
		}
	}
	text := strconv.Itoa(len(dropped)) + " findings dropped"
	if gating > 0 {
		text += ", " + strconv.Itoa(gating) + " of them critical or major"
	}
	s.emit(Event{Kind: EventDropped, Agent: stageSynthesis, Text: text, Findings: dropped})
}

// attribute derives sources and lenses from the merged input ids, discarding whatever the model put in
// either field. A merged id that is not an input is a hard error: dropping it quietly produces a finding
// with fewer sources than it earned. So is a second output claiming an input already spoken for — one
// finder's work became two report entries, possibly contradicting each other.
//
// One output holding an id twice is not a reuse: the input still became exactly one entry, so each
// output tracks its own ids and skips its repeats. Rejecting a reuse is also what makes the output ids
// unique, which verify depends on, since it keys its verdicts by id.
func (s *synthesizer) attribute(list []synthesized, inputs map[string]finding.Finding,
	claimed map[string]bool) ([]finding.Finding, error) {
	out := make([]finding.Finding, 0, len(list))
	for _, item := range list {
		if len(item.MergedIDs) == 0 {
			return nil, fmt.Errorf("synthesized finding %q merged no input ids", item.Title)
		}

		f := item.Finding
		f.ID, f.Sources, f.Lenses = item.MergedIDs[0], nil, nil
		mine := map[string]bool{}
		for _, id := range item.MergedIDs {
			in, ok := inputs[id]
			if !ok {
				return nil, fmt.Errorf("synthesis merged unknown finding id %q", id)
			}
			if mine[id] {
				continue // the same output listing one input twice merges it once, exactly as union already would
			}
			if claimed[id] {
				return nil, fmt.Errorf("synthesis merged finding id %q into more than one output", id)
			}
			claimed[id], mine[id] = true, true
			f.Sources = s.union(f.Sources, in.Sources)
			f.Lenses = s.union(f.Lenses, in.Lenses)
		}
		out = append(out, f)
	}
	return out, nil
}

// union appends what into does not already hold. A source is a process, so one agent carrying two
// lenses collapses to a single name here rather than counting twice.
func (s *synthesizer) union(into, add []string) []string {
	for _, v := range add {
		if !slices.Contains(into, v) {
			into = append(into, v)
		}
	}
	return into
}
