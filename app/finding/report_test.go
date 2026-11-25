package finding

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk on fire") }

func sampleReport() Report {
	return Report{
		Scope: Scope{Task: "pr-123", Run: "after-fix", ScopePath: "/tmp/tasks/pr-123/scope.md"},
		Sources: SourceStatus{
			Expected: 2, Reported: 2, DegradedSources: []string{},
			Agents: []SourceStat{
				{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Executor: "claude",
					RequestedModel: "opus", ActualModel: "claude-opus-5", Effort: "high", Tokens: 48210, Raised: 2},
				{Name: "codex", Lenses: []string{"adversarial"}, Executor: "codex",
					RequestedModel: "gpt-5.6-sol", ActualModel: "gpt-5.6-sol", Effort: "xhigh", Tokens: 31000, Raised: 2},
			},
		},
		Findings: []Finding{
			{ID: "codex-1", File: "app/pipeline/find.go", Line: 40, Severity: Minor, Confidence: 70,
				Title: "minor thing", Sources: []string{"codex"}, Lenses: []string{"adversarial"}},
			{ID: "bugs+impl-1", File: "app/executor/proc.go", Line: 120, EndLine: 128, Severity: Critical,
				Confidence: 92, Title: "watchdog never rearmed", Body: "the idle timer is armed once",
				Fix: "reset per line", Sources: []string{"bugs+impl", "codex"},
				Lenses: []string{"bugs", "adversarial"}, Verdict: Confirmed},
			{ID: "bugs+impl-2", File: "app/executor/claude.go", Line: 12, Severity: Major, Confidence: 81,
				Title: "flag ignored", Sources: []string{"bugs+impl"}, Lenses: []string{"bugs"}},
		},
		Stats: Stats{
			StartedAt:  time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC),
			FinishedAt: time.Date(2026, 7, 26, 16, 7, 44, 0, time.UTC),
			DurationMS: 333000, Tokens: 79210,
			Stages: []StageRun{{Name: "find", DurationMS: 200000}, {Name: "synthesis", DurationMS: 133000}},
		},
	}
}

func TestReport_MarkdownGroupsBySeverity(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, sampleReport().Markdown(buf))
	out := buf.String()

	assert.Contains(t, out, "# Review: pr-123 / after-fix")
	assert.Contains(t, out, "scope: `/tmp/tasks/pr-123/scope.md`")
	assert.NotContains(t, out, "**Degraded run**")

	crit, major, minor := strings.Index(out, "## Critical"), strings.Index(out, "## Major"), strings.Index(out, "## Minor")
	require.Positive(t, crit)
	assert.Less(t, crit, major, "critical renders before major regardless of input order")
	assert.Less(t, major, minor)

	assert.Contains(t, out, "### watchdog never rearmed")
	assert.Contains(t, out, "`app/executor/proc.go:120-128`")
	assert.Contains(t, out, "Fix: reset per line")
	assert.Contains(t, out, "_confidence: 92 | sources: bugs+impl, codex | lenses: bugs, adversarial | verdict: confirmed_")
}

func TestReport_MarkdownEmpty(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, Report{}.Markdown(buf))
	assert.Equal(t, "# Review\n\nNo findings.\n", buf.String())
}

func TestReport_MarkdownTitle(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		want  string
	}{
		{"task and run", Scope{Task: "pr-123", Run: "after-fix"}, "# Review: pr-123 / after-fix"},
		{"task only", Scope{Task: "pr-123"}, "# Review: pr-123"},
		{"run only", Scope{Run: "after-fix"}, "# Review"},
		{"neither", Scope{}, "# Review"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			require.NoError(t, Report{Scope: tt.scope}.Markdown(buf))
			assert.Equal(t, tt.want, strings.SplitN(buf.String(), "\n", 2)[0])
		})
	}
}

