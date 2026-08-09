package finding

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// Report is the result of one review run, in the shape written to stdout.
type Report struct {
	Scope         Scope        `json:"scope"`
	Sources       SourceStatus `json:"sources"`
	Findings      []Finding    `json:"findings"`
	OpenQuestions []Finding    `json:"open_questions"`
	PreExisting   []Finding    `json:"pre_existing"`
	Immaterial    []Finding    `json:"immaterial"`
	Stats         Stats        `json:"stats"`
}

// Scope identifies the run: the caller-chosen task and run names, and the absolute path of
// the scope file the agents were told to read.
type Scope struct {
	Task      string `json:"task"`
	Run       string `json:"run"`
	ScopePath string `json:"scope_path"`
}

// SourceStatus is how many sources were expected, how many reported, and what each one did.
type SourceStatus struct {
	Expected        int          `json:"expected"`
	Reported        int          `json:"reported"`
	DegradedSources []string     `json:"degraded"`
	Agents          []SourceStat `json:"agents"`
}

// SourceStat is one agent's outcome. RequestedModel and ActualModel differ whenever the CLI
// silently ignored the requested model, which it does, so both are reported.
type SourceStat struct {
	Name           string   `json:"name"`
	Lenses         []string `json:"lenses"`
	Executor       string   `json:"executor"`
	RequestedModel string   `json:"requested_model"`
	ActualModel    string   `json:"actual_model"`
	Effort         string   `json:"effort"`

	// Tokens is what the agent's own CLI reported, and it is not the same quantity across executors —
	// never compare one executor's number with another's. claude sums input, output and both cache
	// counters; codex prints one number after its "tokens used" footer. In one measured corpus claude
	// agents ranged 0.11M-7.0M per round while the codex peer sat flat at 0.12-0.17M, which is mostly
	// the two definitions rather than two costs. It is honest per agent and over time for that agent.
	Tokens   int  `json:"tokens"`
	Degraded bool `json:"degraded"`
}

// Stats is the run's timing and token accounting.
type Stats struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	DurationMS int64      `json:"duration_ms"`
	Tokens     int        `json:"tokens"`
	Stages     []StageRun `json:"stages"`
}

