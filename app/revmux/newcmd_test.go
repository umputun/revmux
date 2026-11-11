package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/task"
)

func TestParseArgs_newSubcommand(t *testing.T) {
	home := isolate(t)
	cfg := filepath.Join(home, "cfg")

	t.Run("the command word selects the scaffold", func(t *testing.T) {
		o, err := parseArgs([]string{"--config-dir", cfg, "new", "--task", "pr-1", "--run", "01-initial"})
		require.NoError(t, err)
		assert.True(t, o.showNew)
		assert.False(t, o.showConfig)
		assert.Equal(t, "pr-1", o.Task)
		assert.Equal(t, "01-initial", o.Run)
	})

	t.Run("a review still parses with no command word", func(t *testing.T) {
		o, err := parseArgs([]string{"--task", "pr-1", "--run", "01-initial", "--config-dir", cfg})
		require.NoError(t, err)
		assert.False(t, o.showNew)
	})

	t.Run("nothing is created during parsing", func(t *testing.T) {
		root := t.TempDir()
		_, err := parseArgs([]string{"--config-dir", cfg, "--tasks-dir", root, "new", "--task", "pr-1", "--run", "01"})
		require.NoError(t, err)
		assert.Equal(t, []string{"."}, treeOf(t, root), "Execute records the selection, run does the writing")
	})
}

// the whole point of the subcommand: what a caller writes to the paths it was handed is what a review of
// that same round reads, with no path composed anywhere in between.
func TestRun_new(t *testing.T) {
	scaffold := func(t *testing.T, o options) (task.Paths, *runHarness) {
		t.Helper()
		o.showNew = true
		r := newRunOpts(t, o)
		require.Equal(t, 0, run(r.opts()))
		assert.Empty(t, r.stderr.String(), "the paths are the whole output")

		var p task.Paths
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &p), "the caller model parses this")
		return p, r
	}

	t.Run("a fresh task reports every path and what it created", func(t *testing.T) {
		root := t.TempDir()
		p, r := scaffold(t, options{TasksDir: root, Task: "pr-123", Run: "01-initial"})

		assert.Contains(t, r.stdout.String(), "\n  \"task_dir\"", "indented, since a human reads it occasionally")
		assert.Equal(t, []string{"task_dir", "task_file", "round_dir", "input_dir"}, p.Created)
		assert.Equal(t, filepath.Join(root, "pr-123", "01-initial", task.InputDir), p.InputDir)
		assert.Equal(t, filepath.Join(p.InputDir, task.ScopeFile), p.Scope)
		assert.Equal(t, filepath.Join(p.InputDir, task.GoalFile), p.Goal)
		assert.Equal(t, filepath.Join(p.InputDir, task.ProfileFile), p.Profile)
		assert.Equal(t, filepath.Join(p.InputDir, task.ContextDir), p.Context)
		assert.DirExists(t, p.InputDir)
		assert.FileExists(t, p.TaskFile)
	})

	t.Run("the round it creates is one a review then runs", func(t *testing.T) {
		root := t.TempDir()
		p, _ := scaffold(t, options{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, os.WriteFile(p.Scope, []byte("review the diff"), 0o600))

		rc, err := options{TasksDir: root, Task: "pr-123", Run: "01-initial"}.resolveContext()
		require.NoError(t, err, "the review path opens exactly what new created")
		assert.Equal(t, p.Scope, rc.Scope)
		assert.Equal(t, p.TaskDir, rc.TaskDir, "the task directory, not the round, is what history enumerates")
	})

	t.Run("a review of the scaffolded round reads its input and archives into it", func(t *testing.T) {
		root := t.TempDir()
		p, _ := scaffold(t, options{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, os.WriteFile(p.Scope, []byte("review the diff"), 0o600))
		require.NoError(t, os.WriteFile(p.Goal, []byte("did the fixes land"), 0o600))

		r := newRunOpts(t, options{
			Task: "pr-123", Run: "01-initial", TasksDir: root, Profile: "focused", Lenses: []string{"bugs"},
			StaggerDelay: 30 * time.Second, MaxParallel: 4, NoSynthesis: true,
		})
		r.result = executor.Result{StructuredOutput: json.RawMessage(`{"findings":[{"file":"app/main.go","line":1,` +
			`"severity":"major","confidence":90,"title":"unchecked error","lenses":["bugs"]}]}`)}
		assert.Equal(t, 1, run(r.opts()), "one major finding, so the run exits 1")

		var rep finding.Report
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &rep))
		assert.Equal(t, p.Scope, rep.Scope.ScopePath, "the review points its agents at the path new reported")
		assert.Subset(t, treeOf(t, p.RoundDir),
			[]string{task.ManifestFile, task.FindingsFile, task.ReportFile, task.InputDir},
			"the round holds this run's artifacts beside the caller's own input")
		assert.Equal(t, "review the diff", readFile(t, p.Scope), "which is left exactly as he wrote it")
	})

	t.Run("a second round creates only the round", func(t *testing.T) {
		root := t.TempDir()
		scaffold(t, options{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		p, _ := scaffold(t, options{TasksDir: root, Task: "pr-123", Run: "02-after-fix"})
		assert.Equal(t, []string{"round_dir", "input_dir"}, p.Created)
	})

	t.Run("a round that has already run exits 2", func(t *testing.T) {
		root := t.TempDir()
		p, _ := scaffold(t, options{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, os.WriteFile(filepath.Join(p.RoundDir, task.ManifestFile), []byte("{}"), 0o600))

		r := newRunOpts(t, options{TasksDir: root, Task: "pr-123", Run: "01-initial", showNew: true})
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String(), "half a payload would parse as a complete one")
		assert.Contains(t, r.stderr.String(), "already run")
	})

	t.Run("a name that escapes the tasks root exits 2", func(t *testing.T) {
		root := t.TempDir()
		r := newRunOpts(t, options{TasksDir: root, Task: "../out", Run: "01", showNew: true})
		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "--task")
		assert.Equal(t, []string{"."}, treeOf(t, root), "a name is refused before anything is created")
	})

	t.Run("an omitted run exits 2", func(t *testing.T) {
		r := newRunOpts(t, options{TasksDir: t.TempDir(), Task: "pr-123", showNew: true})
		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "--run is empty")
	})

	t.Run("an unwritable stdout exits 2", func(t *testing.T) {
		r := newRunOpts(t, options{TasksDir: t.TempDir(), Task: "pr-123", Run: "01-initial", showNew: true})
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write task paths")
	})
}
