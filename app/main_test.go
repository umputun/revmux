package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	pmocks "github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
	"github.com/umputun/revmux/app/ui"
)

func TestPrintVersion(t *testing.T) {
	tests := []struct {
		name string
		rev  string
		want string
	}{
		{name: "stamped revision", rev: "master-abc1234-20260726T120000", want: "revmux master-abc1234-20260726T120000\n"},
		{name: "default revision", rev: revision, want: "revmux unknown\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			require.NoError(t, printVersion(buf, tt.rev))
			assert.Equal(t, tt.want, buf.String())
		})
	}

	t.Run("write failure", func(t *testing.T) {
		err := printVersion(failingWriter{}, "v1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write version")
	})
}

func TestBinary_versionOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "revmux")
	build := exec.Command("go", "build", "-ldflags", "-X main.revision=test-rev", "-o", bin, ".") //nolint:gosec // fixed argv, output path from t.TempDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	// main parses arguments before it prints the version, and parsing walks ~/.config/revmux and
	// ./.revmux. A child inheriting the developer's real environment fails this test over an unrelated
	// config, and the ban on touching the user's own setup applies to a spawned binary too.
	home := t.TempDir()
	cmd := exec.Command(bin, "--version") //nolint:gosec // binary just built by this test
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)

	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "output: %s", out)
	assert.Equal(t, "revmux test-rev\n", string(out))
}

func TestRun_metaCommands(t *testing.T) {
	t.Run("version goes to stdout", func(t *testing.T) {
		r := newRunOpts(t, options{Version: true})
		assert.Equal(t, 0, run(r.opts()))
		assert.Equal(t, "revmux unknown\n", r.stdout.String())
		assert.Empty(t, r.stderr.String())
	})

	t.Run("init materializes the tree and reports it on stdout", func(t *testing.T) {
		dir := isolate(t)
		r := newRunOpts(t, options{Init: true})
		assert.Equal(t, 0, run(r.opts()))
		assert.FileExists(t, filepath.Join(dir, projectDirName, configFileName))
		assert.FileExists(t, filepath.Join(dir, projectDirName, "lenses", "bugs.md"))
		assert.Empty(t, r.stderr.String(), "the paths are the whole output")

		var p initPaths
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &p))
		assert.Equal(t, filepath.Join(dir, projectDirName), p.Dir)
		assert.NotEmpty(t, p.Files)
	})

	t.Run("dump-defaults extracts the tree", func(t *testing.T) {
		dir := t.TempDir()
		r := newRunOpts(t, options{DumpDefaults: dir})
		assert.Equal(t, 0, run(r.opts()))
		assert.FileExists(t, filepath.Join(dir, "lenses", "bugs.md"))
		assert.Empty(t, r.stdout.String())
	})

	t.Run("dump-defaults failure exits 2", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		blocked := filepath.Join(t.TempDir(), "ro")
		require.NoError(t, os.Mkdir(blocked, 0o500))
		r := newRunOpts(t, options{DumpDefaults: filepath.Join(blocked, "out")})
		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "error:")
	})

	t.Run("version write failure exits 2", func(t *testing.T) {
		r := newRunOpts(t, options{Version: true})
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write version")
	})
}

func TestRun_config(t *testing.T) {
	root := taskRoot(t)

	emitted := func(t *testing.T, o options) (catalog, *runHarness) {
		t.Helper()
		o.showConfig = true
		r := newRunOpts(t, o)
		require.Equal(t, 0, run(r.opts()))
		assert.Empty(t, r.stderr.String(), "the catalog is the whole output")

		var c catalog
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &c), "the caller model parses this")
		return c, r
	}

	t.Run("every shipped profile and lens is reported", func(t *testing.T) {
		c, r := emitted(t, options{TasksDir: root, Profile: "comprehensive"})
		assert.Contains(t, r.stdout.String(), "\n  \"knobs\"", "indented, since a human reads it occasionally")

		names := make([]string, 0, len(c.Profiles))
		for _, p := range c.Profiles {
			names = append(names, p.Name)
			assert.NotEmpty(t, p.Description, "profile %s has no description", p.Name)
			assert.NotEmpty(t, p.Roster, "profile %s reports no roster", p.Name)
			for _, a := range p.Roster {
				assert.NotEmpty(t, a.Lenses, "agent %s carries no lens", a.Name)
				assert.NotEmpty(t, a.Model, "agent %s reports no model", a.Name)
				assert.NotEmpty(t, a.Effort, "agent %s reports no effort", a.Name)
				assert.NotEmpty(t, a.Executor, "agent %s reports no executor", a.Name)
				assert.NotEmpty(t, a.Color, "agent %s reports no color, so the palette assignment is invisible", a.Name)
			}
		}
		assert.Equal(t, []string{"claude-only", "codex-only", "comprehensive", "final", "focused"}, names)

		set, err := prompt.Load(prompt.LoadOpts{})
		require.NoError(t, err)
		require.Len(t, c.Lenses, len(set.LensNames()))
		for _, l := range c.Lenses {
			assert.NotEmpty(t, l.Description, "lens %s is uncomposable without a description", l.Name)
		}
	})

	t.Run("a roster matches what a run of that profile dispatches", func(t *testing.T) {
		c, _ := emitted(t, options{TasksDir: root})

		set, err := prompt.Load(prompt.LoadOpts{})
		require.NoError(t, err)
		for _, p := range c.Profiles {
			profile, err := set.Profile(p.Name)
			require.NoError(t, err)
			want, err := profile.Roster(nil, set.LensNames())
			require.NoError(t, err)
			assert.Equal(t, want, p.Roster, "profile %s", p.Name)
		}
	})

	t.Run("nothing is written to the tasks directory", func(t *testing.T) {
		before := treeOf(t, root)
		c, _ := emitted(t, options{TasksDir: root, Task: "pr-1", Run: "round-1"})

		assert.Equal(t, before, treeOf(t, root), "the catalog runs before any archive exists")
		assert.NoFileExists(t, filepath.Join(root, "pr-1", "round-1", task.ManifestFile),
			"the catalog claims no round, so a later review of it still runs")
		assert.Equal(t, []taskInfo{{ID: "pr-1", Rounds: []string{}}}, c.Paths.Tasks,
			"a caller picks a --run name from what is already there, and round-1 has not run")
	})

	t.Run("an unreadable prompt tree exits 2", func(t *testing.T) {
		cfg := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(cfg, "lenses"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(cfg, "lenses", "bugs.md"), []byte("---\nnope:\n---\nbody\n"), 0o600))

		r := newRunOpts(t, options{showConfig: true, TasksDir: root, layers: configLayers{user: cfg}})
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String(), "a half-written catalog would parse as a complete one")
		assert.Contains(t, r.stderr.String(), "load prompts")
	})

	t.Run("an unwritable stdout exits 2", func(t *testing.T) {
		r := newRunOpts(t, options{showConfig: true, TasksDir: root})
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write catalog")
	})

	t.Run("an unresolvable profile still reports the tree", func(t *testing.T) {
		c, _ := emitted(t, options{TasksDir: root, Profile: "nope"})
		assert.NotEmpty(t, c.Profiles, "the caller running this is the one whose --profile does not resolve")
	})
}