// StageRun is one pipeline stage: how long it took and which runner it was resolved to. A profile can
// override that runner, so nothing else in a finished round says which binary synthesized it. The fields
// carry the requested triple, never the model that answered — verify fans out into one process per
// group. The find stage leaves all three empty; its runners are the per-agent rows.
type StageRun struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
	Executor   string `json:"executor,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
}

// Degraded reports whether any source failed to deliver. A degraded run that reads like a
// complete one is the worst failure mode this tool has, so both records of it count.
func (s SourceStatus) Degraded() bool {
	return len(s.DegradedNames()) > 0
}

// DegradedNames is every source that failed to deliver, from the explicit list and from the per-agent
// flags together — the two records disagree whenever one is written and the other is not, and a name in
// either is a source whose absence the reader has to be told about.
func (s SourceStatus) DegradedNames() []string {
	names := slices.Clone(s.DegradedSources)
	for _, a := range s.Agents {
		if a.Degraded && !slices.Contains(names, a.Name) {
			names = append(names, a.Name)
		}
	}
	return names
}

func (s SourceStat) model() string {
	switch {
	case s.ActualModel == "" && s.RequestedModel == "":
		return "-"
	case s.ActualModel == "":
		return s.RequestedModel
	case s.RequestedModel == "" || s.ActualModel == s.RequestedModel:
		return s.ActualModel
	}
	return fmt.Sprintf("%s (requested %s)", s.ActualModel, s.RequestedModel)
}

// Markdown renders the report for a human, findings grouped by severity.
func (r Report) Markdown(w io.Writer) error {
	out := r.headerLines()
	out = append(out, r.findingsLines()...)
	out = append(out, r.listLines("Open questions", r.OpenQuestions)...)
	out = append(out, r.listLines("Pre-existing", r.PreExisting)...)
	out = append(out, r.listLines("Immaterial", r.Immaterial)...)
	out = append(out, r.sourcesLines()...)
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if _, err := io.WriteString(w, strings.Join(out, "\n")+"\n"); err != nil {
		return fmt.Errorf("write markdown report: %w", err)
	}
	return nil
}

// JSON renders the report for a machine consumer. Empty lists are emitted as arrays rather
// than null, so a caller can index into them without a nil check.
func (r Report) JSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.withEmptyLists()); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return nil
}

// Above returns the report with findings below minConfidence removed. Open questions,
// pre-existing and immaterial findings pass through untouched: they are never boosted or
// dropped, and filtering them by confidence would discard what the reviewer most needs.
func (r Report) Above(minConfidence int) Report {
	out := r
	out.Findings = make([]Finding, 0, len(r.Findings))
	for _, f := range r.Findings {
		if f.Confidence >= minConfidence {
			out.Findings = append(out.Findings, f)
		}
	}
	return out
}

// ExitCode returns 1 when the report carries findings and 0 when it does not. A tool error
// exits 2 and is decided by the caller, not here.
func (r Report) ExitCode() int {
	if len(r.Findings) > 0 {
		return 1
	}
	return 0
}

func (r Report) headerLines() []string {
	title := "# Review"
	switch {
	case r.Scope.Task != "" && r.Scope.Run != "":
		title += ": " + r.Scope.Task + " / " + r.Scope.Run
	case r.Scope.Task != "":
		title += ": " + r.Scope.Task
	}
	out := []string{title, ""}
	if r.Scope.ScopePath != "" {
		out = append(out, "scope: `"+r.Scope.ScopePath+"`", "")
	}
	if names := r.Sources.DegradedNames(); len(names) > 0 {
		banner := fmt.Sprintf("**Degraded run**: %d of %d sources reported, missing %s",
			r.Sources.Reported, r.Sources.Expected, strings.Join(names, ", "))
		out = append(out, banner, "")
	}
	return out
}

func (r Report) findingsLines() []string {
	if len(r.Findings) == 0 {
		return []string{"No findings.", ""}
	}
	sorted := slices.Clone(r.Findings)
	slices.SortStableFunc(sorted, func(a, b Finding) int { return a.Severity.rank() - b.Severity.rank() })
	out := []string{}
	for i, f := range sorted {
		if i == 0 || f.Severity != sorted[i-1].Severity {
			out = append(out, "## "+f.Severity.Heading(), "")
		}
		out = append(out, f.markdownLines()...)
	}
	return out
}

func (r Report) listLines(title string, list []Finding) []string {
	if len(list) == 0 {
		return nil
	}
	out := []string{"## " + title, ""}
	for _, f := range list {
		out = append(out, f.markdownLines()...)
	}
	return out
}

func (r Report) sourcesLines() []string {
	if len(r.Sources.Agents) == 0 {
		return nil
	}
	out := []string{"## Sources", "",
		"| agent | executor | model | effort | tokens | status |",
		"| --- | --- | --- | --- | --- | --- |",
	}
	for _, a := range r.Sources.Agents {
		status := "ok"
		if a.Degraded {
			status = "degraded"
		}
		out = append(out, fmt.Sprintf("| %s | %s | %s | %s | %d | %s |",
			a.Name, a.Executor, a.model(), a.Effort, a.Tokens, status))
	}
	return append(out, "")
}

func (r Report) withEmptyLists() Report {
	out := r
	out.Findings = r.normalizeList(r.Findings)
	out.OpenQuestions = r.normalizeList(r.OpenQuestions)
	out.PreExisting = r.normalizeList(r.PreExisting)
	out.Immaterial = r.normalizeList(r.Immaterial)
	if out.Sources.DegradedSources == nil {
		out.Sources.DegradedSources = []string{}
	}
	if out.Sources.Agents == nil {
		out.Sources.Agents = []SourceStat{}
	}
	if out.Stats.Stages == nil {
		out.Stats.Stages = []StageRun{}
	}
	return out
}

func (r Report) normalizeList(list []Finding) []Finding {
	out := make([]Finding, len(list))
	for i, f := range list {
		if f.Sources == nil {
			f.Sources = []string{}
		}
		if f.Lenses == nil {
			f.Lenses = []string{}
		}
		out[i] = f
	}
	return out
}
