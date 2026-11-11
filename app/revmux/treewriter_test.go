package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeWriter_write(t *testing.T) {
	t.Run("a nested file is created and reported as created", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "tree")
		w := writerAt(t, dir)

		created, err := w.write("lenses/bugs.md", []byte("body"))
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, "body", readFile(t, filepath.Join(dir, "lenses", "bugs.md")))
		assert.Equal(t, filepath.Join(dir, "lenses", "bugs.md"), w.dest("lenses/bugs.md"))
	})

	t.Run("a file already there is left exactly as it is", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "lenses"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "lenses", "bugs.md"), []byte("mine"), 0o600))
		w := writerAt(t, dir)

		created, err := w.write("lenses/bugs.md", []byte("shipped"))
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, "mine", readFile(t, filepath.Join(dir, "lenses", "bugs.md")))
	})

	// this is where the dangling-link case is tested: the loader reads every project-layer .md, so an
	// init-level fixture would fail two steps earlier and never reach the writer at all
	t.Run("a dangling symlink is an entry, not a missing file", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "elsewhere.md")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "lenses"), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "lenses", "bugs.md")))
		w := writerAt(t, dir)

		created, err := w.write("lenses/bugs.md", []byte("shipped"))
		require.NoError(t, err)
		assert.False(t, created, "Stat would call this a missing file and write the shipped text through it")
		assert.NoFileExists(t, outside, "the target sits outside the destination and nothing is written there")
	})

	t.Run("a symlinked subdirectory leaving the destination is refused", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "lenses")))
		w := writerAt(t, dir)

		created, err := w.write("lenses/bugs.md", []byte("shipped"))
		require.Error(t, err, "a path escaping the destination is refused rather than followed")
		assert.False(t, created)
		assert.Contains(t, err.Error(), filepath.Join(dir, "lenses", "bugs.md"), "the failure names the entry")
		assert.NoFileExists(t, filepath.Join(outside, "bugs.md"),
			"os.Lstat dereferences the directory above the leaf, and this is what that would write")
	})

	// a link landing back inside the destination is contained, which is the whole guarantee: the write
	// stays in the tree the caller was handed
	t.Run("a symlinked subdirectory inside the destination is followed", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "real"), 0o750))
		require.NoError(t, os.Symlink("real", filepath.Join(dir, "lenses")))
		w := writerAt(t, dir)

		created, err := w.write("lenses/bugs.md", []byte("shipped"))
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, "shipped", readFile(t, filepath.Join(dir, "real", "bugs.md")))
	})

	// a refused create and a failed write are different failures, and a caller reading one prefix for both
	// cannot tell a planted entry from a full disk
	t.Run("a directory that cannot be written names the file it could not create", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		dir := t.TempDir()
		lenses := filepath.Join(dir, "lenses")
		require.NoError(t, os.Mkdir(lenses, 0o500))
		t.Cleanup(func() { _ = os.Chmod(lenses, 0o750) }) //nolint:gosec // restores the temp dir for cleanup
		w := writerAt(t, dir)

		_, err := w.write("lenses/bugs.md", []byte("shipped"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create "+filepath.Join(lenses, "bugs.md"))
	})

	t.Run("a destination that cannot be created is named", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		blocked := filepath.Join(t.TempDir(), "ro")
		require.NoError(t, os.Mkdir(blocked, 0o500))
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) }) //nolint:gosec // restores the temp dir for cleanup

		_, err := newTreeWriter(filepath.Join(blocked, "out"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create "+filepath.Join(blocked, "out"))
	})
}

func writerAt(t *testing.T, dir string) *treeWriter {
	t.Helper()
	w, err := newTreeWriter(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.close() })
	return w
}
