package archive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

func TestCleanup(t *testing.T) {
	t.Run("the named task and every round under it is gone, and what went is reported", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs")},
		})
		recordRound(t, filepath.Join(root, "alpha", "02-after-fix"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})
		recordRound(t, filepath.Join(root, "beta", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})

		got, err := Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.NoError(t, err)

		assert.Equal(t, root, got.TasksDir)
		require.Len(t, got.Removed, 1)
		assert.Equal(t, "alpha", got.Removed[0].ID)
		assert.Equal(t, 2, got.Removed[0].Rounds, "both rounds are counted before the tree goes")

		assert.NoDirExists(t, filepath.Join(root, "alpha"))
		assert.DirExists(t, filepath.Join(root, "beta"), "no other task is touched")
	})

	t.Run("the caller's own input is removed with the task", func(t *testing.T) {
		root := t.TempDir()
		round := filepath.Join(root, "alpha", "01-initial")
		recordRound(t, round, map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(1, "solo", "bugs")},
		})
		require.NoError(t, os.MkdirAll(filepath.Join(round, "input"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(round, "input", "scope.md"), []byte("scope"), 0o600))

		_, err := Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.NoError(t, err)
		assert.NoDirExists(t, filepath.Join(root, "alpha"))
	})

	t.Run("a task with no rounds yet is still removable", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "alpha"), 0o750))

		got, err := Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.NoError(t, err)
		assert.Equal(t, 0, got.Removed[0].Rounds)
		assert.NoDirExists(t, filepath.Join(root, "alpha"))
	})

	t.Run("what the root costs afterwards is measured after the removal", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "alpha", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs")},
		})
		require.NoError(t, os.MkdirAll(filepath.Join(root, "beta"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "beta", "keep"), make([]byte, 3<<20), 0o600))

		got, err := Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.NoError(t, err)
		require.NotNil(t, got.TotalMB, "the root was measurable, so the number is there")
		assert.InDelta(t, 3.0, *got.TotalMB, 0.1, "only beta's 3MB is left")
	})
}

// the idle check releases each round's lock as it moves on, so it must leave none behind on a task it
// refused: a lock still held would block the next review from claiming that round.
func TestCleanup_refusals(t *testing.T) {
	t.Run("no task named", func(t *testing.T) {
		_, err := Cleanup(CleanupQuery{TasksDir: t.TempDir()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--task")
	})

	t.Run("a name that is not one entry under the root", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "alpha"), 0o750))
		for _, name := range []string{"../alpha", "sub/alpha", "/etc", ".hidden", ".."} {
			_, err := Cleanup(CleanupQuery{TasksDir: root, Task: name})
			require.Error(t, err, name)
			assert.DirExists(t, filepath.Join(root, "alpha"), "nothing is removed by a refused name")
		}
	})

	t.Run("a task that is not there is an error rather than an empty removal", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "alpha"), 0o750))

		_, err := Cleanup(CleanupQuery{TasksDir: root, Task: "beta"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		assert.DirExists(t, filepath.Join(root, "alpha"))
	})

	t.Run("a round held by a live run refuses the whole task", func(t *testing.T) {
		root := t.TempDir()
		round := filepath.Join(root, "alpha", "01-initial")
		require.NoError(t, os.MkdirAll(round, 0o750))
		require.NoError(t, os.MkdirAll(filepath.Join(round, "input"), 0o750))

		// the claim a running review holds: an empty marker under an exclusive lock
		marker := filepath.Join(round, manifestFile)
		f, err := os.Create(marker) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		ok, err := tryLock(f)
		require.NoError(t, err)
		require.True(t, ok)
		defer f.Close()

		_, err = Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "held by a running review")
		assert.DirExists(t, round, "nothing is removed while a review is writing into it")
	})

	t.Run("the check leaves no lock behind on a task it refused", func(t *testing.T) {
		root := t.TempDir()
		round := filepath.Join(root, "alpha", "01-initial")
		require.NoError(t, os.MkdirAll(round, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(round, manifestFile), nil, 0o600))
		// a second round a live review holds, so the whole task is refused after the first was checked
		held := filepath.Join(root, "alpha", "02-live")
		require.NoError(t, os.MkdirAll(held, 0o750))
		marker := filepath.Join(held, manifestFile)
		f, err := os.Create(marker) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		ok, err := tryLock(f)
		require.NoError(t, err)
		require.True(t, ok)
		defer f.Close()

		_, err = Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.Error(t, err)

		// the first round's marker must be lockable again: a review can still claim it
		g, err := os.OpenFile(filepath.Join(round, manifestFile), os.O_WRONLY, 0o600) //nolint:gosec // t.TempDir
		require.NoError(t, err)
		defer g.Close()
		ok, err = tryLock(g)
		require.NoError(t, err)
		assert.True(t, ok, "the check released what it took")
	})

	t.Run("a round whose run is gone leaves its lock free and the task removable", func(t *testing.T) {
		root := t.TempDir()
		round := filepath.Join(root, "alpha", "01-initial")
		require.NoError(t, os.MkdirAll(round, 0o750))
		// an empty marker nobody holds: the claim of a run that never came back
		require.NoError(t, os.WriteFile(filepath.Join(round, manifestFile), nil, 0o600))

		_, err := Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.NoError(t, err)
		assert.NoDirExists(t, filepath.Join(root, "alpha"))
	})
}