func TestRun_reviewGate(t *testing.T) {
	root := taskRoot(t)

	tests := []struct {
		name    string
		opts    options
		wantErr string
	}{
		{name: "no task", opts: options{TasksDir: root, Profile: "focused"}, wantErr: "--task is required"},
		{name: "bad task name", opts: options{Task: "../out", Run: "round-1", TasksDir: root, Profile: "focused"}, wantErr: "path separator"},
		{name: "bad run name", opts: options{Task: "pr-1", Run: "a/b", TasksDir: root, Profile: "focused"}, wantErr: "--run"},
		{name: "no run", opts: options{Task: "pr-1", TasksDir: root, Profile: "focused"}, wantErr: "revmux new --task pr-1"},
		{name: "missing round", opts: options{Task: "pr-1", Run: "round-9", TasksDir: root, Profile: "focused"}, wantErr: "round-9"},
		{name: "missing task dir", opts: options{Task: "pr-2", Run: "round-1", TasksDir: root, Profile: "focused"}, wantErr: "task directory"},
		{name: "unknown profile", opts: options{Task: "pr-1", Run: "round-1", TasksDir: root, Profile: "nope"}, wantErr: "resolve profile"},
		{name: "unknown lens", opts: options{Task: "pr-1", Run: "round-1", TasksDir: root, Profile: "focused", Lenses: []string{"nope"}}, wantErr: "resolve roster"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunOpts(t, tt.opts)
			assert.Equal(t, 2, run(r.opts()))
			assert.Empty(t, r.stdout.String(), "nothing but the report may reach stdout")
			assert.Contains(t, r.stderr.String(), tt.wantErr)
		})
	}
}

