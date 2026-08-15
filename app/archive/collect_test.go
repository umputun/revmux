package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

func TestRoundReader_readFullRound(t *testing.T) {
	// the shape a real four-agent round leaves behind: 26 found, 10 findings plus 7 pre-existing after
	// synthesis, 9 plus 8 after verification, and a findings.json --min-confidence cut down to 13
	dir := t.TempDir()
	found := stageOne()
	seen := map[string]bool{}
	for _, f := range found {
		require.False(t, seen[f.ID], "a real stage 1 gives every finding its own id, and %s repeats", f.ID)
		seen[f.ID] = true
	}
	writeArtifact(t, dir, foundFile, finding.Report{Sources: roster(), Findings: found})
	// verification reclassified one of the ten synthesized findings as pre-existing rather than rejecting
	// it. The synthesis snapshot carries no verdicts, because synthesis writes none — giving it any would
	// credit this stage with refinements verification made, which is the attribution `judged` exists to
	// get right
	synthesized := make([]finding.Finding, 0, len(verifiedFindings())+1)
	for _, f := range append(verifiedFindings(), preExisting()[7]) {
		f.Verdict = ""
		synthesized = append(synthesized, f)
	}
	unverified := make([]finding.Finding, 0, 7)
	for _, f := range preExisting()[:7] {
		f.Verdict = ""
		unverified = append(unverified, f)
	}
	writeArtifact(t, dir, synthesizedFile, finding.Report{
		Sources: roster(), Findings: synthesized, PreExisting: unverified,
	})
	writeArtifact(t, dir, verifiedFile, finding.Report{
		Sources: roster(), Findings: verifiedFindings(), PreExisting: preExisting(),
	})
	// the four dropped here are two codex findings and two docs+tests ones, so reading survivors from the
	// filtered report would report 8 and 5 rather than the 10 and 7 the stage snapshot carries
	writeArtifact(t, dir, findingsFile, finding.Report{
		Sources: roster(), Findings: []finding.Finding{
			verifiedFindings()[0], verifiedFindings()[3], verifiedFindings()[5],
			verifiedFindings()[7], verifiedFindings()[8],
		},
		PreExisting: preExisting(),
	})

	r := newRoundReader(dir)
	require.NoError(t, r.read())
	got := r.stats()

	t.Run("agent tallies follow the roster order and the stage snapshots", func(t *testing.T) {
		assert.Equal(t, []agentStats{
			{Name: "bugs+impl", Raised: 5, Survived: 5, Corroborated: 4, Tokens: 4986379},
			{Name: "arch+quality", Raised: 4, Survived: 4, Corroborated: 3, Tokens: 3644082},
			{Name: "docs+tests", Raised: 7, Survived: 7, Corroborated: 3, Tokens: 6374878},
			{Name: "codex", Raised: 10, Survived: 10, Corroborated: 4, Tokens: 168467},
		}, got.agents, "survived comes from stage 3, never from the filtered findings.json")
	})

	t.Run("lens tallies come from stage 1 and carry their verdict mix", func(t *testing.T) {
		assert.Equal(t, []lensStats{
			{Name: "bugs", Raised: 3, Ambiguous: 2, Verdicts: map[finding.Verdict]int{
				finding.Confirmed: 1, finding.Refined: 1, finding.Unverified: 1}},
			{Name: "impl", Raised: 4, Ambiguous: 2, Verdicts: map[finding.Verdict]int{
				finding.Confirmed: 2, finding.Refined: 1, finding.Unverified: 1}},
			{Name: "architecture", Raised: 3, Ambiguous: 1, Verdicts: map[finding.Verdict]int{
				finding.Refined: 2, finding.Unverified: 1}},
			{Name: "quality", Raised: 2, Ambiguous: 1, Verdicts: map[finding.Verdict]int{
				finding.Confirmed: 1, finding.Refined: 1}},
			{Name: "docs", Raised: 6, Ambiguous: 1, Verdicts: map[finding.Verdict]int{
				finding.Confirmed: 2, finding.Refined: 2, finding.Unverified: 2}},
			{Name: "tests", Raised: 2, Ambiguous: 1, Verdicts: map[finding.Verdict]int{finding.Confirmed: 2}},
			{Name: "adversarial", Raised: 10, Verdicts: map[finding.Verdict]int{
				finding.Confirmed: 3, finding.Refined: 3, finding.Unverified: 3, finding.PreExisting: 1}},
		}, got.lenses)
	})

	t.Run("stage attrition chains the artifacts the round carries", func(t *testing.T) {
		assert.Equal(t, []stageFlow{
			{Name: "synthesis", In: 26, Out: 17, Reclassified: 7},
			{Name: "verify", In: 17, Out: 17, Reclassified: 1, Refined: 4},
			{Name: "report", In: 17, Out: 13},
		}, got.stages, "the refinements belong to verify, which writes verdicts, and not to synthesis, "+
			"which writes none; verify also moved one finding out of the actionable list without changing "+
			"the total, which is the work In and Out cannot show; report judges nothing, since counting "+
			"the verdicts findings.json still carries would credit the filter with what it passed through")
	})
}

