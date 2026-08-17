package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/archive"
	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	pmocks "github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

func TestRun_projectProfileSnapshot(t *testing.T) {
	const body = "# how this project is calibrated\n"

	project := func(t *testing.T) {
		t.Helper()
		t.Chdir(t.TempDir())
		cwd, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(cwd, projectDirName), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(cwd, projectDirName, task.ProfileFile),
			[]byte(body), 0o600))
	}

	t.Run("the round holds the bytes and the agents were pointed at them", func(t *testing.T) {
		project(t)
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		dir := filepath.Join(root, "pr-1", "round-1")
		snapshot := filepath.Join(dir, task.ProfileSnapshotFile)
		require.FileExists(t, snapshot)
		assert.Equal(t, body, readFile(t, snapshot), "PROFILE expands to a path, so the archive keeps the bytes")
		assert.Contains(t, readFile(t, filepath.Join(dir, "prompts", "agents", "lenses.md")), snapshot,
			"the composed prompt names the round's snapshot, never the project file outside it")
		assert.NoFileExists(t, filepath.Join(dir, task.InputDir, task.ProfileFile),
			"input/ is the caller's and revmux writes nothing into it")
	})

	t.Run("a round carrying its own profile is snapshotted from nothing", func(t *testing.T) {
		project(t)
		r, root := archiveRun(t)
		input := filepath.Join(root, "pr-1", "round-1", task.InputDir)
		require.NoError(t, os.WriteFile(filepath.Join(input, task.ProfileFile), []byte("# this round only\n"), 0o600))
		require.Equal(t, 1, run(r.opts()))

		dir := filepath.Join(root, "pr-1", "round-1")
		assert.NoFileExists(t, filepath.Join(dir, task.ProfileSnapshotFile))
		assert.Contains(t, readFile(t, filepath.Join(dir, "prompts", "agents", "lenses.md")),
			filepath.Join(input, task.ProfileFile), "the round's own profile wins over the project one")
	})

	// the snapshot is the first thing this run writes into the round, so it is also what makes an
	// interrupted round unreclaimable. That is deliberate — two runs' artifacts under one manifest is
	// the un-auditable archive CheckReclaim exists to refuse — but it must be the real refusal
	t.Run("an interrupted round that got a snapshot is refused rather than re-used", func(t *testing.T) {
		project(t)
		r, root := archiveRun(t)
		ro := r.opts()

		review, err := ro.pipelineConfig()
		require.NoError(t, err)
		require.NoError(t, ro.materializeProfile(review.archive, review.context))
		require.NoError(t, review.archive.Close())

		_, err = archive.New(task.Round{TasksDir: root, Task: "pr-1", Run: "round-1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "prompts", "the refusal names what the dead run left behind")
	})

	t.Run("a round that already ran is refused before anything is written over it", func(t *testing.T) {
		project(t)
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		cwd, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(cwd, projectDirName, task.ProfileFile),
			[]byte("# rewritten after the round ran\n"), 0o600))

		second := newRunOpts(t, r.o)
		require.Equal(t, 2, run(second.opts()))
		assert.Equal(t, body, readFile(t, filepath.Join(root, "pr-1", "round-1", task.ProfileSnapshotFile)),
			"the claim refuses first, so the finished round keeps the calibration it actually ran under")
	})

	t.Run("a round-local profile writes no snapshot and stays reclaimable", func(t *testing.T) {
		project(t)
		r, root := archiveRun(t)
		input := filepath.Join(root, "pr-1", "round-1", task.InputDir)
		require.NoError(t, os.WriteFile(filepath.Join(input, task.ProfileFile), []byte("# this round\n"), 0o600))
		ro := r.opts()

		review, err := ro.pipelineConfig()
		require.NoError(t, err)
		require.NoError(t, ro.materializeProfile(review.archive, review.context))
		require.NoError(t, review.archive.Close())

		assert.NoFileExists(t, filepath.Join(root, "pr-1", "round-1", task.ProfileSnapshotFile))
		reclaimed, err := archive.New(task.Round{TasksDir: root, Task: "pr-1", Run: "round-1"})
		require.NoError(t, err, "the round holds only input/ and the marker, so it is still open")
		require.NoError(t, reclaimed.Close())
	})

	// the read finishes before Archive.Writer is called, so a source that vanishes costs the run and
	// not the round. Only a partial revmux-owned artifact makes a round unreclaimable
	t.Run("a source that cannot be read fails the run without burning the round", func(t *testing.T) {
		project(t)
		r, root := archiveRun(t)
		ro := r.opts()

		review, err := ro.pipelineConfig()
		require.NoError(t, err)
		require.NoError(t, os.Remove(review.context.ProfileSource))

		require.Error(t, ro.materializeProfile(review.archive, review.context))
		require.NoError(t, review.archive.Close())

		assert.NoFileExists(t, filepath.Join(root, "pr-1", "round-1", task.ProfileSnapshotFile))
		reclaimed, err := archive.New(task.Round{TasksDir: root, Task: "pr-1", Run: "round-1"})
		require.NoError(t, err, "nothing was written into the round, so it is still open")
		require.NoError(t, reclaimed.Close())
	})

	t.Run("a signal between the claim and the snapshot leaves the round reclaimable", func(t *testing.T) {
		project(t)
		r, root := archiveRun(t)
		ro := r.opts()

		review, err := ro.pipelineConfig()
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err = ro.review(ctx, review)
		require.Error(t, err)
		require.NoError(t, review.archive.Close())

		dir := filepath.Join(root, "pr-1", "round-1")
		assert.NoFileExists(t, filepath.Join(dir, task.ProfileSnapshotFile))
		assert.NoFileExists(t, filepath.Join(dir, task.EventsFile))
		reclaimed, err := archive.New(task.Round{TasksDir: root, Task: "pr-1", Run: "round-1"})
		require.NoError(t, err, "a round nothing reviewed must not be spent")
		require.NoError(t, reclaimed.Close())
	})
}