func TestRun_review(t *testing.T) {
	// every subtest takes its own tasks root: a run name that already exists is a load-time error, so
	// two runs of round-1 under one root would collide rather than exercise what each case asserts
	base := func(t *testing.T) options {
		t.Helper()
		return options{
			Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused", Lenses: []string{"bugs"},
			StaggerDelay: 30 * time.Second, MaxParallel: 4,
			NoSynthesis: true, // these assert rendering and exit codes; the merge has its own case below
			// most cases here read the rendered report, so they ask for it; stdout is JSON by default
			// because the caller is a program, and the case below that checks that says so itself
			Markdown: true,
		}
	}

	t.Run("markdown to stdout, progress to stderr, exit 1", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{
			StructuredOutput: json.RawMessage(`{"findings":[{"file":"app/main.go","line":42,"severity":"major",` +
				`"confidence":90,"title":"unchecked error","body":"the write error is dropped","lenses":["bugs"]}]}`),
			Tokens: 4210, ActualModel: "claude-opus-5",
		}

		assert.Equal(t, 1, run(r.opts()), "findings above the threshold exit 1")

		out := r.stdout.String()
		assert.Contains(t, out, "# Review: pr-1 / round-1")
		assert.Contains(t, out, "## Major")
		assert.Contains(t, out, "unchecked error")
		assert.Contains(t, out, "`app/main.go:42`")
		assert.Contains(t, out, "sources: lenses", "Go stamps the executing agent's name")
		assert.Contains(t, out, "claude-opus-5")
		assert.Contains(t, out, "4210")

		assert.Contains(t, r.stderr.String(), "── find ──", "the stage line, not the word inside \"1 findings\"")
		// the plain renderer prefixes the agent in its own resolved color, the same one the ui uses,
		// padded to the column a one-agent roster still holds open for the derived verify group below it
		pad := func(name string) string { return strings.Repeat(" ", minNameWidth-len(name)+2) }
		assert.Contains(t, r.stderr.String(), prompt.AgentSpec{Color: "6"}.Paint("lenses")+pad("lenses")+"done, 1 findings")
		assert.Contains(t, r.stderr.String(),
			prompt.DerivedSpec("verify app").Paint("verify app")+pad("verify app")+"started [app]",
			"and a derived name lands in that same column rather than past it")
	})

	t.Run("json to stdout, which is the default", func(t *testing.T) {
		o := base(t)
		o.Markdown = false // the default, and what a caller model parses
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"x"}]}`)}

		assert.Equal(t, 1, run(r.opts()))

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &rep))
		assert.Equal(t, "pr-1", rep.Scope.Task)
		assert.Equal(t, "round-1", rep.Scope.Run)
		assert.Equal(t, filepath.Join(o.TasksDir, "pr-1", "round-1", task.InputDir, task.ScopeFile), rep.Scope.ScopePath)
		require.Len(t, rep.Findings, 1)
		assert.Equal(t, []string{"lenses"}, rep.Findings[0].Sources)
	})

	t.Run("min-confidence filters the report and the exit code together", func(t *testing.T) {
		o := base(t)
		o.MinConfidence = 80
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"below the bar"}]}`)}

		assert.Equal(t, 0, run(r.opts()))
		assert.Contains(t, r.stdout.String(), "No findings.")
		assert.NotContains(t, r.stdout.String(), "below the bar")
	})

	// the findings browser is a rendering path like stdout is, and review is what hands it the
	// report. Filtering after that hand-off lists in the ui exactly what stdout and the exit code
	// both say is absent, so the threshold has to be applied before review returns.
	t.Run("min-confidence is applied before the renderer is handed the report", func(t *testing.T) {
		o := base(t)
		o.MinConfidence = 80
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"below the bar"},` +
				`{"file":"b.go","line":2,"severity":"major","confidence":90,"title":"above the bar"}]}`)}

		ro := r.opts()
		review, err := ro.pipelineConfig()
		require.NoError(t, err)

		rep, err := ro.review(context.Background(), review)
		require.NoError(t, err)
		require.Len(t, rep.Findings, 1, "review returns the same report it gave the renderer")
		assert.Equal(t, "above the bar", rep.Findings[0].Title)
	})

	// run derives this context from an interrupt, and children are started with Setsid, so the terminal
	// never signals them. Cancellation is the only thing that reaps them, and it has to reach the agent
	// rather than stopping at the pipeline.
	t.Run("canceling the review's context cancels the agent and stops the run", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		ro := r.opts()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var launched atomic.Int64
		var agentErr error
		ro.newRunner = func(pipeline.RunnerSpec) pipeline.Runner {
			return &pmocks.RunnerMock{
				RunFunc: func(ctx context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
					launched.Add(1)
					cancel() // stands in for the interrupt landing mid-run
					agentErr = ctx.Err()
					return executor.Result{}, ctx.Err()
				},
			}
		}

		review, err := ro.pipelineConfig()
		require.NoError(t, err)

		_, err = ro.review(ctx, review)
		require.Error(t, err, "a canceled run has no review to report")
		require.Error(t, agentErr, "the agent's own context falls with the run, which is what kills its process group")
		assert.Equal(t, int64(1), launched.Load(), "a canceled run is not retried")
	})

	t.Run("the synthesis stage is wired and its merge reaches stdout", func(t *testing.T) {
		o := base(t)
		o.NoSynthesis = false
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"raw","lenses":["bugs"]}]}`)}
		r.synth = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"merged_ids":["lenses-1"],"file":"a.go","line":1,"severity":"major","confidence":85,` +
				`"title":"merged","body":"what it breaks"}],"open_questions":[],"pre_existing":[]}`)}

		assert.Equal(t, 1, run(r.opts()))
		out := r.stdout.String()
		assert.Contains(t, out, "merged", "the merged finding replaces the passthrough")
		assert.NotContains(t, out, "raw")
		assert.Contains(t, out, "sources: lenses", "attribution survives the merge")
		assert.Contains(t, r.stderr.String(), "── synthesis ──", "the stage line, not the agent of the same name")
	})

	t.Run("nothing found exits 0", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{StructuredOutput: json.RawMessage(`{"findings":[]}`)}
		assert.Equal(t, 0, run(r.opts()))
		assert.Contains(t, r.stdout.String(), "No findings.")
	})

	t.Run("every source degraded exits 2 with no report", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.runErr = errors.New("stalled")
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String(), "an empty report would read as a clean run")
		assert.Contains(t, r.stderr.String(), "review failed")
		assert.Contains(t, r.stderr.String(), "every source degraded")
		assert.Contains(t, r.stderr.String(), "retrying:", "the source was retried before it was given up on")
		assert.Contains(t, r.stderr.String(), "degraded:")
		assert.Equal(t, 2, r.attempts(), "one launch plus one retry, then the run stops")
	})

	// both renderings, because base() asks for markdown and the default path would otherwise go
	// untested — a report that cannot reach the caller must fail the run whichever shape it was in
	for _, tc := range []struct {
		name     string
		markdown bool
		want     string
	}{
		{"a markdown report that cannot be written exits 2", true, "write markdown report"},
		{"and neither can a json one, which is the default", false, "report to stdout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := base(t)
			o.Markdown = tc.markdown
			r := newRunOpts(t, o)
			r.result = executor.Result{StructuredOutput: json.RawMessage(`{"findings":[]}`)}
			ro := r.opts()
			ro.stdout = failingWriter{}
			assert.Equal(t, 2, run(ro))
			assert.Contains(t, r.stderr.String(), tc.want)
		})
	}
}

func TestRun_promptsCarryPathsNotContents(t *testing.T) {
	// the tasks root is outside the tree under review, which is where the never-embed rule is easiest
	// to break: nothing about these paths is relative to the repo, so a composer tempted to inline
	// would have to inline the whole file
	root := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(root, cwd+string(os.PathSeparator)), "the tasks root must be outside the repo")

	round := filepath.Join(root, "pr-1", "round-1")
	input := filepath.Join(round, task.InputDir)
	require.NoError(t, os.MkdirAll(filepath.Join(input, task.ContextDir), 0o750))
	contents := map[string]string{
		task.ScopeFile:   "SENTINEL-SCOPE run git diff master...HEAD",
		task.GoalFile:    "SENTINEL-GOAL make the watchdog observable",
		task.ProfileFile: "SENTINEL-PROFILE go, tests with testify",
		filepath.Join(task.ContextDir, "ticket.md"): "SENTINEL-CONTEXT the ticket body",
	}
	for name, body := range contents {
		require.NoError(t, os.WriteFile(filepath.Join(input, name), []byte(body), 0o600))
	}

	r := newRunOpts(t, options{
		Task: "pr-1", Run: "round-1", TasksDir: root, Profile: "focused", Lenses: []string{"bugs"},
		StaggerDelay: 30 * time.Second, MaxParallel: 4, VerifyGroups: 4, NoSynthesis: true,
	})
	r.result = executor.Result{StructuredOutput: json.RawMessage(
		`{"findings":[{"file":"app/main.go","line":42,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}
	require.Equal(t, 1, run(r.opts()))

	prompts := r.prompts()
	require.Len(t, prompts, 2, "the finder and the verifier its finding produced")

	for _, want := range []string{
		filepath.Join(input, task.ScopeFile), filepath.Join(input, task.GoalFile),
		filepath.Join(input, task.ProfileFile), filepath.Join(input, task.ContextDir),
	} {
		assert.Contains(t, prompts[0], want, "the agent is handed the path and reads the file itself")
	}

	// the archived prompt is the bytes the process received, so a leak would show up in both
	archived, err := os.ReadFile(filepath.Join(round, "prompts", "agents", "lenses.md")) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	assert.Equal(t, prompts[0], string(archived))

	for _, body := range contents {
		sentinel := strings.SplitN(body, " ", 2)[0]
		for i, p := range prompts {
			assert.NotContains(t, p, sentinel, "prompt %d carries file contents, not just a path", i)
		}
		assert.NotContains(t, string(archived), sentinel)
	}
}

func TestRun_degradedSourceDoesNotAbortTheRun(t *testing.T) {
	// the whole focused roster: a claude lens agent plus a codex peer, no stagger, since the mock
	// runner emits no activity and the fake clock fires no timer, so nothing would open the gate
	r := newRunOpts(t, options{
		Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused",
		MaxParallel: 4, NoSynthesis: true, NoVerify: true, Markdown: true,
	})
	r.result = executor.Result{
		StructuredOutput: json.RawMessage(`{"findings":[{"file":"app/main.go","line":42,"severity":"major",` +
			`"confidence":90,"title":"unchecked error","lenses":["bugs"]}]}`),
		Tokens: 4210, ActualModel: "claude-opus-5",
	}

	var codexRuns atomic.Int64
	ro := r.opts()
	ro.newRunner = func(spec pipeline.RunnerSpec) pipeline.Runner {
		return &pmocks.RunnerMock{
			RunFunc: func(_ context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
				if spec.Executor == executorCodex {
					codexRuns.Add(1)
					return executor.Result{}, errors.New("killed")
				}
				return r.result, nil
			},
		}
	}

	assert.Equal(t, 1, run(ro), "one dead source must not waste every other agent's work")
	assert.EqualValues(t, 2, codexRuns.Load(), "one launch plus one retry, and never a third")

	out := r.stdout.String()
	assert.Contains(t, out, "**Degraded run**: 1 of 2 sources reported, missing codex",
		"a degraded run that reads like a complete one is the worst failure this tool has")
	assert.Contains(t, out, "unchecked error", "the surviving source still reported")
	assert.Contains(t, out, "| codex | codex |")
	assert.Contains(t, out, "| degraded |")

	stderr := r.stderr.String()
	assert.Contains(t, stderr, "retrying:")
	assert.Contains(t, stderr, "degraded:")
}

func TestRun_configComposesARunnableInvocation(t *testing.T) {
	// what a caller model actually does: read the catalog, then compose from it alone
	catalogOf := func(t *testing.T, root string) catalog {
		t.Helper()
		r := newRunOpts(t, options{showConfig: true, TasksDir: root})
		require.Equal(t, 0, run(r.opts()))
		var c catalog
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &c))
		return c
	}

	review := func(t *testing.T, o options) (*runHarness, string) {
		t.Helper()
		root := taskRoot(t)
		o.Task, o.Run, o.TasksDir = "pr-1", "round-1", root
		o.MaxParallel, o.NoSynthesis, o.NoVerify = 4, true, true
		r := newRunOpts(t, o)
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"minor","confidence":55,"title":"x"}]}`)}
		require.Equal(t, 1, run(r.opts()), "an invocation composed from the catalog must run")
		return r, root
	}

	t.Run("every lens the catalog names composes into a prompt", func(t *testing.T) {
		c := catalogOf(t, taskRoot(t))
		require.NotEmpty(t, c.Lenses)

		lenses := make([]string, 0, len(c.Lenses))
		for _, l := range c.Lenses {
			lenses = append(lenses, l.Name)
		}

		r, _ := review(t, options{Profile: c.Profiles[0].Name, Lenses: lenses})
		for _, name := range lenses {
			assert.Contains(t, r.prompts()[0], "## Lens: "+name, "lens %s is named by the catalog but never loaded", name)
		}
	})

	t.Run("a reported roster is what that profile dispatches", func(t *testing.T) {
		for _, p := range catalogOf(t, taskRoot(t)).Profiles {
			t.Run(p.Name, func(t *testing.T) {
				_, root := review(t, options{Profile: p.Name})

				// the agents that ran, read back from the prompt each one was archived under
				entries, err := os.ReadDir(filepath.Join(root, "pr-1", "round-1", "prompts", "agents"))
				require.NoError(t, err)
				dispatched := make([]string, 0, len(entries))
				for _, e := range entries {
					dispatched = append(dispatched, strings.TrimSuffix(e.Name(), ".md"))
				}

				want := make([]string, 0, len(p.Roster))
				for _, a := range p.Roster {
					want = append(want, a.Name)
				}
				slices.Sort(want)
				slices.Sort(dispatched)
				assert.Equal(t, want, dispatched)
			})
		}
	})
}

func TestRunOpts_tty(t *testing.T) {
	// a real terminal is never opened in a test, so the opener stands in for one: what matters is
	// that the gate is the opener and nothing else
	opened := func(t *testing.T) func() (*os.File, error) {
		t.Helper()
		return func() (*os.File, error) { return os.CreateTemp(t.TempDir(), "tty") }
	}

	t.Run("an openable tty is what enables the ui", func(t *testing.T) {
		r := newRunOpts(t, options{})
		ro := r.opts()
		ro.stdout = failingWriter{} // stdout is not a terminal here, and must not be the gate
		ro.openTTY = opened(t)

		tty := ro.tty()
		require.NotNil(t, tty)
		assert.NoError(t, tty.Close())
	})

	t.Run("--no-tui never opens one", func(t *testing.T) {
		r := newRunOpts(t, options{NoTUI: true})
		ro := r.opts()
		ro.openTTY = func() (*os.File, error) {
			t.Fatal("the opener must not be reached with --no-tui")
			return nil, errors.New("unreachable")
		}
		assert.Nil(t, ro.tty())
	})

	t.Run("a tty that will not open falls back to the plain renderer", func(t *testing.T) {
		assert.Nil(t, newRunOpts(t, options{}).opts().tty(), "the harness opener always fails")
	})

	t.Run("no opener at all is the same as no tty", func(t *testing.T) {
		ro := newRunOpts(t, options{}).opts()
		ro.openTTY = nil
		assert.Nil(t, ro.tty())
	})
}

func TestRunOpts_render(t *testing.T) {
	roster := []prompt.AgentSpec{{Name: "bugs", Color: "6"}}

	// finished drives one renderer through a whole run: two events, the channel closed, then the
	// report handed over the way review does it. The channel is unbuffered and the sends are waited
	// on, because the second one returns only after the renderer applied the first — without that
	// the frame under assertion races the quit.
	// A test that needs the reader to close the browser holds the key with holdKey before calling this,
	// since the wait below only ends once he does.
	finished := func(t *testing.T, ro runOpts, rep finding.Report, err, arcErr error) {
		t.Helper()
		events, sent := make(chan pipeline.Event), make(chan struct{})
		go func() {
			defer close(sent)
			events <- pipeline.Event{Kind: pipeline.EventStage, Stage: "find", At: time.Now()}
			events <- pipeline.Event{Kind: pipeline.EventAgentStarted, Agent: "bugs", Text: "bugs", At: time.Now()}
			close(events)
		}()

		r := ro.render(renderConfig{roster: roster, events: events})
		select {
		case <-sent:
		case <-time.After(10 * time.Second):
			// the sends are unbuffered, so a renderer that stopped reading — a q that quit mid-run
			// again, say — parks this goroutine. Bounded, that is a named failure rather than the
			// package timeout ten minutes later
			t.Fatal("the renderer must drain the events it was given before the report arrives")
		}
		done := make(chan struct{})
		go func() { defer close(done); r.finish(rep, err, arcErr) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the renderer must let go of the terminal once the report is in")
		}
	}

	t.Run("with no tty the plain renderer takes the events", func(t *testing.T) {
		r := newRunOpts(t, options{})
		ro := r.opts()
		ro.snapshot = func(reviewContext) []ui.InputDocument {
			t.Fatal("the headless renderer must not read review inputs")
			return nil
		}
		finished(t, ro, finding.Report{}, nil, nil)
		assert.Contains(t, r.stderr.String(), "── find ──", "the stage line, not the word inside \"1 findings\"")
		assert.Empty(t, r.stdout.String(), "stdout belongs to the report alone")
	})

	// summarized carries the text a leaking summary would echo: a title and a body the code has access to
	// and must not print. Without them the NotContains assertions below hold no matter what finish does.
	summarized := finding.Report{
		Sources: finding.SourceStatus{Expected: 2, Reported: 2},
		Findings: []finding.Finding{{Severity: finding.Major, Title: "unchecked error",
			Body: "Close is dropped on the error path", File: "proc.go", Fix: "check it"}},
	}

	t.Run("the plain renderer closes with a summary, after the last event line", func(t *testing.T) {
		r := newRunOpts(t, options{})
		finished(t, r.opts(), summarized, nil, nil)

		out := r.stderr.String()
		assert.Contains(t, out, "── complete ──", "a reader tailing the log is told the run ended")
		assert.Contains(t, out, "sources 2/2, degraded none")
		assert.Contains(t, out, "1 findings: 1 major")
		assert.Greater(t, strings.Index(out, "── complete ──"), strings.Index(out, "started [bugs]"),
			"the drain goroutine owns the event lines, so the summary may only follow them")
		assert.NotContains(t, out, "unchecked error", "a finding's title belongs to stdout")
		assert.NotContains(t, out, "Close is dropped", "and so does its body")
		assert.NotContains(t, out, "proc.go", "and its location")
	})

	t.Run("an archive failure gets no summary, since the round it describes is unusable", func(t *testing.T) {
		r := newRunOpts(t, options{})
		finished(t, r.opts(), summarized, nil, errors.New("archive report.md: no space left on device"))

		out := r.stderr.String()
		assert.Contains(t, out, "── find ──", "the event lines still stand")
		assert.NotContains(t, out, "── complete ──",
			"the run exits 2 over a half-written archive, so the log may not call it complete")
		assert.NotContains(t, out, "sources 2/2")
	})

	t.Run("with a tty the ui renders there, and a failed run gets the terminal back", func(t *testing.T) {
		r := newRunOpts(t, options{})
		ro := r.opts()
		open, frames, _ := ttyPair(t)
		ro.openTTY = open
		snapshotted := false
		ro.snapshot = func(reviewContext) []ui.InputDocument {
			snapshotted = true
			return []ui.InputDocument{{Label: "scope", Path: "input/scope.md", Content: "# scope", Markdown: true}}
		}

		finished(t, ro, finding.Report{}, errors.New("every source degraded"), nil)

		assert.True(t, snapshotted, "the startup snapshot is built once the tty opens")
		assert.Contains(t, frames(), "find", "the ui renders to the tty")
		assert.Empty(t, r.stdout.String(), "and never to stdout")
		assert.Empty(t, r.stderr.String(), "nor to the plain renderer's stream")
	})

	t.Run("a finished report goes to the browser, and the reader closing it ends the wait", func(t *testing.T) {
		r := newRunOpts(t, options{})
		ro := r.opts()
		open, _, press := ttyPair(t)
		ro.openTTY = open
		holdKey(t, press, "q")

		finished(t, ro, finding.Report{Findings: []finding.Finding{{Title: "unchecked error"}}}, nil, nil)
		assert.Empty(t, r.stdout.String(), "the browser renders the report, it does not write it")
		assert.Empty(t, r.stderr.String(), "and the browser is the summary, so stderr gets none of its own")
	})
}

func TestRun_reportWrittenOnce(t *testing.T) {
	base := func(t *testing.T) options {
		t.Helper()
		return options{
			Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused", Lenses: []string{"bugs"},
			StaggerDelay: 30 * time.Second, MaxParallel: 4, NoSynthesis: true,
			Markdown: true, // this reads the rendered banner off stdout
		}
	}
	found := executor.Result{StructuredOutput: json.RawMessage(
		`{"findings":[{"file":"a.go","line":1,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}

	// the reader may close the browser before or after the report reaches it; either way package
	// main is the one writer, which is what these assert
	t.Run("under the tui", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = found
		ro := r.opts()
		open, _, press := ttyPair(t)
		ro.openTTY = open
		var snapshotted atomic.Bool
		ro.snapshot = func(reviewContext) []ui.InputDocument {
			snapshotted.Store(true)
			return []ui.InputDocument{{Label: "scope", Path: "input/scope.md", Content: "# scope", Markdown: true}}
		}
		newRunner := ro.newRunner
		ro.newRunner = func(spec pipeline.RunnerSpec) pipeline.Runner {
			assert.True(t, snapshotted.Load(), "the tty snapshot must finish before the first review process starts")
			return newRunner(spec)
		}
		holdKey(t, press, "q")

		assert.Equal(t, 1, run(ro))
		out := r.stdout.String()
		assert.Equal(t, 1, strings.Count(out, "# Review: pr-1 / round-1"), "written once, by package main only")
		assert.Equal(t, 1, strings.Count(out, "unchecked error"))
		assert.Empty(t, r.stderr.String(), "the ui took the events, so the plain renderer saw none")
	})

	t.Run("under --no-tui", func(t *testing.T) {
		o := base(t)
		o.NoTUI = true
		r := newRunOpts(t, o)
		r.result = found

		assert.Equal(t, 1, run(r.opts()))
		out := r.stdout.String()
		assert.Equal(t, 1, strings.Count(out, "# Review: pr-1 / round-1"))
		assert.Equal(t, 1, strings.Count(out, "unchecked error"))
		assert.Contains(t, r.stderr.String(), "── find ──", "and the plain renderer took the events")
	})
}