// --no-synthesis --no-verify writes neither snapshot, so stages/1-found.json is the last word and nothing
// filtered anything: survived equals raised because no stage ran, not because the agent earned it.
func TestRoundReader_read(t *testing.T) {
	t.Run("an agent whose findings all get dropped survives nothing", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("keeper", "bugs"), agent("noise", "quality")),
			Findings: slices.Concat(
				raised(2, "keeper", "bugs"),
				raised(3, "noise", "quality"),
			),
		})
		writeArtifact(t, dir, verifiedFile, finding.Report{
			Sources:  sources(agent("keeper", "bugs"), agent("noise", "quality")),
			Findings: raised(2, "keeper", "bugs"),
		})

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		assert.Equal(t, []agentStats{
			{Name: "keeper", Raised: 2, Survived: 2},
			{Name: "noise", Raised: 3},
		}, r.stats().agents, "raised and survived are read from different snapshots and must not track each other")
	})

	t.Run("a run that skipped verification counts survivors from the synthesis snapshot", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("solo", "bugs")), Findings: raised(4, "solo", "bugs"),
		})
		writeArtifact(t, dir, synthesizedFile, finding.Report{
			Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs"),
		})

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		got := r.stats()
		assert.Equal(t, []agentStats{{Name: "solo", Raised: 4, Survived: 2}}, got.agents,
			"stages/3-verified.json was never written, so the stage before it is the last word")
		assert.Equal(t, []stageFlow{{Name: "synthesis", In: 4, Out: 2}}, got.stages,
			"a stage nothing wrote is not a transition that happened")
	})

	t.Run("a run that skipped both stages counts stage 1 as its own survivors", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("solo", "bugs")), Findings: raised(6, "solo", "bugs"),
		})
		writeArtifact(t, dir, findingsFile, finding.Report{
			Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs"),
		})

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		got := r.stats()
		assert.Equal(t, []agentStats{{Name: "solo", Raised: 6, Survived: 6}}, got.agents,
			"the last stage snapshot is stage 1 itself, and findings.json is never a survivor source")
		assert.Equal(t, []stageFlow{{Name: "report", In: 6, Out: 2}}, got.stages,
			"only the --min-confidence cut happened, and neither skipped stage is reported as a transition")
	})

	t.Run("a survivor with no verdict counts as unverified", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs"),
		})
		writeArtifact(t, dir, verifiedFile, finding.Report{
			Sources:     sources(agent("solo", "bugs")),
			Findings:    []finding.Finding{{Sources: []string{"solo"}, Lenses: []string{"bugs"}, Verdict: finding.Confirmed}},
			PreExisting: []finding.Finding{{Sources: []string{"solo"}, Lenses: []string{"bugs"}}},
		})

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		assert.Equal(t, map[finding.Verdict]int{finding.Confirmed: 1, finding.Unverified: 1},
			r.stats().lenses[0].Verdicts, "a pre-existing issue is never verified, and unverified says exactly that")
	})

	t.Run("degraded is the per-agent flag as well as the explicit list", func(t *testing.T) {
		dir := t.TempDir()
		flagged := agent("flagged", "bugs")
		flagged.Degraded = true
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: finding.SourceStatus{
				Expected: 3, Reported: 1, DegradedSources: []string{"listed"},
				Agents: []finding.SourceStat{agent("fine", "docs"), flagged, agent("listed", "tests")},
			},
			Findings: raised(1, "fine", "docs"),
		})

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		assert.Equal(t, []agentStats{
			{Name: "fine", Raised: 1, Survived: 1},
			{Name: "flagged", DegradedRounds: 1},
			{Name: "listed", DegradedRounds: 1},
		}, r.stats().agents, "the two records of a degrade disagree, so both count")
	})

	t.Run("a round with no stages/1-found.json is refused", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, findingsFile, finding.Report{Sources: sources(agent("solo", "bugs"))})

		err := newRoundReader(dir).read()
		require.Error(t, err)
		assert.Contains(t, err.Error(), foundFile)
	})

	t.Run("an unreadable snapshot is refused rather than read as an absent one", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a file with no permissions")
		}
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{Sources: sources(agent("solo", "bugs"))})
		unreadable(t, filepath.Join(dir, foundFile))

		err := newRoundReader(dir).read()
		require.Error(t, err)
		assert.Contains(t, err.Error(), foundFile)
	})

	t.Run("a malformed snapshot is refused rather than half-counted", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs"),
		})
		require.NoError(t, os.WriteFile(filepath.Join(dir, verifiedFile), []byte(`{"findings":`), 0o600))

		err := newRoundReader(dir).read()
		require.Error(t, err)
		assert.Contains(t, err.Error(), verifiedFile)
	})
}

