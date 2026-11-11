package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/task"
)

// the reason no --task is declared on the subcommand: the one a caller passed would be the one nothing
// read, and that removes nothing while reporting success.
func TestParseArgs_cleanupSubcommand(t *testing.T) {
	t.Run("the command word selects the removal and nothing else", func(t *testing.T) {
		isolate(t)
		o, err := parseArgs([]string{"cleanup", "--task", "pr-1"})
		require.NoError(t, err)
		assert.True(t, o.showCleanup)
		assert.False(t, o.showStats)
		assert.False(t, o.showNew)
		assert.False(t, o.showInit)
		assert.False(t, o.showConfig)
	})

	t.Run("a review still parses with no command word", func(t *testing.T) {
		isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--run", "01-initial"})
		require.NoError(t, err)
		assert.False(t, o.showCleanup)
	})

	t.Run("--task lands in one field whichever side of the command word it is on", func(t *testing.T) {
		isolate(t)
		before, err := parseArgs([]string{"--task", "pr-1", "cleanup"})
		require.NoError(t, err)
		after, err := parseArgs([]string{"cleanup", "--task", "pr-1"})
		require.NoError(t, err)

		assert.Equal(t, "pr-1", before.Task)
		assert.Equal(t, "pr-1", after.Task)
	})

	t.Run("nothing is removed during parsing", func(t *testing.T) {
		dir := isolate(t)
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-1"), 0o750))

		_, err := parseArgs([]string{"--tasks-dir", root, "cleanup", "--task", "pr-1"})
		require.NoError(t, err)
		assert.DirExists(t, filepath.Join(root, "pr-1"), "Execute records the selection, run does the removing")
		assert.Equal(t, []string{"."}, treeOf(t, dir))
	})
}

func TestRun_cleanup(t *testing.T) {
	t.Run("the task goes and what went is reported on stdout", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("bugs+impl", "bugs"), Findings: raised(3, "bugs+impl", "bugs")},
		})
		recordRound(t, filepath.Join(root, "beta", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("codex", "adversarial"), Findings: raised(1, "codex", "adversarial")},
		})

		r := newRunOpts(t, options{TasksDir: root, Task: "alpha", showCleanup: true})
		require.Equal(t, 0, run(r.opts()))
		assert.Empty(t, r.stderr.String(), "the payload is the whole output")

		var doc struct {
			TasksDir string `json:"tasks_dir"`
			Removed  []struct {
				ID     string  `json:"id"`
				Rounds int     `json:"rounds"`
				SizeMB float64 `json:"size_mb"`
			} `json:"removed"`
			TotalMB *float64 `json:"total_mb_after"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &doc), "the caller model parses this")
		require.Len(t, doc.Removed, 1)
		assert.Equal(t, "alpha", doc.Removed[0].ID)
		assert.Equal(t, 1, doc.Removed[0].Rounds)
		assert.Equal(t, root, doc.TasksDir)
		require.NotNil(t, doc.TotalMB, "the root measured cleanly, so the number is reported")

		assert.NoDirExists(t, filepath.Join(root, "alpha"))
		assert.DirExists(t, filepath.Join(root, "beta"), "only the named task goes")
	})

	t.Run("a task that is not there fails the call and removes nothing", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("bugs+impl", "bugs"), Findings: raised(1, "bugs+impl", "bugs")},
		})

		r := newRunOpts(t, options{TasksDir: root, Task: "typo", showCleanup: true})
		assert.Equal(t, 2, run(r.opts()), "a tool error, the code every subcommand shares for one")
		assert.Empty(t, r.stdout.String(), "nothing on stdout means nothing happened")
		assert.Contains(t, r.stderr.String(), "not found")
		assert.DirExists(t, filepath.Join(root, "alpha"))
	})

	t.Run("no --task at all names the flag rather than removing everything", func(t *testing.T) {
		isolate(t)
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			task.FoundFile: {Sources: roster("bugs+impl", "bugs"), Findings: raised(1, "bugs+impl", "bugs")},
		})

		r := newRunOpts(t, options{TasksDir: root, showCleanup: true})
		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "--task")
		assert.DirExists(t, filepath.Join(root, "alpha"), "an unnamed task is not every task")
	})
}
