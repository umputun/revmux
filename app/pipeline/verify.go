package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

// thinGroup is the size below which a directory does not earn its own verifier and is merged with
// the other thin ones. One finding is not worth a process of its own.
const thinGroup = 2

// labelDirs caps how many directory names a merged group spells out before the rest are counted,
// since the label becomes a filename under prompts/stages/.
const labelDirs = 3

// labelUnsafe is everything a group label may not carry. Separators collapse to a dash rather than
// being dropped, so app/executor and appexecutor cannot label the same.
var labelUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// groupBySource is the Config.VerifyGroupBy value that buckets by the agent that raised a finding. The
// flag's vocabulary is spelled a second time in package main's option tag, which cannot name a constant.
const groupBySource = "source"

// verifier owns the verify stage: one agent per group, each seeing only its own findings. The
// stagger is handed in rather than constructed, since the gate latched open during find.
type verifier struct {
	cfg     Config
	emit    func(Event)
	save    func(name string, data []byte)
	stagger *stagger

	stage  *prompt.Stage // resolved by compose, before any group goroutine reads it
	tokens atomic.Int64
}

// verifyGroup is what one verifier sees: the keys it covers, their findings and the prompt composed from
// them. The composed text rides along because the archive stores it per group, under a filename built
// from the same label.
type verifyGroup struct {
	dirs     []string // the group's keys: directories, or agent names under --verify-group-by=source
	findings []finding.Finding
	text     string
	name     string // the resolved, collision-free label; assigned once by compose
	shown    string // the short, collision-free name a reader sees
}

// verdict is the wire shape of one entry the model returns. The correction fields apply to a
// refined verdict only, and a zero one leaves the original value alone.
type verdict struct {
	ID         string           `json:"id"`
	Verdict    finding.Verdict  `json:"verdict"`
	Line       int              `json:"line"`
	EndLine    int              `json:"end_line"`
	Severity   finding.Severity `json:"severity"`
	Confidence int              `json:"confidence"`
	Title      string           `json:"title"`
	Body       string           `json:"body"`
	Fix        string           `json:"fix"`
}

// run verifies every finding and routes it by the verdict it came back with. Rejected findings are
// dropped; immaterial and pre-existing move to their own lists, which is why the stage returns a
// whole report rather than a slice.
func (v *verifier) run(ctx context.Context, rep finding.Report) (finding.Report, error) {
	groups := v.groupByDir(rep.Findings)
	if len(groups) == 0 {
		return rep, nil
	}

	if err := v.compose(groups); err != nil {
		return finding.Report{}, err
	}

	judged := make([][]finding.Finding, len(groups))
	var wg sync.WaitGroup
	for i, g := range groups {
		wg.Go(func() { judged[i] = v.judge(ctx, g, i+1) })
	}
	wg.Wait()

	rep.Findings = nil
	for _, list := range judged {
		for _, f := range list {
			switch f.Verdict {
			case finding.Immaterial:
				rep.Immaterial = append(rep.Immaterial, f)
			case finding.PreExisting:
				rep.PreExisting = append(rep.PreExisting, f)
			default:
				rep.Findings = append(rep.Findings, f)
			}
		}
	}
	rep.Stats.Tokens += int(v.tokens.Load())
	return rep, nil
}

// compose builds every group's prompt before any of them is dispatched. A prompt tree that does not
// compose is a config error every group would hit identically, so it fails the run rather than degrading
// each group in turn.
func (v *verifier) compose(groups []verifyGroup) error {
	// resolved through the profile, not the set: a profile may override which binary verifies
	stage, err := v.cfg.Profile.Stage(v.cfg.Set, stageVerify)
	if err != nil {
		return fmt.Errorf("resolve verify stage: %w", err)
	}
	v.stage = stage
	v.name(groups)

	for i := range groups {
		text, err := stage.Compose(prompt.ComposeOpts{Vars: v.vars(groups[i]), History: v.cfg.History})
		if err != nil {
			return fmt.Errorf("compose verify prompt for %s: %w", groups[i].name, err)
		}
		groups[i].text = text
		// one file per group: a single verify.md would lose every prompt but the last
		v.save(v.promptName(groups[i]), []byte(archivedPrompt(stage.Executor, text, finding.VerifySchema())))
	}
	return nil
}