func TestRoundReader_ambiguousLenses(t *testing.T) {
	tests := []struct {
		name      string
		lensSet   []string
		named     []string
		ambiguous bool
	}{
		{name: "the agent's whole set says only that the agent raised it", lensSet: []string{"bugs", "impl"},
			named: []string{"bugs", "impl"}, ambiguous: true},
		{name: "the whole set in another order is the same set", lensSet: []string{"bugs", "impl"},
			named: []string{"impl", "bugs"}, ambiguous: true},
		{name: "one lens of two is attributable", lensSet: []string{"bugs", "impl"},
			named: []string{"impl"}, ambiguous: false},
		{name: "two lenses of three are attributable", lensSet: []string{"bugs", "impl", "docs"},
			named: []string{"bugs", "docs"}, ambiguous: false},
		{name: "a single-lens agent can never be ambiguous", lensSet: []string{"adversarial"},
			named: []string{"adversarial"}, ambiguous: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeArtifact(t, dir, foundFile, finding.Report{
				Sources:  sources(agent("one", tt.lensSet...)),
				Findings: []finding.Finding{{Sources: []string{"one"}, Lenses: tt.named}},
			})

			r := newRoundReader(dir)
			require.NoError(t, r.read())
			want := 0
			if tt.ambiguous {
				want = 1
			}
			checked := 0
			for _, l := range r.stats().lenses {
				if !slices.Contains(tt.named, l.Name) {
					continue
				}
				assert.Equal(t, want, l.Ambiguous, "lens %s", l.Name)
				checked++
			}
			assert.Len(t, tt.named, checked, "every named lens must be asserted on, or the case checks nothing")
		})
	}
}

// the synthesis stage retries under the same event kind, naming itself as the agent. Counted blind it
// becomes a source no roster contains, reported beside the agents that really ran.
func TestRoundReader_readEvents(t *testing.T) {
	t.Run("retries are counted per agent", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("bugs+impl", "bugs"), agent("codex", "adversarial")),
		})
		writeEvents(t, dir,
			`{"kind":"stage","stage":"find"}`,
			`{"kind":"agent_retried","agent":"bugs+impl","text":"stalled"}`,
			`{"kind":"agent_activity","agent":"codex","text":"agent_retried"}`,
			`{"kind":"agent_retried","agent":"codex","text":"rate limited"}`,
			`{"kind":"agent_retried","agent":"bugs+impl","text":"stalled again"}`,
			`{"kind":"agent_done","agent":"codex"}`,
		)

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		assert.Equal(t, []agentStats{{Name: "bugs+impl", Retries: 2}, {Name: "codex", Retries: 1}}, r.stats().agents,
			"the kind decides, not a text that happens to name one")
	})

	t.Run("a stage retry is not an agent", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("bugs+impl", "bugs")), Findings: raised(1, "bugs+impl", "bugs"),
		})
		writeEvents(t, dir,
			`{"kind":"agent_retried","agent":"synthesis","text":"529"}`,
			`{"kind":"agent_retried","agent":"bugs+impl","text":"stalled"}`,
			`{"kind":"agent_retried","text":"no agent at all"}`,
		)

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		assert.Equal(t, []agentStats{{Name: "bugs+impl", Raised: 1, Survived: 1, Retries: 1}}, r.stats().agents,
			"only a name this round tallied is an agent, and no stage is one")
	})

	t.Run("a round with no events.jsonl has no retries", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{
			Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs"),
		})

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		assert.Equal(t, []agentStats{{Name: "solo", Raised: 1, Survived: 1}}, r.stats().agents)
	})

	t.Run("a last line an interrupted run never finished is skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{Sources: sources(agent("solo", "bugs"))})
		require.NoError(t, os.WriteFile(filepath.Join(dir, eventsFile),
			[]byte("{\"kind\":\"agent_retried\",\"agent\":\"solo\"}\n{\"kind\":\"agent_ret"), 0o600))

		r := newRoundReader(dir)
		require.NoError(t, r.read(), "a run killed mid-write leaves exactly this")
		assert.Equal(t, []agentStats{{Name: "solo", Retries: 1}}, r.stats().agents)
	})

	t.Run("an unreadable events.jsonl is refused rather than counted as none", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a file with no permissions")
		}
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{Sources: sources(agent("solo", "bugs"))})
		writeEvents(t, dir, `{"kind":"agent_retried","agent":"solo"}`)
		unreadable(t, filepath.Join(dir, eventsFile))

		err := newRoundReader(dir).read()
		require.Error(t, err)
		assert.Contains(t, err.Error(), eventsFile)
	})

	t.Run("an event line larger than a scan buffer is still read", func(t *testing.T) {
		dir := t.TempDir()
		writeArtifact(t, dir, foundFile, finding.Report{Sources: sources(agent("solo", "bugs"))})
		big := `{"kind":"findings","agent":"solo","text":"` + fill(200_000) + `"}`
		writeEvents(t, dir, big, `{"kind":"agent_retried","agent":"solo"}`)

		r := newRoundReader(dir)
		require.NoError(t, r.read())
		assert.Equal(t, 1, r.stats().agents[0].Retries, "a findings event carries every finding's body")
	})
}

