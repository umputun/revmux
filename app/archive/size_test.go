package archive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// one unreadable round under one task must not discard every other task's numbers, and must not be
// reported as a task occupying nothing either: the total is a floor and says which entry it is missing.
func TestDirSize(t *testing.T) {
	t.Run("regular files under the tree are summed, at every depth", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "01-initial", "agents"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "01-initial", "manifest.json"), make([]byte, 100), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "01-initial", "agents", "bugs.jsonl"), make([]byte, 900), 0o600))

		got, unread, err := dirSize(dir)
		require.NoError(t, err)
		assert.Equal(t, int64(1000), got)
		assert.Empty(t, unread)
	})

	t.Run("an absent directory is zero and not an error", func(t *testing.T) {
		got, unread, err := dirSize(filepath.Join(t.TempDir(), "never-created"))
		require.NoError(t, err)
		assert.Equal(t, int64(0), got)
		assert.Empty(t, unread, "an absent dir is not even a skip")
	})

	t.Run("an unreadable entry is skipped and named, and what is readable is still summed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root traverses a 0000 directory regardless of its mode")
		}
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kept"), make([]byte, 500), 0o600))
		blocked := filepath.Join(dir, "blocked")
		require.NoError(t, os.MkdirAll(blocked, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(blocked, "inner"), make([]byte, 500), 0o600))
		require.NoError(t, os.Chmod(blocked, 0o000))
		// restored so t.TempDir's own cleanup can descend it; a directory needs its execute bit
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) }) //nolint:gosec // G302 is about files, not dirs

		got, unread, err := dirSize(dir)
		require.NoError(t, err, "one unreadable directory must not fail the measurement")
		assert.Equal(t, int64(500), got, "the readable file is still counted")
		require.Len(t, unread, 1)
		assert.Contains(t, unread[0], "blocked", "and the entry it could not read is named")
	})

	t.Run("a symlink contributes nothing and is not followed", func(t *testing.T) {
		target := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(target, "big"), make([]byte, 4096), 0o600))

		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "own"), make([]byte, 128), 0o600))
		require.NoError(t, os.Symlink(target, filepath.Join(dir, "elsewhere")))

		got, _, err := dirSize(dir)
		require.NoError(t, err)
		assert.Equal(t, int64(128), got, "a tree outside this task is not this task's cost")
	})

	t.Run("an empty directory is zero", func(t *testing.T) {
		got, _, err := dirSize(t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, int64(0), got)
	})
}

func TestMB(t *testing.T) {
	tbl := []struct {
		name  string
		bytes int64
		want  float64
	}{
		{"zero", 0, 0},
		{"exactly one megabyte", 1 << 20, 1.0},
		{"rounded to one decimal", 6_920_601, 6.6},
		{"a small file rounds to zero rather than vanishing from the type", 4096, 0},
		{"halfway rounds up", 1024 * 1024 * 3 / 2, 1.5},
	}
	for _, tt := range tbl {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, mb(tt.bytes), 0.001)
		})
	}
}

// the rounds argument is task.Rounds' order, which is lexical rather than chronological.
func TestLastRun(t *testing.T) {
	write := func(t *testing.T, dir, round, body string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, round), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, round, manifestFile), []byte(body), 0o600))
	}

	t.Run("the newest round's finished_at dates the task", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "01-initial", `{"finished_at":"2026-07-27T10:00:00Z"}`)
		write(t, dir, "02-after-fix", `{"finished_at":"2026-07-31T09:00:00Z"}`)

		assert.Equal(t, "2026-07-31", lastRun(dir, []string{"01-initial", "02-after-fix"}))
	})

	t.Run("order of the rounds argument does not decide it", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "01-initial", `{"finished_at":"2026-07-31T09:00:00Z"}`)
		write(t, dir, "02-after-fix", `{"finished_at":"2026-07-27T10:00:00Z"}`)

		assert.Equal(t, "2026-07-31", lastRun(dir, []string{"01-initial", "02-after-fix"}))
	})

	t.Run("a round with no manifest, no finished_at or an undecodable one is skipped", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "01-initial", `{"run":"01-initial"}`)
		write(t, dir, "02-broken", `not json at all`)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "03-bare"), 0o750))
		write(t, dir, "04-good", `{"finished_at":"2026-08-02T12:00:00Z"}`)

		got := lastRun(dir, []string{"01-initial", "02-broken", "03-bare", "04-good"})
		assert.Equal(t, "2026-08-02", got, "the one round that dates itself still dates the task")
	})

	t.Run("no round dates the task at all", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "01-initial", `{"run":"01-initial"}`)
		assert.Empty(t, lastRun(dir, []string{"01-initial"}))
	})

	t.Run("no rounds", func(t *testing.T) {
		assert.Empty(t, lastRun(t.TempDir(), nil))
	})
}