// the three artifacts package main owns are what make a finished run auditable, and none of them are
// recoverable from the report the caller was shown. A run that emits findings while leaving the archive
// short of them reads as a review that died mid-run.
func TestRun_archivesItsOwnArtifacts(t *testing.T) {
	o := options{
		Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused", Lenses: []string{"bugs"},
		StaggerDelay: 30 * time.Second, MaxParallel: 4, NoSynthesis: true, MinConfidence: 80,
		Markdown: true,
	}
	r := newRunOpts(t, o)
	r.result = executor.Result{
		StructuredOutput: json.RawMessage(`{"findings":[{"file":"a.go","line":1,"severity":"major",` +
			`"confidence":90,"title":"above the bar","lenses":["bugs"]},` +
			`{"file":"b.go","line":2,"severity":"minor","confidence":55,"title":"below the bar"}]}`),
		Tokens: 4210, ActualModel: "claude-opus-5",
	}

	assert.Equal(t, 1, run(r.opts()))

	runDir := filepath.Join(o.TasksDir, "pr-1", "round-1")
	assert.Subset(t, treeOf(t, runDir), []string{task.ReportFile, task.FindingsFile, task.ManifestFile})

	// the archived report is the filtered one, byte for byte what the caller was shown
	md, err := os.ReadFile(filepath.Join(runDir, task.ReportFile)) //nolint:gosec // path from t.TempDir
	require.NoError(t, err)
	assert.Equal(t, r.stdout.String(), string(md))
	assert.Contains(t, string(md), "above the bar")
	assert.NotContains(t, string(md), "below the bar", "the filter runs before the archive, not after it")

	var archived finding.Report
	fj, err := os.ReadFile(filepath.Join(runDir, task.FindingsFile)) //nolint:gosec // path from t.TempDir
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(fj, &archived))
	require.Len(t, archived.Findings, 1)
	assert.Equal(t, []string{"lenses"}, archived.Findings[0].Sources)

	var m manifest
	mj, err := os.ReadFile(filepath.Join(runDir, task.ManifestFile)) //nolint:gosec // path from t.TempDir
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(mj, &m))
	assert.Equal(t, "pr-1", m.Task)
	assert.Equal(t, "round-1", m.Run)
	assert.Equal(t, "focused", m.Profile)
	require.Len(t, m.Agents, 1, "the manifest is built from the resolved roster")
	assert.Equal(t, "claude-opus-5", m.Agents[0].ActualModel, "what actually ran, not what was requested")
	assert.NotEmpty(t, m.Prompts, "which prompt file won each precedence race")
}