func TestCollectStats(t *testing.T) {
	// two tasks sharing one agent and one lens, so the fold has something to match by name and something to
	// append: alpha runs bugs+impl beside codex over two rounds, beta runs docs+tests beside codex over one
	root := t.TempDir()
	alpha := sources(agent("bugs+impl", "bugs", "impl"), agent("codex", "adversarial"))
	beta := sources(agent("docs+tests", "docs", "tests"), agent("codex", "adversarial"))

	recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
		foundFile: {Sources: alpha, Findings: slices.Concat(
			raised(3, "bugs+impl", "bugs"), raised(2, "codex", "adversarial"))},
		verifiedFile: {Sources: alpha, Findings: []finding.Finding{
			merged(finding.Confirmed, []string{"bugs+impl", "codex"}, "bugs", "adversarial")}},
	})
	recordRound(t, filepath.Join(root, "alpha", "02-followup"), map[string]finding.Report{
		foundFile: {Sources: alpha, Findings: slices.Concat(
			raised(1, "bugs+impl", "impl"), raised(1, "codex", "adversarial"))},
		verifiedFile: {Sources: alpha, Findings: []finding.Finding{
			merged(finding.Refined, []string{"codex"}, "adversarial")}},
	})
	recordRound(t, filepath.Join(root, "beta", "01-initial"), map[string]finding.Report{
		foundFile: {Sources: beta, Findings: slices.Concat(
			raised(2, "docs+tests", "docs"), raised(1, "codex", "adversarial"))},
		verifiedFile: {Sources: beta, Findings: []finding.Finding{
			merged(finding.Confirmed, []string{"docs+tests"}, "docs"),
			merged(finding.Refined, []string{"codex"}, "adversarial")}},
	})

	got, err := CollectStats(StatsQuery{TasksDir: root})
	require.NoError(t, err)

	t.Run("every task under the root is reported, in the order task.List names them", func(t *testing.T) {
		assert.Equal(t, []string{"alpha", "beta"}, ids(got))
	})

	t.Run("a task's rounds fold together", func(t *testing.T) {
		assert.Equal(t, 2, got.Tasks[0].Rounds)
		assert.Equal(t, []agentStats{
			{Name: "bugs+impl", Raised: 4, Survived: 1, Corroborated: 1},
			{Name: "codex", Raised: 3, Survived: 2, Corroborated: 1},
		}, got.Tasks[0].Agents)
		assert.Equal(t, []lensStats{
			{Name: "bugs", Raised: 3, Verdicts: map[finding.Verdict]int{finding.Confirmed: 1}},
			{Name: "impl", Raised: 1, Verdicts: map[finding.Verdict]int{}},
			{Name: "adversarial", Raised: 3, Verdicts: map[finding.Verdict]int{
				finding.Confirmed: 1, finding.Refined: 1}},
		}, got.Tasks[0].Lenses, "a lens the roster carries but nothing raised is still reported, at zero")
		assert.Equal(t, []stageFlow{{Name: "verify", In: 7, Out: 2, Refined: 1}}, got.Tasks[0].Stages,
			"the refinement folds across the task's rounds, which is where revmux stats reads it")
	})

	t.Run("a task keeps its own roster", func(t *testing.T) {
		assert.Equal(t, 1, got.Tasks[1].Rounds)
		assert.Equal(t, []agentStats{
			{Name: "docs+tests", Raised: 2, Survived: 1},
			{Name: "codex", Raised: 1, Survived: 1},
		}, got.Tasks[1].Agents)
	})

	t.Run("totals fold exact bytes, not the rounded megabytes each task reports", func(t *testing.T) {
		assert.Equal(t, got.Tasks[0].sizeBytes+got.Tasks[1].sizeBytes, got.Totals.sizeBytes)
		assert.InDelta(t, mb(got.Totals.sizeBytes), got.Totals.SizeMB, 0.001)
		assert.Positive(t, got.Tasks[0].sizeBytes, "a task that recorded rounds occupies disk")
	})

	t.Run("totals match names across tasks and append the ones only one task ran", func(t *testing.T) {
		totals := got.Totals
		// size is asserted above against the tasks it folded; zeroing it here keeps this an exhaustive
		// comparison of everything else rather than one pinned to the byte count of the fixtures
		totals.sizeBytes, totals.SizeMB = 0, 0
		assert.Equal(t, taskStats{
			Rounds: 3, Skipped: []string{},
			Agents: []agentStats{
				{Name: "bugs+impl", Raised: 4, Survived: 1, Corroborated: 1},
				{Name: "codex", Raised: 4, Survived: 3, Corroborated: 1},
				{Name: "docs+tests", Raised: 2, Survived: 1},
			},
			Lenses: []lensStats{
				{Name: "bugs", Raised: 3, Verdicts: map[finding.Verdict]int{finding.Confirmed: 1}},
				{Name: "impl", Raised: 1, Verdicts: map[finding.Verdict]int{}},
				{Name: "adversarial", Raised: 4, Verdicts: map[finding.Verdict]int{
					finding.Confirmed: 1, finding.Refined: 2}},
				{Name: "docs", Raised: 2, Verdicts: map[finding.Verdict]int{finding.Confirmed: 1}},
				{Name: "tests", Verdicts: map[finding.Verdict]int{}},
			},
			Stages: []stageFlow{{Name: "verify", In: 10, Out: 4, Refined: 2}},
		}, totals, "the totals carry no id: they are every task at once")
	})

	t.Run("a task's verdict counts are its own after the totals folded them in", func(t *testing.T) {
		assert.Equal(t, map[finding.Verdict]int{finding.Refined: 1}, got.Tasks[1].Lenses[2].Verdicts,
			"beta's adversarial map is not the one the totals accumulate into")
	})
}