// name resolves each group's label, suffixing any that collide. Two distinct directories slug to one
// label — app/executor and app-executor both give app-executor — and would share one archived prompt.
func (v *verifier) name(groups []verifyGroup) {
	taken := make(map[string]bool, len(groups))
	for i := range groups {
		name := v.freeName(groups[i].label(), taken)
		groups[i].name = name
		taken[name] = true
	}

	// the displayed name stays short: a status row and a tab have one column each, where the archived
	// label is a filename an auditor reads at leisure
	shown := make(map[string]bool, len(groups))
	for i := range groups {
		groups[i].shown = v.freeName(groups[i].shortLabel(), shown)
		shown[groups[i].shown] = true
	}
}

// freeName returns the first unused name for a label. The generated candidate is checked against
// taken as well, not just the label: a directory literally named foo-bar-2 would otherwise claim the
// name built for the second foo-bar, and one archived prompt would overwrite the other.
func (v *verifier) freeName(label string, taken map[string]bool) string {
	name := label
	for n := 2; taken[name]; n++ {
		name = label + "-" + strconv.Itoa(n)
	}
	return name
}

// promptName is where one group's composed prompt goes. The name is already a filename-safe slug, so
// a group covering app/executor lands in a file rather than a stray nested directory.
func (v *verifier) promptName(g verifyGroup) string {
	return path.Join(task.StagePromptDir, stageVerify+"-"+g.name+".md")
}

// judge runs one group and falls back to leaving its findings unverified when the verifier does not
// deliver. index is never zero: the leader slot belongs to the find stage.
func (v *verifier) judge(ctx context.Context, g verifyGroup, index int) []finding.Finding {
	if err := v.stagger.acquire(ctx, index); err != nil {
		return v.unverifiedGroup(g, err)
	}
	defer v.stagger.release()

	out, err := v.runOne(ctx, g)
	if err != nil {
		return v.unverifiedGroup(g, err)
	}
	return out
}

// runOne runs one group's already-composed prompt and applies the verdicts it returned.
func (v *verifier) runOne(ctx context.Context, g verifyGroup) ([]finding.Finding, error) {
	agent := g.agent()
	v.emit(Event{Kind: EventAgentStarted, Agent: agent, Text: strings.Join(g.dirs, ", ")})

	spec := RunnerSpec{Executor: v.stage.Executor, Model: v.stage.Model, Effort: v.stage.Effort}
	res, err := v.cfg.NewRunner(spec).Run(ctx, executor.Request{
		Prompt: g.text, Model: v.stage.Model, Effort: v.stage.Effort, Schema: finding.VerifySchema(),
	}, newSink(agent, v.emit, nil))
	// counted before the error is judged: the run total is what was billed, not what was useful
	v.tokens.Add(int64(res.Tokens))
	if err != nil {
		return nil, fmt.Errorf("verify %s: %w", g.name, err)
	}

	out, err := v.apply(g, res.StructuredOutput)
	if err != nil {
		return nil, err
	}
	v.emit(Event{Kind: EventAgentDone, Agent: agent, Text: strconv.Itoa(len(out)) + " of " +
		strconv.Itoa(len(g.findings)) + " kept" + v.adjusted(g.findings, out)})
	return out, nil
}