func TestRun_ttyGate(t *testing.T) {
	// the report is redirected here, so stdout is a pipe and not a terminal: that must not decide
	// whether the ui runs, or `revmux > findings.json` would silently lose it
	base := func(t *testing.T) options {
		t.Helper()
		return options{
			Task: "pr-1", Run: "round-1", TasksDir: taskRoot(t), Profile: "focused", Lenses: []string{"bugs"},
			StaggerDelay: 30 * time.Second, MaxParallel: 4, NoSynthesis: true,
		}
	}

	redirected := func(t *testing.T, ro runOpts) string {
		t.Helper()
		pr, pw, err := os.Pipe()
		require.NoError(t, err)
		ro.stdout = pw

		assert.Equal(t, 1, run(ro))
		require.NoError(t, pw.Close())
		out, err := io.ReadAll(pr)
		require.NoError(t, err)
		return string(out)
	}

	t.Run("an openable tty runs the ui even though stdout is a pipe", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}
		ro := r.opts()
		open, _, press := ttyPair(t)
		ro.openTTY = open
		holdKey(t, press, "q")

		out := redirected(t, ro)

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(out), &rep), "the redirected report is still whole json")
		require.Len(t, rep.Findings, 1)
		assert.Empty(t, r.stderr.String(), "the ui took the events, so the plain renderer never ran")
	})

	t.Run("a tty that will not open falls back to the plain renderer", func(t *testing.T) {
		r := newRunOpts(t, base(t))
		r.result = executor.Result{StructuredOutput: json.RawMessage(
			`{"findings":[{"file":"a.go","line":1,"severity":"major","confidence":90,"title":"unchecked error"}]}`)}

		out := redirected(t, r.opts()) // the harness opener always fails

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(out), &rep))
		assert.Contains(t, r.stderr.String(), "── find ──", "the stage line, not the word inside \"1 findings\"")
	})
}