// task.List accepts a task directory that is a relative symlink to a sibling, and every read path follows
// it — so cleanup reaches one and must not report the alias as the history it points at.
func TestCleanup_alias(t *testing.T) {
	t.Run("a symlinked task is refused, naming what it points at", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs")},
		})
		require.NoError(t, os.Symlink("pr-123", filepath.Join(root, "oauth")))

		_, err := Cleanup(CleanupQuery{TasksDir: root, Task: "oauth"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
		assert.Contains(t, err.Error(), "pr-123", "the message names the id to pass instead")

		assert.DirExists(t, filepath.Join(root, "pr-123", "01-initial"), "the target keeps every round")
		_, statErr := os.Lstat(filepath.Join(root, "oauth"))
		require.NoError(t, statErr, "and the alias itself is not unlinked either")
	})

	t.Run("the task the alias points at is still removable under its own id", func(t *testing.T) {
		root := t.TempDir()
		recordRound(t, filepath.Join(root, "pr-123", "01-initial"), map[string]finding.Report{
			foundFile: {Sources: sources(agent("solo", "bugs")), Findings: raised(2, "solo", "bugs")},
		})
		require.NoError(t, os.Symlink("pr-123", filepath.Join(root, "oauth")))

		// a megabyte of filler, so the reported size is a number describe could only have read before the
		// tree went — SizeMB alone rounds a small fixture to 0.0 and would pin nothing
		require.NoError(t, os.WriteFile(filepath.Join(root, "pr-123", "01-initial", "bulk"),
			make([]byte, 2<<20), 0o600))

		got, err := Cleanup(CleanupQuery{TasksDir: root, Task: "pr-123"})
		require.NoError(t, err)
		assert.Equal(t, 1, got.Removed[0].Rounds)
		assert.InDelta(t, 2.0, got.Removed[0].SizeMB, 0.1, "measured before the tree went")
		assert.NoDirExists(t, filepath.Join(root, "pr-123"))
	})
}

// a task holding one unreadable directory must still be removable, or the user is pushed back to the
// rm -rf this command exists to replace.
func TestCleanup_unreadableEntry(t *testing.T) {
	t.Run("a task with an unreadable directory is still removed, its size a floor", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root traverses a 0000 directory regardless of its mode")
		}
		root := t.TempDir()
		round := filepath.Join(root, "alpha", "01-initial")
		require.NoError(t, os.MkdirAll(round, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(round, manifestFile), []byte(`{"run":"x"}`), 0o600))
		blocked := filepath.Join(round, "agents")
		require.NoError(t, os.MkdirAll(blocked, 0o700))
		require.NoError(t, os.Chmod(blocked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) }) //nolint:gosec // G302 is about files, not dirs

		got, err := Cleanup(CleanupQuery{TasksDir: root, Task: "alpha"})
		require.NoError(t, err, "an entry the measurement could not read must not block the removal")
		assert.Equal(t, "alpha", got.Removed[0].ID)
	})
}
