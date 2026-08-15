package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/task"
)

// the reason no --task is declared on the subcommand: a second one would make the two invocations below
// fill different fields, and the one the caller passed would be the one nothing read.
func TestParseArgs_statsSubcommand(t *testing.T) {
	t.Run("the command word selects the corpus reading", func(t *testing.T) {
		isolate(t)
		o, err := parseArgs([]string{"stats"})
		require.NoError(t, err)
		assert.True(t, o.showStats)
		assert.False(t, o.showNew)
		assert.False(t, o.showInit)
		assert.False(t, o.showConfig)
	})

	t.Run("a review still parses with no command word", func(t *testing.T) {
		isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--run", "01-initial"})
		require.NoError(t, err)
		assert.False(t, o.showStats)
	})

	t.Run("--task lands in one field whichever side of the command word it is on", func(t *testing.T) {
		isolate(t)
		before, err := parseArgs([]string{"--task", "pr-1", "stats"})
		require.NoError(t, err)
		after, err := parseArgs([]string{"stats", "--task", "pr-1"})
		require.NoError(t, err)

		assert.Equal(t, "pr-1", before.Task)
		assert.Equal(t, "pr-1", after.Task)
		assert.True(t, before.showStats)
		assert.True(t, after.showStats)
	})

	t.Run("nothing is written during parsing", func(t *testing.T) {
		dir := isolate(t)
		_, err := parseArgs([]string{"--tasks-dir", filepath.Join(dir, "tasks"), "stats"})
		require.NoError(t, err)
		assert.Equal(t, []string{"."}, treeOf(t, dir), "Execute records the selection, run does the reading")
	})
}

// ambiguous and the verdict map are the two per-lens numbers a suggestion has to quote, so they have to
// survive the trip through the payload rather than only through the archive types.
func TestRun_stats(t *testing.T) {
	corpusOf := func(t *testing.T, o options) (statsDoc, *runHarness) {
		t.Helper()
		o.showStats = true
		r := newRunOpts(t, o)
		require.Equal(t, 0, run(r.opts()))
		assert.Empty(t, r.stderr.String(), "the corpus is the whole output")

		var doc statsDoc
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &doc), "the caller model parses this")
		return doc, r
	}

	t.Run("every task under the root is reported on stdout", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			task.FoundFile:    {Sources: roster("bugs+impl", "bugs"), Findings: raised(3, "bugs+impl", "bugs")},
			task.VerifiedFile: {Sources: roster("bugs+impl", "bugs"), Findings: raised(1, "bugs+impl", "bugs")},
		})
		recordRound(t, filepath.Join(root, "beta", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("codex", "adversarial"), Findings: raised(5, "codex", "adversarial")},
		})

		doc, r := corpusOf(t, options{TasksDir: root})
		assert.Contains(t, r.stdout.String(), "\n  \"tasks\"", "indented, since a human reads it occasionally")
		assert.Equal(t, []string{"alpha", "beta"}, taskIDs(doc))
		assert.Equal(t, 2, doc.Totals.Rounds)
		assert.Equal(t, []statsAgent{{Name: "bugs+impl", Raised: 3, Survived: 1, Tokens: 100}},
			doc.Tasks[0].Agents, "survived comes from the stage snapshot, never from the filtered report")
		assert.Equal(t, []statsStage{{Name: "verify", In: 3, Out: 1}}, doc.Tasks[0].Stages)
		assert.Equal(t, []statsLens{{Name: "bugs", Raised: 3, Verdicts: map[string]int{"unverified": 1}}},
			doc.Tasks[0].Lenses)
		assert.Empty(t, doc.Tasks[0].Skipped, "every round here decodes, and the field is present rather than absent")
	})

	t.Run("per-lens ambiguity and verdicts reach stdout", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		pair := roster("bugs+impl", "bugs", "impl")
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			// naming the agent's whole lens set is what the find stage falls back to when the model named none
			task.FoundFile: {Sources: pair, Findings: raised(2, "bugs+impl", "bugs", "impl")},
			task.VerifiedFile: {Sources: pair, Findings: []finding.Finding{
				{ID: "bugs+impl-1", Sources: []string{"bugs+impl"}, Lenses: []string{"bugs"},
					Verdict: finding.Confirmed},
			}},
		})

		doc, _ := corpusOf(t, options{TasksDir: root})
		assert.Equal(t, []statsLens{
			{Name: "bugs", Raised: 2, Ambiguous: 2, Verdicts: map[string]int{"confirmed": 1}},
			{Name: "impl", Raised: 2, Ambiguous: 2, Verdicts: map[string]int{}},
		}, doc.Tasks[0].Lenses, "a per-lens number is only as good as its ambiguous share, so both are reported")
	})

	t.Run("--task narrows the corpus to one task", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("bugs+impl", "bugs"), Findings: raised(3, "bugs+impl", "bugs")},
		})
		recordRound(t, filepath.Join(root, "beta", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("codex", "adversarial"), Findings: raised(5, "codex", "adversarial")},
		})

		doc, _ := corpusOf(t, options{TasksDir: root, Task: "beta"})
		assert.Equal(t, []string{"beta"}, taskIDs(doc))
		assert.Equal(t, []statsAgent{{Name: "codex", Raised: 5, Survived: 5, Tokens: 100}}, doc.Totals.Agents)
	})

	t.Run("a tasks root nobody has reviewed under is an empty document, not an error", func(t *testing.T) {
		isolate(t)
		doc, r := corpusOf(t, options{TasksDir: filepath.Join(t.TempDir(), "never-created")})
		assert.Empty(t, doc.Tasks)
		assert.Contains(t, r.stdout.String(), `"tasks": []`, "a null would make every caller special-case it")
		assert.Equal(t, 0, doc.Totals.Rounds)
	})

	t.Run("retries come from the event log", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		dir := filepath.Join(root, "pr-123", "01-initial")
		recordRound(t, dir, map[string]finding.Report{
			task.FoundFile: {Sources: roster("codex", "adversarial"), Findings: raised(2, "codex", "adversarial")},
		})
		require.NoError(t, os.WriteFile(filepath.Join(dir, task.EventsFile),
			[]byte("{\"kind\":\"agent_retried\",\"agent\":\"codex\"}\n"), 0o600))

		doc, _ := corpusOf(t, options{TasksDir: root})
		assert.Equal(t, 1, doc.Totals.Agents[0].Retries, "no stage snapshot records a relaunch")
	})

	t.Run("a task the root does not carry exits 2", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("codex", "adversarial")},
		})

		r := newRunOpts(t, options{TasksDir: root, Task: "pr123", showStats: true})
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String(), "half a payload would parse as a complete one")
		assert.Contains(t, r.stderr.String(), "pr123", "a typo'd id answered with zeros reads as a task with no history")
	})

	t.Run("an unwritable stdout exits 2", func(t *testing.T) {
		isolate(t)
		r := newRunOpts(t, options{TasksDir: t.TempDir(), showStats: true})
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write stats")
	})
}