// ttyPair hands the run a real pty and gives the test all three ends of it: the opener yields the
// slave side, frames reports what was rendered to it, and press types keys on it.
//
// press returns its error rather than asserting on it: the program closes the slave before the call
// the test is waiting in returns, and a write to a master with no slave open fails with EIO. Its one
// caller is holdKey, which is racing exactly that shutdown by design.
//
// A regular file cannot stand in for a terminal here, which is what this replaces. It is not
// pollable, so the input reader falls back to a path that cannot be interrupted, and a program asked
// to quit never finishes letting go — a hang that only appears on linux and cost a CI timeout to
// find. A pty is a terminal, so raw mode, polling and shutdown all behave the way they do in use.
//
// The master is drained continuously rather than read at the end: its buffer is a few kilobytes, an
// alt-screen frame is bigger than that, and an undrained pty blocks the renderer mid-write. Echo is
// left alone — bubbletea turns it off with raw mode, and a keystroke echoed before that only adds a
// character no assertion here looks at.
func ttyPair(t *testing.T) (open func() (*os.File, error), frames func() string, press func(string) error) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	require.NoError(t, err)

	var mu sync.Mutex
	var buf bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		b := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		_, _ = tty.Close(), ptmx.Close()
		<-drained
	})

	return func() (*os.File, error) { return tty, nil },
		func() string {
			mu.Lock()
			defer mu.Unlock()
			return buf.String()
		},
		func(keys string) error {
			if _, writeErr := ptmx.WriteString(keys); writeErr != nil {
				return fmt.Errorf("press %q: %w", keys, writeErr)
			}
			return nil
		}
}