// apply turns the returned verdicts into findings. A finding the model said nothing about stays, marked
// unverified: silence is not a rejection. A payload carrying no verdicts key at all is different — the
// process answered something else — and degrades the group.
func (v *verifier) apply(g verifyGroup, raw json.RawMessage) ([]finding.Finding, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("verify %s returned no structured output", g.name)
	}

	var out struct {
		Verdicts []verdict `json:"verdicts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode verdicts for %s: %w", g.name, err)
	}
	if !answered(raw, keyVerdicts) {
		return nil, fmt.Errorf("verify %s returned no verdicts object", g.name)
	}

	byID := make(map[string]verdict, len(out.Verdicts))
	for _, d := range out.Verdicts {
		byID[d.ID] = d
	}

	kept := make([]finding.Finding, 0, len(g.findings))
	for _, f := range g.findings {
		d, ok := byID[f.ID]
		if !ok || !d.known() {
			f.Verdict = finding.Unverified
			kept = append(kept, f)
			continue
		}
		if d.Verdict == finding.Rejected {
			continue
		}
		kept = append(kept, d.applyTo(f))
	}
	return kept, nil
}

// rank orders the severity vocabulary so a change between two can be read as a direction. An
// unrecognized value ranks with minor, since the codex path has no schema enforcing the vocabulary.
func (v *verifier) rank(s finding.Severity) int {
	switch s {
	case finding.Critical:
		return 3
	case finding.Major:
		return 2
	default:
		return 1
	}
}

// adjusted says what verification changed about a group beyond keeping or rejecting it. Measured over one
// archived corpus the stage rejected 2 findings of the 150 that reached it while lowering 21 severities,
// so a count that did not move reads as a verifier that did nothing.
func (v *verifier) adjusted(before, after []finding.Finding) string {
	was := make(map[string]finding.Severity, len(before))
	for _, f := range before {
		was[f.ID] = f.Severity
	}

	lowered, raised, refined := 0, 0, 0
	for _, f := range after {
		// ranked rather than tested against minor at one end: critical -> major is a lowering
		if old, ok := was[f.ID]; ok {
			switch was, now := v.rank(old), v.rank(f.Severity); {
			case now < was:
				lowered++
			case now > was:
				raised++
			}
		}
		if f.Verdict == finding.Refined {
			refined++
		}
	}

	parts := make([]string, 0, 3)
	for _, p := range []struct {
		n    int
		what string
	}{{lowered, "lowered"}, {raised, "raised"}, {refined, "refined"}} {
		if p.n > 0 {
			parts = append(parts, strconv.Itoa(p.n)+" "+p.what)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

// vars adds this group's findings to the run's own. A verifier sees only its own group, so nothing
// here carries a finding from another one.
func (v *verifier) vars(g verifyGroup) prompt.Vars {
	out := prompt.Vars{}
	maps.Copy(out, v.cfg.Vars)
	out["FINDINGS"] = g.findingsBlock()
	return out
}

// groupByDir buckets findings by key, merges the thin buckets into one and caps how many groups result.
// Directory approximates code locality, so one verifier reads that area once. Source mode keeps every
// bucket instead: thin-merging is what directory locality buys, and a panelist's lone argument has to be
// judged apart from the argument answering it.
func (v *verifier) groupByDir(findings []finding.Finding) []verifyGroup {
	byKey := map[string][]finding.Finding{}
	for _, f := range findings {
		key := v.key(f)
		byKey[key] = append(byKey[key], f)
	}

	merge := v.cfg.VerifyGroupBy != groupBySource
	groups, thin := []verifyGroup{}, verifyGroup{}
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		g := verifyGroup{dirs: []string{key}, findings: byKey[key]}
		if merge && len(g.findings) < thinGroup {
			thin = thin.merge(g)
			continue
		}
		groups = append(groups, g)
	}
	if len(thin.findings) > 0 {
		groups = append(groups, thin)
	}
	return v.capped(groups)
}

// capped merges the smallest groups together until the count fits the configured limit, taking whole
// directories rather than splitting one.
func (v *verifier) capped(groups []verifyGroup) []verifyGroup {
	limit := v.cfg.VerifyGroups
	if limit <= 0 || len(groups) <= limit {
		return groups
	}

	ordered := slices.Clone(groups)
	slices.SortStableFunc(ordered, func(a, b verifyGroup) int { return len(a.findings) - len(b.findings) })

	merged := verifyGroup{}
	for _, g := range ordered[:len(ordered)-limit+1] {
		merged = merged.merge(g)
	}
	out := append([]verifyGroup{merged}, ordered[len(ordered)-limit+1:]...)
	slices.SortFunc(out, func(a, b verifyGroup) int { return strings.Compare(a.label(), b.label()) })
	return out
}

// key is the bucket one finding falls in: the agent that raised it in source mode, its directory
// otherwise. A panel's arguments are judged one panelist at a time, so one verifier never holds a case
// and the case answering it. In directory mode a file-level finding carries no line and a
// repository-root file has no directory, and both land in the same root bucket.
func (v *verifier) key(f finding.Finding) string {
	if v.cfg.VerifyGroupBy == groupBySource {
		if len(f.Sources) == 0 {
			return "."
		}
		return f.Sources[0]
	}
	if f.File == "" {
		return "."
	}
	return path.Dir(f.File)
}

// unverifiedGroup marks a group nothing judged and says so on the event channel, so a failed
// verifier is visible rather than looking like a group that confirmed everything.
func (v *verifier) unverifiedGroup(g verifyGroup, err error) []finding.Finding {
	v.emit(Event{Kind: EventAgentDegraded, Agent: g.agent(), Text: err.Error()})
	out := make([]finding.Finding, 0, len(g.findings))
	for _, f := range g.findings {
		f.Verdict = finding.Unverified
		out = append(out, f)
	}
	return out
}

// unverified is the --no-verify path: every finding is marked unverified rather than shipping with an
// empty verdict that reads like it was checked. Open questions and pre-existing issues are never verified.
func (v *verifier) unverified(rep finding.Report) finding.Report {
	for i := range rep.Findings {
		rep.Findings[i].Verdict = finding.Unverified
	}
	return rep
}

// known reports whether the model answered with a verdict from the enum. The codex path has no
// schema to enforce one, so an unrecognized word must not reach the report as if it were a judgment.
func (d verdict) known() bool {
	switch d.Verdict {
	case finding.Confirmed, finding.Refined, finding.Rejected, finding.Immaterial, finding.PreExisting:
		return true
	default:
		return false
	}
}

// knownSeverity reports whether a refined verdict's severity is from the enum, for the reason known has.
func (d verdict) knownSeverity() bool {
	switch d.Severity {
	case finding.Critical, finding.Major, finding.Minor:
		return true
	default:
		return false
	}
}

// applyTo folds one verdict into the finding it judged. Only a refined verdict carries corrections,
// and an omitted field leaves the original value in place.
func (d verdict) applyTo(f finding.Finding) finding.Finding {
	f.Verdict = d.Verdict
	if d.Verdict != finding.Refined {
		return f
	}
	if d.Line > 0 {
		f.Line = d.Line
	}
	if d.EndLine > 0 {
		f.EndLine = d.EndLine
	}
	if d.knownSeverity() {
		f.Severity = d.Severity
	}
	if d.Confidence > 0 {
		f.Confidence = d.Confidence
	}
	if d.Title != "" {
		f.Title = d.Title
	}
	if d.Body != "" {
		f.Body = d.Body
	}
	if d.Fix != "" {
		f.Fix = d.Fix
	}
	return f
}

// merge folds another group into this one. It copies rather than appending in place, since the
// findings slices come from the grouping map and must not be shared between two groups.
func (g verifyGroup) merge(o verifyGroup) verifyGroup {
	return verifyGroup{dirs: slices.Concat(g.dirs, o.dirs), findings: slices.Concat(g.findings, o.findings)}
}

// label is the group's filename-safe slug: promptName builds prompts/stages/verify-<label>.md from
// it, so a separator here would silently create a nested directory instead of a file.
func (g verifyGroup) label() string {
	if len(g.dirs) == 0 {
		return "root"
	}

	named := g.dirs
	suffix := ""
	if len(named) > labelDirs {
		named, suffix = named[:labelDirs], "+"+strconv.Itoa(len(g.dirs)-labelDirs)+"-more"
	}
	parts := make([]string, 0, len(named))
	for _, d := range named {
		parts = append(parts, g.slug(d))
	}
	return strings.Join(parts, "+") + suffix
}

func (g verifyGroup) slug(dir string) string {
	out := strings.Trim(labelUnsafe.ReplaceAllString(dir, "-"), "-.")
	if out == "" {
		return "root"
	}
	return out
}

// agent is what a reader sees: a verify group named for what it covers. The descriptive label stays on
// the archived prompt, and the group's own started event lists every directory it covers.
func (g verifyGroup) agent() string {
	return stageVerify + " " + g.shown
}

// shortLabel names the group by what it covers rather than by its position — the last element of the
// first key, since a directory's leading path is the same for every group in one review.
func (g verifyGroup) shortLabel() string {
	if len(g.dirs) == 0 {
		return "root"
	}
	parts := strings.Split(strings.Trim(g.dirs[0], "/"), "/")
	return g.slug(parts[len(parts)-1])
}

// findingsBlock is what the group's verifier is asked to judge, ids included, since a verdict names
// the finding it applies to.
func (g verifyGroup) findingsBlock() string {
	b, _ := json.MarshalIndent(g.findings, "", "  ") // Finding is plain data, so encoding cannot fail
	return string(b)
}