func TestRun_archive(t *testing.T) {
	t.Run("the run directory holds every artifact a later reader needs", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		dir := filepath.Join(root, "pr-1", "round-1")
		for _, name := range []string{
			"report.md", "findings.json", "manifest.json", "events.jsonl",
			filepath.Join("prompts", "agents", "lenses.md"),
			filepath.Join("stages", "1-found.json"),
			filepath.Join("agents", "lenses.jsonl"),
		} {
			assert.FileExists(t, filepath.Join(dir, name))
		}

		// stdout is JSON, so it is findings.json the caller was handed; report.md is the rendered form
		// kept for a human reading the archive afterwards
		machine, err := os.ReadFile(filepath.Join(dir, task.FindingsFile)) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, r.stdout.String(), string(machine), "the archived json is what the caller was shown")

		report, err := os.ReadFile(filepath.Join(dir, "report.md")) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Contains(t, string(report), "# Review:", "and the rendered form is archived beside it")

		var rep finding.Report
		data, err := os.ReadFile(filepath.Join(dir, "findings.json")) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &rep))
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, "unchecked error", rep.Findings[0].Title)
	})

	t.Run("nothing is written outside the round", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		entries, err := os.ReadDir(filepath.Join(root, "pr-1"))
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		slices.Sort(names)
		assert.Equal(t, []string{"round-1"}, names, "the task directory holds rounds, and revmux authors none of it")

		input := treeOf(t, filepath.Join(root, "pr-1", "round-1", task.InputDir))
		assert.Equal(t, []string{".", task.ScopeFile}, input, "the caller's own input is left exactly as it was")
	})

	t.Run("the manifest records what actually ran, not only what was asked for", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		var got manifest
		data, err := os.ReadFile(filepath.Join(root, "pr-1", "round-1", task.ManifestFile)) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &got))

		assert.Equal(t, "pr-1", got.Task)
		assert.Equal(t, "round-1", got.Run)
		assert.Equal(t, "focused", got.Profile)
		assert.Equal(t, filepath.Join(root, "pr-1", "round-1", task.InputDir, task.ScopeFile), got.ScopePath)

		require.Len(t, got.Agents, 1, "the --lenses override runs one agent")
		agent := got.Agents[0]
		assert.Equal(t, "lenses", agent.Name)
		assert.Equal(t, []string{"bugs"}, agent.Lenses)
		assert.Equal(t, "claude", agent.Executor)
		assert.Equal(t, "opus", agent.RequestedModel)
		assert.Equal(t, "claude-opus-5", agent.ActualModel, "--model can be silently ignored, so both are recorded")
		assert.Equal(t, "high", agent.Effort)
		assert.Equal(t, 4210, agent.Tokens)
		assert.False(t, agent.Degraded)

		require.NotEmpty(t, got.Stages)
		assert.Equal(t, "find", got.Stages[0].Name)
		assert.Empty(t, got.Stages[0].Executor, "find has one runner per roster entry, not one of its own")
		for _, st := range got.Stages[1:] {
			assert.NotEmpty(t, st.Executor,
				"%s: a profile can override the runner, so the round records which binary produced the stage", st.Name)
		}
		assert.Positive(t, got.Tokens)
		assert.False(t, got.StartedAt.IsZero())

		var lens *prompt.FileOrigin
		for i, o := range got.Prompts {
			if o.Path == "lenses/bugs.md" {
				lens = &got.Prompts[i]
			}
		}
		require.NotNil(t, lens, "provenance answers which lens text raised a finding")
		assert.Equal(t, prompt.LayerEmbedded, lens.Layer)
		assert.Len(t, lens.Hash, 64, "a content hash tells two rounds of one task apart")
	})

	t.Run("a degraded agent keeps its roster entry in the manifest", func(t *testing.T) {
		r, root := archiveRun(t)
		// the whole focused roster: a claude lens agent plus a codex peer. No stagger, since the mock
		// runner emits no activity and the fake clock fires no timer, so nothing would open the gate
		r.o.Lenses, r.o.StaggerDelay = nil, 0

		ro := r.opts()
		ro.newRunner = func(spec pipeline.RunnerSpec) pipeline.Runner {
			return &pmocks.RunnerMock{
				RunFunc: func(_ context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
					if spec.Executor == executorCodex {
						return executor.Result{}, errors.New("codex died")
					}
					return r.result, nil
				},
			}
		}
		require.Equal(t, 1, run(ro))

		var got manifest
		data, err := os.ReadFile(filepath.Join(root, "pr-1", "round-1", task.ManifestFile)) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &got))

		require.Len(t, got.Agents, 2, "a source that never delivered is still part of the roster that ran")
		codex := got.Agents[1]
		assert.Equal(t, "codex", codex.Name)
		assert.Equal(t, "gpt-5.6-sol", codex.RequestedModel)
		assert.Equal(t, "high", codex.Effort)
		assert.True(t, codex.Degraded)
		assert.Empty(t, codex.ActualModel, "nothing ran, so nothing is claimed to have run")
		assert.Equal(t, []string{"codex"}, got.Degraded)
	})

	t.Run("a run name that already exists fails without touching the round it names", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		report := filepath.Join(root, "pr-1", "round-1", "report.md")
		before, err := os.ReadFile(report) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)

		again, _ := archiveRun(t)
		again.o.TasksDir = root
		assert.Equal(t, 2, run(again.opts()))
		assert.Contains(t, again.stderr.String(), "has already run")
		assert.Empty(t, again.stdout.String(), "nothing but the report reaches stdout, and there is no report")

		after, err := os.ReadFile(report) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after))
	})

	t.Run("an archive that cannot be written exits 2 with no report", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		r, root := archiveRun(t)
		round := filepath.Join(root, "pr-1", "round-1")
		//nolint:gosec // a directory the run must fail to write into, restored below
		require.NoError(t, os.Chmod(round, 0o500))
		t.Cleanup(func() { _ = os.Chmod(round, 0o750) }) //nolint:gosec // restores the temp directory so cleanup can remove it

		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "open run archive")
		assert.Empty(t, r.stdout.String(), "a report next to no archive would read as auditable")
	})

	t.Run("a second round coexists with the first", func(t *testing.T) {
		r, root := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))

		second, _ := archiveRun(t)
		second.o.TasksDir, second.o.Run = root, "after-fix"
		roundIn(t, root, "after-fix")
		require.Equal(t, 1, run(second.opts()))

		assert.FileExists(t, filepath.Join(root, "pr-1", "round-1", "report.md"))
		assert.FileExists(t, filepath.Join(root, "pr-1", "after-fix", "report.md"))
	})
}