func TestCollectStats_query(t *testing.T) {
	t.Run("a named task narrows the corpus to it", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs")},
		})
		recordRound(t, filepath.Join(root, "beta", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("other", "docs")), Findings: raised(5, "other", "docs")},
		})

		got, err := CollectStats(StatsQuery{TasksDir: root, Task: "beta"})
		require.NoError(t, err)
		assert.Equal(t, []string{"beta"}, ids(got))
		assert.Equal(t, []agentStats{{Name: "other", Raised: 5, Survived: 5}}, got.Totals.Agents)
	})

	t.Run("a task the root does not carry is named rather than answered empty", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs"))},
		})

		_, err := CollectStats(StatsQuery{TasksDir: root, Task: "pr123"})
		require.Error(t, err, "a typo'd id answered with an empty document reads as a task with no history")
		assert.Contains(t, err.Error(), "pr123")
	})

	t.Run("a name that is not one task directory matches nothing", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs"))},
		})

		for _, name := range []string{"../" + filepath.Base(root), "pr-123/01-initial", ".."} {
			_, err := CollectStats(StatsQuery{TasksDir: root, Task: name})
			require.Error(t, err, "task %s", name)
		}
	})
}

func TestCollectStats_roundGating(t *testing.T) {
	t.Run("a round nobody ran is not counted", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(3, "solo", "bugs")},
		})
		// a round `revmux new` scaffolded and nobody has run yet: input/ and no marker at all
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-123", "02-prepared", inputDir), 0o750))
		// a round claimed by a run that never came back: the marker is there and still empty
		claimed := filepath.Join(root, "pr-123", "03-claimed")
		recordRound(t, claimed, map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(9, "solo", "bugs")},
		})
		require.NoError(t, os.WriteFile(filepath.Join(claimed, manifestFile), nil, 0o600))

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		assert.Equal(t, 1, got.Totals.Rounds)
		assert.Equal(t, []agentStats{{Name: "solo", Raised: 3, Survived: 3}}, got.Totals.Agents,
			"an open round's artifacts belong to a run that never finished")
	})

	t.Run("a round whose artifacts will not decode is left out and named", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs")},
		})
		broken := filepath.Join(root, "pr-123", "02-broken")
		recordRound(t, broken, map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs"))},
		})
		require.NoError(t, os.WriteFile(filepath.Join(broken, verifiedFile), []byte(`{"findings":`), 0o600))

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		assert.Equal(t, 1, got.Tasks[0].Rounds, "rounds counts the ones these numbers were read from")
		assert.Equal(t, []agentStats{{Name: "solo", Raised: 2, Survived: 2}}, got.Tasks[0].Agents)

		require.Len(t, got.Tasks[0].Skipped, 1, "a corpus that shrank silently reads as a smaller one")
		assert.Contains(t, got.Tasks[0].Skipped[0], verifiedFile, "the reason names the artifact that would not decode")
		assert.Equal(t, got.Tasks[0].Skipped, got.Totals.Skipped, "the totals carry it too, or the fold hides it")
	})

	t.Run("a task with no rounds is still reported, so both commands name the same tasks", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-123", "01-initial", inputDir), 0o750))

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		require.Equal(t, []string{"pr-123"}, ids(got))
		assert.Equal(t, newTaskStats("pr-123"), got.Tasks[0])
	})

	t.Run("an unreadable task directory is refused rather than reported as no rounds", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		root := t.TempDir()
		dir := filepath.Join(root, "pr-123")
		require.NoError(t, os.MkdirAll(dir, 0o750))
		unreadable(t, dir)

		_, err := CollectStats(StatsQuery{TasksDir: root})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pr-123")
	})
}

func TestCollectStats_tasksRoot(t *testing.T) {
	t.Run("a tasks root that is not there is a project that has never run a review", func(t *testing.T) {
		got, err := CollectStats(StatsQuery{TasksDir: filepath.Join(t.TempDir(), "never-created")})
		require.NoError(t, err)
		assert.Equal(t, Corpus{Tasks: []taskStats{}, Totals: newTaskStats("")}, got)

		data, marshalErr := json.Marshal(got)
		require.NoError(t, marshalErr)
		assert.Contains(t, string(data), `"tasks":[]`, "an empty corpus is a document, not a null")
	})

	t.Run("an unreadable tasks root is refused rather than reported as no tasks", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		root := t.TempDir()
		unreadable(t, root)

		_, err := CollectStats(StatsQuery{TasksDir: root})
		require.Error(t, err, "an empty corpus over a root nobody could read is how a caller mints a duplicate id")
		assert.Contains(t, err.Error(), root)
	})

	t.Run("an empty query reads no location of its own", func(t *testing.T) {
		got, err := CollectStats(StatsQuery{})
		require.NoError(t, err)
		assert.Empty(t, got.Tasks, "there is no default tasks root here, so the developer's own is never read")
	})

	t.Run("the corpus names only what the query's root carries", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "only-task", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		assert.Equal(t, []string{"only-task"}, ids(got))
	})
}

