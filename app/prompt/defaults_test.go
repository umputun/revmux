package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaults_EveryShippedFileHasADescription(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, l := range set.Lenses() {
		assert.NotEmpty(t, l.Description, "lens %s", l.Name)
	}
	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		assert.NotEmpty(t, p.Description, "profile %s", name)
	}
	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err)
		assert.NotEmpty(t, st.Description, "stage %s", name)
	}
}

func TestDefaults_FocusedRoster(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("focused")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	require.Len(t, specs, 2)

	assert.Equal(t, AgentSpec{Name: "bugs", Lenses: []string{"bugs"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6", ColorName: "cyan"}, specs[0])
	assert.Equal(t, AgentSpec{Name: "adversarial", Lenses: []string{"adversarial"}, Executor: "codex",
		Model: "gpt-5.6-sol", Effort: "high", Color: "3", ColorName: "yellow"}, specs[1],
		"the adversarial entry composes a lens, not a prompt file of its own")
}

func TestDefaults_ProfileBodyNamesEveryContextPathAsAPath(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)

		for _, v := range []string{"SCOPE", "GOAL", "PROFILE", "CONTEXT", "WORKDIR"} {
			assert.Contains(t, p.Body, "{{"+v+"}}", "%s: a variable the caller resolves must appear in the body", name)
		}
		assert.Contains(t, strings.ToLower(p.Body), "a **path**, not the text it names",
			"%s: a path handed to an agent with no instruction is a path it may ignore", name)
		assert.Contains(t, p.Body, "none provided", "%s: the body must say what an absent context file looks like", name)
	}
}

func TestDefaults_EveryProfileResolvesItsRoster(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	known := set.LensNames()
	require.Len(t, set.ProfileNames(), 10,
		"the shipped set is comprehensive, focused, final, claude-only, codex-only, agy-only, trio, grill-me, triage and expert")

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		specs, err := p.Roster(nil, known)
		require.NoError(t, err, "%s", name)
		require.NotEmpty(t, specs, "%s", name)

		colors := map[string]string{}
		for _, spec := range specs {
			assert.NotEmpty(t, spec.ColorName, "%s/%s: a shipped roster names its own color rather than leaning on the palette",
				name, spec.Name)
			assert.NotContains(t, colors, spec.Color, "%s: %s and %s share a color", name, colors[spec.Color], spec.Name)
			colors[spec.Color] = spec.Name
			assert.NotEmpty(t, spec.Model, "%s/%s: a roster entry with no model runs on whatever the binary defaults to",
				name, spec.Name)
			assert.NotEmpty(t, spec.Effort, "%s/%s: effort must resolve from the profile when the entry omits it",
				name, spec.Name)
			for _, l := range spec.Lenses {
				assert.Contains(t, known, l, "%s/%s: unknown lens %s", name, spec.Name, l)
			}
		}
	}
}

func TestDefaults_EveryProfileResolvesEveryStage(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		for _, stage := range []string{"synthesis", "verify"} {
			st, err := p.Stage(set, stage)
			require.NoError(t, err, "%s/%s", name, stage)
			assert.NotEmpty(t, st.Executor, "%s/%s", name, stage)
			assert.NotEmpty(t, st.Model, "%s/%s: a stage with no model runs on whatever the binary defaults to",
				name, stage)
			assert.NotEmpty(t, st.Body, "%s/%s: the body is the stage file's whether or not the profile overrides",
				name, stage)
		}
	}
}

// codex-only is the profile that would silently be two thirds claude if a profile could not name its
// stages, so it pins the whole run on one binary rather than only the find stage
func TestDefaults_CodexOnlyRunsEveryStageOnCodex(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("codex-only")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	for _, spec := range specs {
		assert.Equal(t, "codex", spec.Executor, spec.Name)
	}

	for _, stage := range []string{"synthesis", "verify"} {
		st, stErr := p.Stage(set, stage)
		require.NoError(t, stErr)
		assert.Equal(t, "codex", st.Executor, stage)
		assert.Equal(t, "gpt-5.6-sol", st.Model, "%s inherits the profile's own model", stage)
		assert.Equal(t, "high", st.Effort, "%s inherits the profile's own effort", stage)
	}

	// --lenses replaces the roster with one agent inheriting the profile's model, so an executor
	// hardcoded to claude here would ask claude for a codex model and would need the binary this
	// profile exists to do without
	override, err := p.Roster([]string{"bugs"}, set.LensNames())
	require.NoError(t, err)
	require.Len(t, override, 1)
	assert.Equal(t, "codex", override[0].Executor)
	assert.Equal(t, "gpt-5.6-sol", override[0].Model)

	claude, err := set.Profile("claude-only")
	require.NoError(t, err)
	for _, stage := range []string{"synthesis", "verify"} {
		st, stErr := claude.Stage(set, stage)
		require.NoError(t, stErr)
		assert.Equal(t, "claude", st.Executor, "%s: the mirror profile names no override and keeps the file's own", stage)
	}
}