func TestReport_MarkdownDegradedBanner(t *testing.T) {
	tests := []struct {
		name    string
		sources SourceStatus
		want    string
	}{
		{"named list", SourceStatus{Expected: 4, Reported: 3, DegradedSources: []string{"docs+tests"}},
			"**Degraded run**: 3 of 4 sources reported, missing docs+tests"},
		{"from agent flag", SourceStatus{Expected: 2, Reported: 1,
			Agents: []SourceStat{{Name: "codex", Degraded: true}}},
			"**Degraded run**: 1 of 2 sources reported, missing codex"},
		{"both records agree", SourceStatus{Expected: 2, Reported: 1, DegradedSources: []string{"codex"},
			Agents: []SourceStat{{Name: "codex", Degraded: true}}},
			"**Degraded run**: 1 of 2 sources reported, missing codex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			require.NoError(t, Report{Sources: tt.sources}.Markdown(buf))
			assert.Contains(t, buf.String(), tt.want)
		})
	}
}

func TestReport_MarkdownExtraSections(t *testing.T) {
	rep := Report{
		OpenQuestions: []Finding{{File: "app/main.go", Line: 3, Title: "is this intentional", Confidence: 40}},
		PreExisting:   []Finding{{File: "app/old.go", Title: "predates the change", Confidence: 60}},
		Immaterial:    []Finding{{File: "app/x.go", Line: 9, Title: "true but not worth it", Confidence: 55}},
		Rejected: []Finding{{File: "app/y.go", Line: 2, Title: "nil map write", Confidence: 70, Verdict: Rejected,
			Reason: "the map is created in New"}},
	}

	buf := &bytes.Buffer{}
	require.NoError(t, rep.Markdown(buf))
	out := buf.String()

	assert.Contains(t, out, "## Rejected")
	assert.Contains(t, out, "### nil map write")
	assert.Contains(t, out, "Verifier: the map is created in New")

	assert.Contains(t, out, "## Open questions")
	assert.Contains(t, out, "### is this intentional")
	assert.Contains(t, out, "## Pre-existing")
	assert.Contains(t, out, "### predates the change")
	assert.Contains(t, out, "`app/old.go`", "a file-level finding renders as the bare path")
	assert.Contains(t, out, "## Immaterial")
	assert.Contains(t, out, "### true but not worth it")
}

func TestReport_MarkdownOmitsEmptySections(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, Report{}.Markdown(buf))
	out := buf.String()

	assert.NotContains(t, out, "## Open questions")
	assert.NotContains(t, out, "## Pre-existing")
	assert.NotContains(t, out, "## Immaterial")
	assert.NotContains(t, out, "## Rejected")
	assert.NotContains(t, out, "## Sources")
}

func TestReport_MarkdownSourceTable(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, sampleReport().Markdown(buf))
	out := buf.String()

	assert.Contains(t, out, "## Sources")
	assert.Contains(t, out, "| bugs+impl | claude | claude-opus-5 (requested opus) | high | 48210 | 2 | ok |",
		"the model actually used is reported next to the one requested")
	assert.Contains(t, out, "| codex | codex | gpt-5.6-sol | xhigh | 31000 | 2 | ok |")
}

func TestReport_MarkdownSourceTableStatus(t *testing.T) {
	rep := Report{Sources: SourceStatus{Expected: 1, Agents: []SourceStat{
		{Name: "docs+tests", Executor: "claude", Degraded: true},
	}}}

	buf := &bytes.Buffer{}
	require.NoError(t, rep.Markdown(buf))
	assert.Contains(t, buf.String(), "| docs+tests | claude | - |  | 0 | 0 | degraded |")
}