func TestTaskStats_add(t *testing.T) {
	t.Run("an empty accumulator takes the other side whole", func(t *testing.T) {
		got := newTaskStats("pr-123")
		got.add(taskStats{Rounds: 1, Skipped: []string{"02-broken: decode stages/3-verified.json"},
			Agents: []agentStats{{Name: "solo", Raised: 2, Survived: 1, Corroborated: 1,
				DegradedRounds: 1, Retries: 3, Tokens: 100}},
			Lenses: []lensStats{{Name: "bugs", Raised: 2, Ambiguous: 1,
				Verdicts: map[finding.Verdict]int{finding.Confirmed: 1}}},
			Stages: []stageFlow{{Name: "verify", In: 2, Out: 1}},
		})

		assert.Equal(t, taskStats{ID: "pr-123", Rounds: 1,
			Skipped: []string{"02-broken: decode stages/3-verified.json"},
			Agents: []agentStats{{Name: "solo", Raised: 2, Survived: 1, Corroborated: 1,
				DegradedRounds: 1, Retries: 3, Tokens: 100}},
			Lenses: []lensStats{{Name: "bugs", Raised: 2, Ambiguous: 1,
				Verdicts: map[finding.Verdict]int{finding.Confirmed: 1}}},
			Stages: []stageFlow{{Name: "verify", In: 2, Out: 1}},
		}, got, "the id is the accumulator's own and no argument order can decide it")
	})

	t.Run("every counter accumulates by name", func(t *testing.T) {
		got := newTaskStats("")
		one := taskStats{Rounds: 1,
			Agents: []agentStats{{Name: "solo", Raised: 2, Survived: 1, Corroborated: 1, Retries: 1, Tokens: 10}},
			Lenses: []lensStats{{Name: "bugs", Raised: 2, Ambiguous: 1,
				Verdicts: map[finding.Verdict]int{finding.Confirmed: 1}}},
			Stages: []stageFlow{{Name: "verify", In: 2, Out: 1}},
		}
		two := taskStats{Rounds: 1,
			Agents: []agentStats{{Name: "solo", Raised: 3, Survived: 2, DegradedRounds: 1, Tokens: 5}},
			Lenses: []lensStats{{Name: "bugs", Raised: 3,
				Verdicts: map[finding.Verdict]int{finding.Confirmed: 2, finding.Rejected: 1}}},
			Stages: []stageFlow{{Name: "verify", In: 3, Out: 2}},
		}
		got.add(one)
		got.add(two)

		assert.Equal(t, 2, got.Rounds)
		assert.Equal(t, []agentStats{{Name: "solo", Raised: 5, Survived: 3, Corroborated: 1,
			DegradedRounds: 1, Retries: 1, Tokens: 15}}, got.Agents)
		assert.Equal(t, []lensStats{{Name: "bugs", Raised: 5, Ambiguous: 1,
			Verdicts: map[finding.Verdict]int{finding.Confirmed: 3, finding.Rejected: 1}}}, got.Lenses)
		assert.Equal(t, []stageFlow{{Name: "verify", In: 5, Out: 3}}, got.Stages)
	})

	t.Run("a folded tally is never aliased by the accumulator", func(t *testing.T) {
		one := taskStats{Rounds: 1, Lenses: []lensStats{{Name: "bugs",
			Verdicts: map[finding.Verdict]int{finding.Confirmed: 1}}}}
		got := newTaskStats("")
		got.add(one)
		got.add(taskStats{Rounds: 1, Lenses: []lensStats{{Name: "bugs",
			Verdicts: map[finding.Verdict]int{finding.Confirmed: 4}}}})

		assert.Equal(t, map[finding.Verdict]int{finding.Confirmed: 1}, one.Lenses[0].Verdicts,
			"the source of a fold is read, never written into")
		assert.Equal(t, map[finding.Verdict]int{finding.Confirmed: 5}, got.Lenses[0].Verdicts)
	})
}

// ids is the task ids a corpus reports, in the order it reports them.
func ids(c Corpus) []string {
	out := make([]string, 0, len(c.Tasks))
	for _, t := range c.Tasks {
		out = append(out, t.ID)
	}
	return out
}

// recordRound lays out one round that ran: the findings artifacts it carries plus the non-empty
// manifest.json task.Rounds gates on.
func recordRound(t *testing.T, dir string, reps map[string]finding.Report) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	for name, rep := range reps {
		writeArtifact(t, dir, name, rep)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile), []byte(`{"run":"recorded"}`), 0o600))
}