// holdKey presses one key on the pty until the test is over, which is how a test closes the findings
// browser. A single press cannot: q is inert until the report reaches the browser, and the report is
// handed over by the same call the test is blocked in, so the one press that would work has to land
// after it.
//
// It stops on the first write error rather than failing the test on one. The key it is pressing ends
// the program, and the program closes the slave side of the pty before the call the test is waiting
// in returns — so the write that lands in that window fails with EIO, and asserting on it would turn
// the shutdown this is driving into a failure whose timing is what decides it.
func holdKey(t *testing.T, press func(string) error, k string) {
	t.Helper()
	stop, wg := make(chan struct{}), &sync.WaitGroup{}
	wg.Add(1)
	t.Cleanup(func() { close(stop); wg.Wait() })
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				if err := press(k); err != nil {
					return
				}
			}
		}
	}()
}

func TestRunOpts_runnerFactory(t *testing.T) {
	t.Run("a supplied factory wins", func(t *testing.T) {
		r := newRunOpts(t, options{})
		got := r.opts().runnerFactory(reviewContext{})(pipeline.RunnerSpec{Executor: "claude"})
		assert.IsType(t, &pmocks.RunnerMock{}, got)
	})

	t.Run("routes by the spec's executor", func(t *testing.T) {
		tests := []struct {
			name string
			spec pipeline.RunnerSpec
			want pipeline.Runner
		}{
			{"claude", pipeline.RunnerSpec{Executor: "claude"}, &executor.Claude{}},
			{"codex", pipeline.RunnerSpec{Executor: "codex"}, &executor.Codex{}},
			{"empty defaults to claude", pipeline.RunnerSpec{}, &executor.Claude{}},
		}

		r := newRunOpts(t, options{IdleTimeout: time.Minute, HardTimeout: 20 * time.Minute})
		ro := r.opts()
		ro.newRunner = nil
		factory := ro.runnerFactory(reviewContext{WorkDir: t.TempDir()})

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.IsType(t, tt.want, factory(tt.spec))
			})
		}
	})
}