// an agent whose tools are broken answers with a valid empty list and degrades nothing, so the table is
// the only place that difference is visible.
func TestReport_MarkdownSourceRaisedNothing(t *testing.T) {
	rep := Report{Sources: SourceStatus{Expected: 2, Reported: 2, Agents: []SourceStat{
		{Name: "cost", Executor: "codex", ActualModel: "gpt-5.6-sol", Effort: "high", Tokens: 900, Raised: 0},
		{Name: "thesis", Executor: "claude", ActualModel: "opus", Effort: "high", Tokens: 40000, Raised: 6},
	}}}

	buf := &bytes.Buffer{}
	require.NoError(t, rep.Markdown(buf))
	out := buf.String()
	assert.Contains(t, out, "| cost | codex | gpt-5.6-sol | high | 900 | 0 | ok, nothing raised |")
	assert.Contains(t, out, "| thesis | claude | opus | high | 40000 | 6 | ok |")
	assert.NotContains(t, out, "Degraded run", "a source that answered is not a degraded run")
}

func TestReport_MarkdownEveryEnumValue(t *testing.T) {
	rep := Report{}
	for i, s := range allSeverities {
		for j, v := range allVerdicts {
			rep.Findings = append(rep.Findings, Finding{
				File: "app/main.go", Line: i*10 + j, Severity: s, Verdict: v,
				Title: string(s) + "/" + string(v), Confidence: 90,
			})
		}
	}

	buf := &bytes.Buffer{}
	require.NoError(t, rep.Markdown(buf))
	out := buf.String()

	for _, s := range allSeverities {
		assert.Contains(t, out, "## "+s.Heading(), "severity %q must render its own group", s)
		for _, v := range allVerdicts {
			assert.Contains(t, out, "### "+string(s)+"/"+string(v))
			assert.Contains(t, out, "verdict: "+string(v))
		}
	}
}

func TestReport_MarkdownUnknownSeverityStillRenders(t *testing.T) {
	rep := Report{Findings: []Finding{
		{File: "a.go", Line: 1, Severity: Severity("catastrophic"), Title: "typo severity", Confidence: 90},
		{File: "b.go", Line: 2, Severity: Critical, Title: "known severity", Confidence: 90},
	}}

	buf := &bytes.Buffer{}
	require.NoError(t, rep.Markdown(buf))
	out := buf.String()

	assert.Contains(t, out, "### typo severity")
	assert.Less(t, strings.Index(out, "## Critical"), strings.Index(out, "## Catastrophic"))
}

func TestReport_MarkdownWriteError(t *testing.T) {
	err := sampleReport().Markdown(failingWriter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write markdown report")
}

func TestReport_JSONRoundTrip(t *testing.T) {
	in := sampleReport()
	in.OpenQuestions = []Finding{{ID: "q1", File: "a.go", Title: "why", Sources: []string{"codex"}, Lenses: []string{"adversarial"}}}
	in.PreExisting = []Finding{{ID: "p1", File: "b.go", Title: "old", Sources: []string{"codex"}, Lenses: []string{"adversarial"}}}
	in.Immaterial = []Finding{{ID: "i1", File: "c.go", Title: "meh", Sources: []string{"codex"}, Lenses: []string{"adversarial"}}}
	in.Rejected = []Finding{{ID: "r1", File: "d.go", Title: "no", Sources: []string{"codex"}, Lenses: []string{"adversarial"},
		Verdict: Rejected, Reason: "unreachable"}}

	buf := &bytes.Buffer{}
	require.NoError(t, in.JSON(buf))

	var out Report
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, in, out)
}

func TestReport_JSONShape(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, sampleReport().JSON(buf))

	raw := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &raw))
	for _, key := range []string{"scope", "sources", "findings", "open_questions", "pre_existing", "immaterial", "rejected", "stats"} {
		assert.Contains(t, raw, key)
	}

	sources := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(raw["sources"], &sources))
	assert.Contains(t, sources, "degraded", "the Go field is DegradedSources but the wire key stays degraded")
	assert.Contains(t, sources, "expected")
	assert.Contains(t, sources, "reported")
	assert.Contains(t, sources, "agents")
}