func TestRun_writesOnlyUnderItsOwnRun(t *testing.T) {
	// every path a review could reach is watched: this round's own caller-written input/, a sibling
	// round, and both config layers — loading the prompt tree must never install anything as a side effect
	r, root := archiveRun(t)
	user, project := t.TempDir(), t.TempDir()
	writeConfig(t, user, "verify-groups = 6\n")
	r.o.layers = configLayers{user: user, project: project}

	sibling := filepath.Join(root, "pr-1", "earlier")
	require.NoError(t, os.MkdirAll(sibling, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(sibling, task.ManifestFile), []byte(`{"run":"earlier"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sibling, task.FindingsFile), []byte(`{"findings":[]}`), 0o600))

	before := map[string][]string{"tasks": treeOf(t, root), "user": treeOf(t, user), "project": treeOf(t, project)}
	siblingBefore := treeOf(t, sibling)
	require.Equal(t, 1, run(r.opts()))

	assert.Equal(t, before["user"], treeOf(t, user), "a review reads the config tree and writes none of it")
	assert.Equal(t, before["project"], treeOf(t, project))
	assert.Equal(t, siblingBefore, treeOf(t, sibling), "an earlier round is what a reflection agent reads")

	own := filepath.Join("pr-1", "round-1")
	for _, p := range treeOf(t, root) {
		if slices.Contains(before["tasks"], p) {
			continue
		}
		assert.True(t, p == own || strings.HasPrefix(p, own+string(filepath.Separator)),
			"%q was created outside the round", p)
	}
}

func TestRun_tokensPerAgentSumToTheRunTotal(t *testing.T) {
	// per-agent token counts are one of the things a subprocess buys, so both the report and the
	// manifest carry them and the run total has to be their sum rather than an independent number
	tokens := map[string]int{"bugs": 4210, "codex": 1234}

	r, root := archiveRun(t)
	r.o.Lenses, r.o.StaggerDelay, r.o.Profile = nil, 0, "focused"

	ro := r.opts()
	ro.newRunner = func(spec pipeline.RunnerSpec) pipeline.Runner {
		name := "bugs"
		if spec.Executor == executorCodex {
			name = "codex"
		}
		return &pmocks.RunnerMock{
			RunFunc: func(_ context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
				res := r.result
				res.Tokens = tokens[name]
				return res, nil
			},
		}
	}
	require.Equal(t, 1, run(ro))

	want := tokens["bugs"] + tokens["codex"]
	dir := filepath.Join(root, "pr-1", "round-1")

	var rep finding.Report
	data, err := os.ReadFile(filepath.Join(dir, "findings.json")) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &rep))

	got := 0
	require.Len(t, rep.Sources.Agents, 2)
	for _, a := range rep.Sources.Agents {
		assert.Equal(t, tokens[a.Name], a.Tokens, "agent %s", a.Name)
		got += a.Tokens
	}
	assert.Equal(t, want, got)
	assert.Equal(t, want, rep.Stats.Tokens, "the run total is what the agents actually spent")

	var got2 manifest
	data, err = os.ReadFile(filepath.Join(dir, task.ManifestFile)) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &got2))

	sum := 0
	require.Len(t, got2.Agents, 2)
	for _, a := range got2.Agents {
		assert.Equal(t, tokens[a.Name], a.Tokens, "agent %s", a.Name)
		sum += a.Tokens
	}
	assert.Equal(t, want, sum)
	assert.Equal(t, want, got2.Tokens, "the manifest and the report must not disagree on what a run cost")

	// the per-agent table is part of the rendered report, which lives in the archive now that stdout
	// carries the machine shape
	rendered, err := os.ReadFile(filepath.Join(dir, "report.md")) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	for name, n := range tokens {
		assert.Contains(t, string(rendered), fmt.Sprintf("| %s |", name))
		assert.Contains(t, string(rendered), fmt.Sprintf("| %d |", n), "the rendered report shows per-agent tokens")
	}
}

func TestRun_history(t *testing.T) {
	// the inventory is resolved from reviewContext.TaskDir, and that field pointing at the round instead
	// of the task is a silent failure: History finds no rounds, returns "", and the block is dropped from
	// every composed prompt with no error anywhere
	t.Run("prior rounds reach every composed prompt, resolved from the task directory", func(t *testing.T) {
		first, root := archiveRun(t)
		require.Equal(t, 1, run(first.opts()))

		second, _ := archiveRun(t)
		second.o.TasksDir, second.o.Run = root, "after-fix"
		roundIn(t, root, "after-fix")
		require.Equal(t, 1, run(second.opts()))

		composed := second.prompts()[0]
		assert.Contains(t, composed, "Prior rounds for this task: "+filepath.Join(root, "pr-1"))
		assert.Contains(t, composed, "round-1")
		assert.Contains(t, composed, "1 findings (0 critical, 1 major, 0 minor)")
		assert.Contains(t, composed, "Re-evaluate everything independently",
			"the data and its guard are inseparable, so the composer appends the guard itself")

		assert.FileExists(t, filepath.Join(root, "pr-1", "round-1", task.ReportFile), "revmux deletes nothing")
		assert.FileExists(t, filepath.Join(root, "pr-1", "after-fix", task.ReportFile))
	})

	t.Run("the round being written is not listed in its own inventory", func(t *testing.T) {
		r, root := archiveRun(t)
		roundIn(t, root, "after-fix")
		require.Equal(t, 1, run(r.opts()))
		assert.NotContains(t, r.prompts()[0], "Prior rounds",
			"a sibling round nothing has claimed never ran, and this one is read before it is claimed")
	})

	t.Run("a first round carries no history block", func(t *testing.T) {
		r, _ := archiveRun(t)
		require.Equal(t, 1, run(r.opts()))
		assert.NotContains(t, r.prompts()[0], "Prior rounds",
			"an empty block would say nothing and still cost every prompt")
	})

	// an interrupt on a long review, an unwritable artifact or every source degrading leaves the marker
	// New created and nothing in it. The caller's own input/ is inside that round, so it is re-runnable
	// under the same name rather than a round he has to re-author somewhere else
	t.Run("a round claimed by a run that never finished is re-runnable and is not history", func(t *testing.T) {
		first, root := archiveRun(t)
		require.Equal(t, 1, run(first.opts()))

		roundIn(t, root, "after-fix")
		taskDir := filepath.Join(root, "pr-1")
		require.NoError(t, os.WriteFile(filepath.Join(taskDir, "after-fix", task.ManifestFile), nil, 0o600))

		rounds, err := task.Rounds(taskDir)
		require.NoError(t, err)
		assert.Equal(t, []string{"round-1"}, rounds,
			"an empty marker is a claim, so `revmux config` does not report that round as one that ran")

		second, _ := archiveRun(t)
		second.o.TasksDir, second.o.Run = root, "after-fix"
		require.Equal(t, 1, run(second.opts()), "the round is re-claimed rather than refused as already run")

		// the block alone, since the round's own name is in the scope path every prompt carries
		_, inventory, ok := strings.Cut(second.prompts()[0], "Prior rounds for this task: ")
		require.True(t, ok, "the finished round is still history")
		inventory, _, _ = strings.Cut(inventory, "\n\nEach round holds")
		assert.Contains(t, inventory, "round-1")
		assert.NotContains(t, inventory, "after-fix", "the round being written is not in its own inventory")
		assert.FileExists(t, filepath.Join(taskDir, "after-fix", task.FindingsFile))
	})
}

// archiveRun builds a review that really writes its archive: a fresh tasks root, one agent through the
// --lenses override, and a canned finding so the report is non-empty.
func archiveRun(t *testing.T) (*runHarness, string) {
	t.Helper()
	root := taskRoot(t)
	r := newRunOpts(t, options{
		Task: "pr-1", Run: "round-1", TasksDir: root, Profile: "focused", Lenses: []string{"bugs"},
		StaggerDelay: 30 * time.Second, MaxParallel: 4, NoSynthesis: true, NoVerify: true,
	})
	r.result = executor.Result{
		StructuredOutput: json.RawMessage(`{"findings":[{"file":"app/main.go","line":42,"severity":"major",` +
			`"confidence":90,"title":"unchecked error","body":"the write error is dropped","lenses":["bugs"]}]}`),
		Raw: `{"type":"result"}`, Tokens: 4210, ActualModel: "claude-opus-5",
	}
	return r, root
}

// prompts is what a process receives — the composed prompt plus whatever its executor appends — which
// is how a test asserts on an injected block without reading the archive back.
func (r *runHarness) prompts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) == 0 {
		return []string{""}
	}
	return slices.Clone(r.seen)
}