// fill is n bytes of filler for an oversized event line.
func fill(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// writeArtifact stores one findings artifact the way a run leaves it, creating the stages/ directory the
// snapshots live in.
func writeArtifact(t *testing.T, dir, name string, rep finding.Report) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	f, err := os.Create(path) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	require.NoError(t, rep.JSON(f))
	require.NoError(t, f.Close())
}

// unreadable strips an artifact of every permission, restoring them afterwards so the temp directory can
// still be removed. A directory gets its execute bit back too, or cleanup cannot walk into it.
func unreadable(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	restore := os.FileMode(0o600)
	if fi.IsDir() {
		restore = 0o700
	}
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, restore) })
}

// writeEvents stores the run's decision record, one JSON object per line.
func writeEvents(t *testing.T, dir string, lines ...string) {
	t.Helper()
	out := strings.Join(lines, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, eventsFile), []byte(out), 0o600))
}

// agent is one roster entry as a stage snapshot records it.
func agent(name string, lenses ...string) finding.SourceStat {
	return finding.SourceStat{Name: name, Lenses: lenses, Executor: "claude"}
}

func sources(agents ...finding.SourceStat) finding.SourceStatus {
	return finding.SourceStatus{Expected: len(agents), Reported: len(agents), Agents: agents}
}

// raised is n stage-1 findings from one agent under one lens combination, which is the only shape stage 1
// carries: find stamps sources with the executing agent alone.
func raised(n int, source string, lenses ...string) []finding.Finding {
	out := make([]finding.Finding, n)
	for i := range out {
		out[i] = finding.Finding{
			ID:      source + "-" + strconv.Itoa(i+1),
			Sources: []string{source},
			Lenses:  lenses,
		}
	}
	return out
}

func roster() finding.SourceStatus {
	return finding.SourceStatus{
		Expected: 4, Reported: 4,
		Agents: []finding.SourceStat{
			{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}, Executor: "claude", Tokens: 4986379},
			{Name: "arch+quality", Lenses: []string{"architecture", "quality"}, Executor: "claude", Tokens: 3644082},
			{Name: "docs+tests", Lenses: []string{"docs", "tests"}, Executor: "claude", Tokens: 6374878},
			{Name: "codex", Lenses: []string{"adversarial"}, Executor: "codex", Tokens: 168467},
		},
	}
}

// stageOne is the 26 findings the four agents raised, in the lens distribution a real round produced. The
// ids are renumbered across the whole slice because raised numbers within its own call: two calls naming
// one agent would otherwise repeat ids, and this fixture is the shape a real stage 1 leaves behind.
func stageOne() []finding.Finding {
	out := slices.Concat(
		raised(2, "bugs+impl", "bugs", "impl"),
		raised(2, "bugs+impl", "impl"),
		raised(1, "bugs+impl", "bugs"),
		raised(1, "arch+quality", "quality"),
		raised(1, "arch+quality", "architecture", "quality"),
		raised(2, "arch+quality", "architecture"),
		raised(1, "docs+tests", "docs", "tests"),
		raised(5, "docs+tests", "docs"),
		raised(1, "docs+tests", "tests"),
		raised(10, "codex", "adversarial"),
	)
	for i := range out {
		out[i].ID = out[i].Sources[0] + "-" + strconv.Itoa(i+1)
	}
	return out
}

// verifiedFindings is what verification left, each carrying the union of sources and lenses synthesis
// derived for it.
func verifiedFindings() []finding.Finding {
	return []finding.Finding{
		merged(finding.Refined, []string{"docs+tests", "codex"}, "docs", "adversarial"),
		merged(finding.Confirmed, []string{"codex"}, "adversarial"),
		merged(finding.Refined, []string{"codex"}, "adversarial"),
		merged(finding.Refined, []string{"bugs+impl", "arch+quality", "docs+tests", "codex"},
			"impl", "architecture", "docs", "adversarial"),
		merged(finding.Confirmed, []string{"docs+tests"}, "docs"),
		merged(finding.Confirmed, []string{"bugs+impl", "arch+quality", "docs+tests", "codex"},
			"bugs", "impl", "quality", "docs", "tests", "adversarial"),
		merged(finding.Confirmed, []string{"docs+tests"}, "tests"),
		merged(finding.Refined, []string{"bugs+impl", "arch+quality"}, "bugs", "architecture", "quality"),
		merged(finding.Confirmed, []string{"bugs+impl", "codex"}, "impl", "adversarial"),
	}
}

// preExisting is the eight issues verification routed out of the findings list. Seven carry no verdict at
// all — synthesis split them off before the stage ran — and one was reclassified by it.
func preExisting() []finding.Finding {
	return []finding.Finding{
		merged("", []string{"codex"}, "adversarial"),
		merged("", []string{"bugs+impl"}, "bugs", "impl"),
		merged("", []string{"docs+tests"}, "docs"),
		merged("", []string{"codex"}, "adversarial"),
		merged("", []string{"codex"}, "adversarial"),
		merged("", []string{"docs+tests"}, "docs"),
		merged("", []string{"arch+quality"}, "architecture"),
		merged(finding.PreExisting, []string{"codex"}, "adversarial"),
	}
}