func TestDefaults_ComprehensiveRoster(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("comprehensive")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	require.Len(t, specs, 4)

	assert.Equal(t, AgentSpec{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6", ColorName: "cyan"}, specs[0])
	assert.Equal(t, AgentSpec{Name: "arch+quality", Lenses: []string{"architecture", "quality"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "5", ColorName: "magenta"}, specs[1])
	assert.Equal(t, AgentSpec{Name: "docs+tests", Lenses: []string{"docs", "tests", "comments"},
		Executor: "claude", Model: "opus", Effort: "high", Color: "2", ColorName: "green"}, specs[2],
		"comments rides with docs rather than taking a slot of its own: the two overlap on a stale doc "+
			"comment, so one process settles that where two would both report it, and a fifth agent "+
			"would queue behind the default --max-parallel of 4")
	assert.Equal(t, AgentSpec{Name: "adversarial", Lenses: []string{"adversarial"}, Executor: "codex",
		Model: "gpt-5.6-sol", Effort: "high", Color: "3", ColorName: "yellow"}, specs[3],
		"adversarial is a peer source in the default roster, not a second pass over the others")

	var carried []string
	for _, spec := range specs {
		carried = append(carried, spec.Lenses...)
	}
	assert.ElementsMatch(t,
		[]string{"bugs", "impl", "architecture", "quality", "docs", "tests", "comments", "adversarial"}, carried,
		"the flagship profile carries every code-review lens exactly once; the triage lenses belong to a "+
			"review shape it does not run")
}

func TestDefaults_TriageRoster(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("triage")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	require.Len(t, specs, 4)

	assert.Equal(t, AgentSpec{Name: "facts", Lenses: []string{"grounding", "precedent"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6", ColorName: "cyan"}, specs[0])
	assert.Equal(t, AgentSpec{Name: "thesis", Lenses: []string{"thesis"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "2", ColorName: "green"}, specs[1])
	assert.Equal(t, AgentSpec{Name: "antithesis", Lenses: []string{"antithesis"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "5", ColorName: "magenta"}, specs[2])
	assert.Equal(t, AgentSpec{Name: "cost", Lenses: []string{"cost"}, Executor: "codex",
		Model: "gpt-5.6-sol", Effort: "high", Color: "3", ColorName: "yellow"}, specs[3],
		"cost takes the codex slot because it only reads code: a sandbox with no network cannot empty it, "+
			"and losing codex costs one input rather than the panel's opposition")

	assert.Contains(t, p.Description, "--no-synthesis",
		"a bare --profile triage runs the drop rule over arguments that are single-source by construction")
	body := strings.Join(strings.Fields(p.Body), " ")
	assert.Contains(t, body, "Leave the file field empty when the point is not about a line of code",
		"the finder schema requires file and describes it as a path, so the profile is the counterweight")
}

func TestDefaults_FinalReportsOnlyTheTopTwoSeverities(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	p, err := set.Profile("final")
	require.NoError(t, err)

	specs, err := p.Roster(nil, set.LensNames())
	require.NoError(t, err)
	assert.Len(t, specs, 2, "the last pass before merge is narrow by design")

	body := strings.ToLower(p.Body)
	assert.Contains(t, body, "critical")
	assert.Contains(t, body, "major")
	assert.NotContains(t, body, "**minor**",
		"a profile that still defines minor as a severity will be handed minor findings")
}

func TestDefaults_SeverityContract(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	// derived from the shipped set rather than listed, so a new full profile is guarded without being
	// named here. final reports two severities, triage rates decision weight rather than runtime damage,
	// and expert reviews a plan as well as a diff so it rates what goes wrong if the thing is built;
	// all three are asserted separately below.
	var expected string
	compared := 0
	for _, name := range set.ProfileNames() {
		if name == "final" || name == "triage" || name == "expert" {
			continue
		}
		p, profileErr := set.Profile(name)
		require.NoError(t, profileErr)
		start := strings.Index(p.Body, "## Severity bar")
		require.NotEqual(t, -1, start, "profile %s has no severity section", name)
		end := strings.Index(p.Body[start:], "\n## Reporting")
		require.NotEqual(t, -1, end, "profile %s has no reporting section after severity", name)
		section := p.Body[start : start+end]
		compared++
		if expected == "" {
			expected = section
			continue
		}
		assert.Equal(t, expected, section, "full profiles must gate the same finding at the same severity")
	}
	require.Greater(t, compared, 1, "a contract over one profile compares nothing")
	assert.Contains(t, expected, "Severity is what goes wrong when the code runs")
	assert.Contains(t, expected, "broken contract a caller executes against")
	assert.Contains(t, expected, "Human-facing prose is never that")

	final, err := set.Profile("final")
	require.NoError(t, err)
	finalText := strings.Join(strings.Fields(final.Body), " ")
	assert.Contains(t, finalText, "Severity is what goes wrong when the code runs")
	assert.Contains(t, finalText, "document a machine or an agent executes against as a contract")

	triage, err := set.Profile("triage")
	require.NoError(t, err)
	triageText := strings.Join(strings.Fields(triage.Body), " ")
	assert.Contains(t, triageText, "Severity is how much a point bears on the decision")
	assert.Contains(t, triageText, "**critical** — decisive on its own")
	assert.NotContains(t, triageText, "Severity is what goes wrong when the code runs",
		"a panel arguing about a filed item has no code running to go wrong")

	expert, err := set.Profile("expert")
	require.NoError(t, err)
	expertText := strings.Join(strings.Fields(expert.Body), " ")
	assert.Contains(t, expertText, "Rate by what goes wrong if it is built and run as written")
	assert.Contains(t, expertText, "the approach cannot work")
	assert.Contains(t, expertText, "a plan under review is exactly such a document")
	assert.NotContains(t, expertText, "Severity is what goes wrong when the code runs",
		"a plan has no runtime, and expert reviews one as readily as a diff")
}

func TestDefaults_EveryLensIsComposedBySomeProfile(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	// derived from the shipped set rather than listed: a lens no roster names never runs, and the only
	// thing that would otherwise notice is a reviewer wondering why its findings stopped arriving
	composed := map[string]struct{}{}
	for _, name := range set.ProfileNames() {
		p, profileErr := set.Profile(name)
		require.NoError(t, profileErr)
		specs, rosterErr := p.Roster(nil, set.LensNames())
		require.NoError(t, rosterErr)
		for _, spec := range specs {
			for _, lens := range spec.Lenses {
				composed[lens] = struct{}{}
			}
		}
	}

	for _, l := range set.Lenses() {
		assert.Contains(t, composed, l.Name, "lens %s is in no shipped profile's roster, so nothing ever runs it", l.Name)
	}
}

func TestDefaults_LensesAreSelfContained(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)
	lenses := set.Lenses()
	require.Len(t, lenses, 13,
		"the shipped set is bugs, impl, architecture, quality, docs, tests, comments and adversarial for code "+
			"review, plus grounding, precedent, thesis, antithesis and cost for triage")

	for _, l := range lenses {
		body, err := set.lens(l.Name)
		require.NoError(t, err)
		assert.NotEmpty(t, strings.TrimSpace(body), "lens %s is empty", l.Name)
		assert.Empty(t, varPattern.FindString(body),
			"lens %s names a variable; context instructions are shared preamble and belong in the profile body", l.Name)
		assert.Contains(t, body, "## Lens: "+l.Name, "lens %s must say which lens raised a finding", l.Name)
	}
}

func TestDefaults_NoShippedFileCarriesThePriorRoundBlock(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	bodies := map[string]string{}
	for _, l := range set.Lenses() {
		body, err := set.lens(l.Name)
		require.NoError(t, err)
		bodies["lenses/"+l.Name] = body
	}
	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		bodies["profiles/"+name] = p.Body
	}
	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err)
		bodies["stages/"+name] = st.Body
	}

	for path, body := range bodies {
		lower := strings.ToLower(body)
		for _, phrase := range []string{"prior round", "previous round", "earlier round", "runs/", "re-evaluate everything"} {
			assert.NotContains(t, lower, phrase,
				"%s mentions %q; the history block is injected, and a copy here drifts from the injected text", path, phrase)
		}
	}
}

func TestDefaults_LensesStayExecutorAgnostic(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	for _, l := range set.Lenses() {
		body, err := set.lens(l.Name)
		require.NoError(t, err)
		lower := strings.ToLower(body)
		for _, banned := range []string{"json", "schema", "claude", "codex", "agy"} {
			assert.NotContains(t, lower, banned,
				"lens %s must not carry an output contract or name a binary", l.Name)
		}
	}
}

func TestDefaults_ComposeEveryAgentOfEveryProfile(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	vars := Vars{"SCOPE": "/s.md", "GOAL": "none provided", "PROFILE": "none provided",
		"CONTEXT": "none provided", "WORKDIR": "/repo", "FINDINGS": "[]", "SOURCES": "2 of 2"}

	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		require.NoError(t, err)
		specs, err := p.Roster(nil, set.LensNames())
		require.NoError(t, err)
		for _, spec := range specs {
			out, err := p.Compose(set, spec, ComposeOpts{Vars: vars})
			require.NoError(t, err, "%s/%s", name, spec.Name)
			assert.NotEmpty(t, out)
		}
	}

	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err)
		out, err := st.Compose(ComposeOpts{Vars: vars})
		require.NoError(t, err, name)
		assert.NotEmpty(t, out)
	}
}

func TestDefaults_WhatNotToReportContract(t *testing.T) {
	// the second block duplicated byte-identically across every profile, and the one a finder consults
	// before writing anything down: a rule added to one and not the others makes the same finding
	// reportable under one review shape and suppressed under another. The severity bar has been pinned
	// since it shipped and has never drifted; this one was unpinned and drifted twice
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	var expected string
	compared := 0
	for _, name := range set.ProfileNames() {
		if name == "triage" || name == "expert" {
			// triage suppresses arguments about a filed item rather than findings about a diff, and
			// expert reviews a plan too, where no linter ran and no line was touched
			continue
		}
		p, profileErr := set.Profile(name)
		require.NoError(t, profileErr)
		start := strings.Index(p.Body, "## What not to report")
		require.NotEqual(t, -1, start, "profile %s has no what-not-to-report section", name)
		section := strings.TrimSpace(p.Body[start:])
		compared++
		if expected == "" {
			expected = section
			continue
		}
		assert.Equal(t, expected, section,
			"profile %s suppresses a different set than the others do", name)
	}
	require.Greater(t, compared, 1, "a contract over one profile compares nothing")
	assert.Contains(t, expected, "a defect on a line this change did not touch")
	assert.Contains(t, expected, "anything a linter, compiler or type checker catches")
	assert.Contains(t, expected, "Pre-existing problems are the one exception")

	triage, err := set.Profile("triage")
	require.NoError(t, err)
	start := strings.Index(triage.Body, "## What not to report")
	require.NotEqual(t, -1, start, "triage has no what-not-to-report section")
	section := strings.TrimSpace(triage.Body[start:])
	assert.Contains(t, section, "a restatement of the item")
	assert.Contains(t, section, "the case another panelist is making")
	assert.Contains(t, section, "a cost, a duplicate or a risk you did not actually check")
	assert.NotContains(t, section, "linter",
		"a triage panel is not told to defer to tools that ran before a review it is not doing")

	expert, err := set.Profile("expert")
	require.NoError(t, err)
	start = strings.Index(expert.Body, "## What not to report")
	require.NotEqual(t, -1, start, "expert has no what-not-to-report section")
	section = strings.TrimSpace(expert.Body[start:])
	assert.Contains(t, section, "reviewing a change: a defect on a line it did not touch")
	assert.Contains(t, section, "reviewing a proposal: a detail it deliberately leaves to implementation")
	assert.Contains(t, section, "Pre-existing problems are the one exception")
}