// treeOf lists every path under dir, relative and sorted, so a test can assert a whole directory is
// untouched rather than naming the files it expects not to appear.
func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		require.NoError(t, err)
		out = append(out, rel)
		return nil
	})
	require.NoError(t, err)
	slices.Sort(out)
	return out
}

// taskRoot builds a tasks root holding one task with its first round prepared, never the real
// ./.revmux/tasks.
func taskRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	roundIn(t, root, "round-1")
	return root
}

// roundIn prepares one round of the pr-1 task the way a caller leaves it: a directory under the task
// holding the input/ this round's review context lives in. revmux opens it and creates none of it, so a
// review against a round nothing prepared has no scope to read.
func roundIn(t *testing.T, root, run string) {
	t.Helper()
	input := filepath.Join(root, "pr-1", run, task.InputDir)
	require.NoError(t, os.MkdirAll(input, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(input, task.ScopeFile), []byte("review the diff"), 0o600))
}

// runHarness holds the writers a run wrote to, so a test can assert on each stream separately, plus
// the canned executor result every agent gets back.
type runHarness struct {
	o      options
	stdout *strings.Builder
	stderr *strings.Builder
	result executor.Result
	synth  executor.Result
	runErr error
	runs   atomic.Int64

	mu   sync.Mutex
	seen []string // every prompt the runner was handed, in launch order
}

// synthesisMarker is a line only the synthesis prompt carries, so the harness answers that stage
// with its own fixture rather than handing it a finder-shaped one.
const synthesisMarker = "merging a review panel"

// attempts is how many processes the run launched, which is what proves a failing source was retried
// exactly once rather than not at all or forever.
func (r *runHarness) attempts() int { return int(r.runs.Load()) }

func newRunOpts(t *testing.T, o options) *runHarness {
	t.Helper()
	return &runHarness{o: o, stdout: &strings.Builder{}, stderr: &strings.Builder{}}
}

func (r *runHarness) opts() runOpts {
	clk := &mocks.ClockMock{
		NowFunc: func() time.Time { return time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC) },
		AfterFuncFunc: func(time.Duration, func()) executor.Timer {
			return &mocks.TimerMock{
				StopFunc:  func() bool { return true },
				ResetFunc: func(time.Duration) bool { return true },
			}
		},
	}
	return runOpts{
		opts: r.o, clock: clk, stdout: r.stdout, stderr: r.stderr,
		openTTY:   func() (*os.File, error) { return nil, errors.New("no tty in tests") },
		newRunner: r.newRunner,
	}
}

func (r *runHarness) newRunner(spec pipeline.RunnerSpec) pipeline.Runner {
	return &pmocks.RunnerMock{
		RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
			r.runs.Add(1)
			// a real executor appends to the prompt inside Run, and this mock stands in for one. Record
			// what a process would actually receive, or an archive compared against this is compared
			// against bytes no process ever saw
			received := req.Prompt + executor.ClaudeNarrationContract(req.Schema)
			if spec.Executor == "codex" {
				received = req.Prompt + executor.CodexOutputContract(req.Schema)
			}
			r.mu.Lock()
			r.seen = append(r.seen, received)
			r.mu.Unlock()
			if req.RawOutput != nil {
				_, _ = req.RawOutput.Write([]byte(r.result.Raw))
			}
			if strings.Contains(req.Prompt, synthesisMarker) {
				return r.synth, nil
			}
			return r.result, r.runErr
		},
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