func merged(verdict finding.Verdict, srcs []string, lenses ...string) finding.Finding {
	return finding.Finding{Sources: srcs, Lenses: lenses, Verdict: verdict}
}

func TestCollectStats_disk(t *testing.T) {
	t.Run("a task carries its size, its task.md description and the date of its last round", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "alpha")
		recordRound(t, filepath.Join(dir, "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})
		require.NoError(t, os.WriteFile(filepath.Join(dir, "01-initial", manifestFile),
			[]byte(`{"run":"01-initial","finished_at":"2026-07-27T10:00:00Z"}`), 0o600))
		recordRound(t, filepath.Join(dir, "02-after-fix"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})
		require.NoError(t, os.WriteFile(filepath.Join(dir, "02-after-fix", manifestFile),
			[]byte(`{"run":"02-after-fix","finished_at":"2026-07-31T09:00:00Z"}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "task.md"),
			[]byte("---\ndescription: the round-layout rework\n---\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "01-initial", "bulk"), make([]byte, 2<<20), 0o600))

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		require.Len(t, got.Tasks, 1)
		assert.Equal(t, "the round-layout rework", got.Tasks[0].Description)
		assert.InDelta(t, 2.0, got.Tasks[0].SizeMB, 0.1, "the artifacts plus the 2MB file")
		assert.Equal(t, "2026-07-31", got.Tasks[0].LastRun, "the latest round dates the task")
		assert.Equal(t, "2026-07-31", got.Totals.LastRun)
	})

	t.Run("a task with no task.md reports no description rather than failing", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		assert.Empty(t, got.Tasks[0].Description)
	})

	t.Run("a task.md that will not parse leaves the description empty, and revmux config names why", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "alpha")
		recordRound(t, filepath.Join(dir, "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})
		require.NoError(t, os.WriteFile(filepath.Join(dir, "task.md"), []byte("---\ntitel: typo\n---\n"), 0o600))

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		assert.Empty(t, got.Tasks[0].Description)
		assert.Equal(t, 1, got.Tasks[0].Rounds, "the corpus is still reported")
	})

	t.Run("a round whose manifest carries no finished_at does not date the task", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err)
		assert.Empty(t, got.Tasks[0].LastRun)
		assert.Positive(t, got.Tasks[0].SizeMB+float64(got.Tasks[0].sizeBytes), "it still has a size")
	})
}

// the regression this pins: dirSize walking the whole tree made one unreadable directory under one task
// fatal for the corpus, so `revmux stats` reported nothing about the other tasks at all.
func TestCollectStats_unreadableEntry(t *testing.T) {
	t.Run("one unreadable directory does not discard every other task's numbers", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root traverses a 0000 directory regardless of its mode")
		}
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs")},
		})
		recordRound(t, filepath.Join(root, "beta", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})
		blocked := filepath.Join(root, "alpha", "01-initial", "agents")
		require.NoError(t, os.MkdirAll(blocked, 0o700))
		require.NoError(t, os.Chmod(blocked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) }) //nolint:gosec // G302 is about files, not dirs

		got, err := CollectStats(StatsQuery{TasksDir: root})
		require.NoError(t, err, "the corpus still prints")
		require.Len(t, got.Tasks, 2)
		assert.Equal(t, []string{"alpha", "beta"}, ids(got))
		assert.Equal(t, 1, got.Tasks[1].Rounds, "beta's numbers survive alpha's unreadable entry")

		// and the shortfall is named rather than left to read as alpha's real cost
		require.NotEmpty(t, got.Tasks[0].Skipped)
		assert.Contains(t, strings.Join(got.Tasks[0].Skipped, " "), "unreadable")
	})
}

// the fold is what `revmux stats` prints: it reports the task entry and the totals, never one round's own
// tally, so a field computed per round and not folded here is zero everywhere it is read.
func TestTaskStats_addStages(t *testing.T) {
	t.Run("every field folds, not only in and out", func(t *testing.T) {
		var got taskStats
		got.addStages([]stageFlow{{Name: "verify", In: 10, Out: 9, Reclassified: 2, Refined: 4}})
		got.addStages([]stageFlow{{Name: "verify", In: 6, Out: 6, Reclassified: 1, Refined: 3}})

		assert.Equal(t, []stageFlow{{Name: "verify", In: 16, Out: 15, Reclassified: 3, Refined: 7}},
			got.Stages)
	})

	t.Run("a stage the tally has not seen is appended whole", func(t *testing.T) {
		var got taskStats
		got.addStages([]stageFlow{{Name: "synthesis", In: 20, Out: 12, Reclassified: 5}})
		got.addStages([]stageFlow{{Name: "verify", In: 12, Out: 12, Refined: 3}})

		assert.Equal(t, []stageFlow{
			{Name: "synthesis", In: 20, Out: 12, Reclassified: 5},
			{Name: "verify", In: 12, Out: 12, Refined: 3},
		}, got.Stages)
	})
}