func TestReport_JSONEmptyListsAreArrays(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, Report{}.JSON(buf))
	out := buf.String()

	assert.Contains(t, out, `"findings": []`)
	assert.Contains(t, out, `"open_questions": []`)
	assert.Contains(t, out, `"pre_existing": []`)
	assert.Contains(t, out, `"immaterial": []`)
	assert.Contains(t, out, `"rejected": []`)
	assert.Contains(t, out, `"degraded": []`)
	assert.Contains(t, out, `"agents": []`)
	assert.Contains(t, out, `"stages": []`)
	assert.NotContains(t, out, "null")
}

func TestStats_JSONStageTokensAlwaysPresent(t *testing.T) {
	// a stage that spent nothing — a verify with nothing to check, or an executor that reported no
	// usage — still carries "tokens": 0, so a caller can tell a recorded zero from a field nobody wrote;
	// an omitempty on StageRun.Tokens would erase exactly that distinction
	data, err := json.Marshal(Stats{Stages: []StageRun{{Name: "verify"}}})
	require.NoError(t, err)
	var raw struct {
		Stages []map[string]json.RawMessage `json:"stages"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Len(t, raw.Stages, 1)
	require.Contains(t, raw.Stages[0], "tokens", "the stage entry itself, not the run total beside it")
	assert.JSONEq(t, "0", string(raw.Stages[0]["tokens"]))
	assert.NotContains(t, raw.Stages[0], "executor", "nothing ran, so the runner fields stay omitted")
}

func TestReport_JSONFindingListsAreArrays(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, Report{Findings: []Finding{{ID: "a-1", File: "a.go"}}}.JSON(buf))
	assert.NotContains(t, buf.String(), "null", "a finding with no sources or lenses still emits arrays")
}

func TestReport_JSONWriteError(t *testing.T) {
	err := sampleReport().JSON(failingWriter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode report")
}

func TestReport_Above(t *testing.T) {
	rep := Report{Findings: []Finding{
		{ID: "a", Confidence: 95}, {ID: "b", Confidence: 80}, {ID: "c", Confidence: 79}, {ID: "d", Confidence: 0},
	}}

	tests := []struct {
		name string
		min  int
		want []string
	}{
		{"keeps all at zero", 0, []string{"a", "b", "c", "d"}},
		{"threshold is inclusive", 80, []string{"a", "b"}},
		{"drops everything", 100, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rep.Above(tt.min)
			ids := make([]string, 0, len(got.Findings))
			for _, f := range got.Findings {
				ids = append(ids, f.ID)
			}
			assert.Equal(t, tt.want, nilIfEmpty(ids))
			assert.Len(t, rep.Findings, 4, "the source report is not mutated")
		})
	}
}

func TestReport_AboveKeepsTheOtherLists(t *testing.T) {
	rep := Report{
		Scope:         Scope{Task: "pr-1"},
		Sources:       SourceStatus{Expected: 1, Reported: 1},
		Findings:      []Finding{{ID: "a", Confidence: 10}},
		OpenQuestions: []Finding{{ID: "q", Confidence: 10}},
		PreExisting:   []Finding{{ID: "p", Confidence: 0}},
		Immaterial:    []Finding{{ID: "i", Confidence: 5}},
		Rejected:      []Finding{{ID: "r", Confidence: 5}},
		Stats:         Stats{Tokens: 42},
	}

	got := rep.Above(80)
	assert.Empty(t, got.Findings)
	assert.Equal(t, rep.OpenQuestions, got.OpenQuestions, "open questions are never filtered by confidence")
	assert.Equal(t, rep.PreExisting, got.PreExisting)
	assert.Equal(t, rep.Immaterial, got.Immaterial)
	assert.Equal(t, rep.Rejected, got.Rejected)
	assert.Equal(t, rep.Scope, got.Scope)
	assert.Equal(t, rep.Sources, got.Sources)
	assert.Equal(t, rep.Stats, got.Stats)
}

func TestReport_ExitCode(t *testing.T) {
	tests := []struct {
		name string
		rep  Report
		want int
	}{
		{"no findings", Report{}, 0},
		{"findings", Report{Findings: []Finding{{ID: "a"}}}, 1},
		{"only open questions", Report{OpenQuestions: []Finding{{ID: "q"}}}, 0},
		{"only rejected", Report{Rejected: []Finding{{ID: "r", Verdict: Rejected, Reason: "not reachable"}}}, 0},
		{"filtered out", Report{Findings: []Finding{{ID: "a", Confidence: 10}}}.Above(80), 0},
		{"survives filter", Report{Findings: []Finding{{ID: "a", Confidence: 90}}}.Above(80), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.rep.ExitCode())
		})
	}
}

func TestSourceStatus_Degraded(t *testing.T) {
	tests := []struct {
		name  string
		s     SourceStatus
		want  bool
		names []string
	}{
		{"clean", SourceStatus{Expected: 2, Reported: 2, Agents: []SourceStat{{Name: "a"}}}, false, nil},
		{"named", SourceStatus{DegradedSources: []string{"a"}}, true, []string{"a"}},
		{"agent flag only", SourceStatus{Agents: []SourceStat{{Name: "a", Degraded: true}}}, true, []string{"a"}},
		{"both, deduped", SourceStatus{DegradedSources: []string{"a"},
			Agents: []SourceStat{{Name: "a", Degraded: true}}}, true, []string{"a"}},
		{"two different", SourceStatus{DegradedSources: []string{"a"},
			Agents: []SourceStat{{Name: "b", Degraded: true}}}, true, []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.s.Degraded())
			assert.Equal(t, tt.names, nilIfEmpty(tt.s.DegradedNames()))
		})
	}
}

func TestSourceStat_JSONRoundTrip(t *testing.T) {
	in := SourceStat{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Executor: "claude",
		RequestedModel: "opus", ActualModel: "claude-opus-5", Effort: "high", Tokens: 48210, Degraded: true}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	var out SourceStat
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, in, out)

	raw := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(data, &raw))
	for _, key := range []string{"name", "lenses", "executor", "requested_model", "actual_model", "effort", "tokens", "degraded"} {
		assert.Contains(t, raw, key)
	}
}

func TestSourceStat_Model(t *testing.T) {
	tests := []struct {
		name string
		s    SourceStat
		want string
	}{
		{"both empty", SourceStat{}, "-"},
		{"requested only", SourceStat{RequestedModel: "opus"}, "opus"},
		{"actual only", SourceStat{ActualModel: "claude-opus-5"}, "claude-opus-5"},
		{"same", SourceStat{RequestedModel: "opus", ActualModel: "opus"}, "opus"},
		{"silently substituted", SourceStat{RequestedModel: "haiku", ActualModel: "claude-sonnet-4-6"},
			"claude-sonnet-4-6 (requested haiku)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.s.model())
		})
	}
}

func TestStats_JSONRoundTrip(t *testing.T) {
	in := Stats{
		StartedAt:  time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC),
		FinishedAt: time.Date(2026, 7, 26, 16, 7, 44, 0, time.UTC),
		DurationMS: 333000, Tokens: 166470,
		Stages: []StageRun{{Name: "find", DurationMS: 200000, Tokens: 141200},
			{Name: "synthesis", DurationMS: 133000, Tokens: 25270}},
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	var out Stats
	require.NoError(t, json.Unmarshal(data, &out))
	assert.True(t, in.StartedAt.Equal(out.StartedAt), "manifest.json and the prior-round history both read this")
	assert.True(t, in.FinishedAt.Equal(out.FinishedAt))
	assert.Equal(t, in.Stages, out.Stages, "a stage duration cannot be recomputed after the fact")
	assert.Equal(t, in.Tokens, out.Tokens)
	assert.Equal(t, in.DurationMS, out.DurationMS)
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