// statsDoc is the slice of the stats payload these tests assert on. app/archive keeps everything under
// Corpus unexported, so a consumer reads the JSON rather than the Go shape — which is what a caller model
// does too.
type statsDoc struct {
	Tasks  []statsTask `json:"tasks"`
	Totals statsTask   `json:"totals"`
}

type statsTask struct {
	ID          string       `json:"id"`
	Description string       `json:"description"`
	SizeMB      float64      `json:"size_mb"`
	LastRun     string       `json:"last_run"`
	Rounds      int          `json:"rounds"`
	Skipped     []string     `json:"skipped"`
	Agents      []statsAgent `json:"agents"`
	Lenses      []statsLens  `json:"lenses"`
	Stages      []statsStage `json:"stages"`
}

type statsAgent struct {
	Name     string `json:"name"`
	Raised   int    `json:"raised"`
	Survived int    `json:"survived"`
	Retries  int    `json:"retries"`
	Tokens   int    `json:"tokens"`
}

type statsLens struct {
	Name      string         `json:"name"`
	Raised    int            `json:"raised"`
	Ambiguous int            `json:"ambiguous"`
	Verdicts  map[string]int `json:"verdicts"`
}

type statsStage struct {
	Name string `json:"name"`
	In   int    `json:"in"`
	Out  int    `json:"out"`
}

func taskIDs(doc statsDoc) []string {
	out := make([]string, 0, len(doc.Tasks))
	for _, tk := range doc.Tasks {
		out = append(out, tk.ID)
	}
	return out
}

// recordRound lays out one round that ran: the stage snapshots the numbers are read from, the input/ the
// caller wrote beside them, and the non-empty manifest.json task.Rounds gates on. dir is always under a
// t.TempDir, never the real ./.revmux/tasks.
func recordRound(t *testing.T, dir string, reps map[string]finding.Report) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, task.InputDir), 0o750))
	for name, rep := range reps {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		f, err := os.Create(path) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.NoError(t, rep.JSON(f))
		require.NoError(t, f.Close())
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, task.ManifestFile), []byte(`{"run":"recorded"}`), 0o600))
}

// roster is a one-agent source list as a stage snapshot records it.
func roster(name string, lenses ...string) finding.SourceStatus {
	return finding.SourceStatus{
		Expected: 1, Reported: 1,
		Agents: []finding.SourceStat{{Name: name, Lenses: lenses, Executor: "claude", Tokens: 100}},
	}
}

// raised is n findings from one agent under one lens combination, the shape the find stage stamps.
func raised(n int, source string, lenses ...string) []finding.Finding {
	out := make([]finding.Finding, n)
	for i := range out {
		out[i] = finding.Finding{ID: source + "-" + strconv.Itoa(i+1), Sources: []string{source}, Lenses: lenses}
	}
	return out
}
