package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/task"
)

func TestInputSnapshotter_load(t *testing.T) {
	root := t.TempDir()
	contextDir := filepath.Join(root, "context")
	require.NoError(t, os.MkdirAll(filepath.Join(contextDir, "nested"), 0o750))
	scope, goal := filepath.Join(root, "scope.md"), filepath.Join(root, "goal.md")
	require.NoError(t, os.WriteFile(scope, []byte("# Scope\nreview it"), 0o600))
	require.NoError(t, os.WriteFile(goal, []byte("find regressions"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "a.md"), []byte("**design**"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "nested", "b.json"), []byte(`{"ok":true}`), 0o600))

	s := &inputSnapshotter{limits: inputLimits{fileBytes: 1024, totalBytes: 4096, contexts: 10, entries: 20}}
	docs := s.load(reviewContext{Scope: scope, Goal: goal, Context: contextDir})

	require.Len(t, docs, 4, "no profile was supplied, so it costs no tab")
	assert.Equal(t, []string{"scope", "goal", "a.md", "nested/b.json"},
		[]string{docs[0].Label, docs[1].Label, docs[2].Label, docs[3].Label})
	assert.Equal(t, "input/scope.md", docs[0].Path)
	assert.Equal(t, "# Scope\nreview it", docs[0].Content)
	assert.True(t, docs[0].Markdown)
	assert.True(t, docs[2].Markdown)
	assert.False(t, docs[3].Markdown)

	t.Run("present empty optional file stays distinct from missing", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("scope"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(root, "goal.md"), nil, 0o600))

		snapshot := (&inputSnapshotter{limits: inputLimits{
			fileBytes: 100, totalBytes: 100, contexts: 2, entries: 10,
		}}).load(reviewContext{InputDir: root, Scope: scope})

		require.Len(t, snapshot, 2, "goal.md exists and keeps its tab; the absent profile costs none")
		assert.Equal(t, "goal", snapshot[1].Label)
		assert.Empty(t, snapshot[1].Content)
		assert.Empty(t, snapshot[1].Notice, "the renderer identifies this as an empty file")
	})
}

// inputPath rebuilds the round-local path and reports the tab absent when that file is missing, which
// under the project fallback would hide a profile the agents were reading — a calibrated run shown as
// an uncalibrated one
func TestInputSnapshotter_projectProfileKeepsItsTab(t *testing.T) {
	root := t.TempDir()
	scope := filepath.Join(root, task.ScopeFile)
	require.NoError(t, os.WriteFile(scope, []byte("scope"), 0o600))
	snapshot := filepath.Join(root, "input-profile.md")
	require.NoError(t, os.WriteFile(snapshot, []byte("# project calibration"), 0o600))

	docs := (&inputSnapshotter{limits: inputLimits{
		fileBytes: 1024, totalBytes: 4096, contexts: 2, entries: 10,
	}}).load(reviewContext{InputDir: root, Scope: scope, Profile: snapshot, ProfileSource: "/elsewhere/profile.md"})

	require.Len(t, docs, 2)
	assert.Equal(t, "profile", docs[1].Label)
	assert.Equal(t, task.ProfileSnapshotFile, docs[1].Path, "labeled by where the bytes really came from")
	assert.Equal(t, "# project calibration", docs[1].Content)
}

func TestInputSnapshotter_limits(t *testing.T) {
	t.Run("per-file and context count", func(t *testing.T) {
		root := t.TempDir()
		contextDir := filepath.Join(root, "context")
		require.NoError(t, os.Mkdir(contextDir, 0o750))
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("123456789"), 0o600))
		for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(contextDir, name), []byte(name), 0o600))
		}

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 5, totalBytes: 100, contexts: 2, entries: 10}}
		docs := s.load(reviewContext{Scope: scope, Context: contextDir})

		assert.Equal(t, "12345", docs[0].Content)
		assert.Contains(t, docs[0].Notice, "showing 5 of 9 bytes")
		require.Len(t, docs, 4, "scope, two context files and one overflow tab; no goal or profile was supplied")
		assert.Equal(t, "more", docs[3].Label)
		assert.Equal(t, "more context entries omitted after the 2-file display or 10-entry traversal limit", docs[3].Notice)
	})

	t.Run("aggregate limit leaves the tab visible", func(t *testing.T) {
		root := t.TempDir()
		scope, goal := filepath.Join(root, "scope.md"), filepath.Join(root, "goal.md")
		require.NoError(t, os.WriteFile(scope, []byte("1234"), 0o600))
		require.NoError(t, os.WriteFile(goal, []byte("5678"), 0o600))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 10, totalBytes: 4, contexts: 2, entries: 10}}
		docs := s.load(reviewContext{Scope: scope, Goal: goal})

		assert.Equal(t, "1234", docs[0].Content)
		assert.Empty(t, docs[1].Content)
		assert.Contains(t, docs[1].Notice, "snapshot display limit was reached")
	})

	t.Run("empty file remains known after aggregate limit", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("1234"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(root, "goal.md"), nil, 0o600))

		docs := (&inputSnapshotter{limits: inputLimits{
			fileBytes: 10, totalBytes: 4, contexts: 2, entries: 10,
		}}).load(reviewContext{InputDir: root, Scope: scope})

		assert.Equal(t, "1234", docs[0].Content)
		assert.Empty(t, docs[1].Content)
		assert.Empty(t, docs[1].Notice)
	})

	t.Run("empty directories consume the traversal budget", func(t *testing.T) {
		root := t.TempDir()
		contextDir := filepath.Join(root, "context")
		require.NoError(t, os.Mkdir(contextDir, 0o750))
		for _, name := range []string{"a", "b", "c", "d"} {
			require.NoError(t, os.Mkdir(filepath.Join(contextDir, name), 0o750))
		}

		docs := (&inputSnapshotter{limits: inputLimits{
			fileBytes: 10, totalBytes: 100, contexts: 10, entries: 2,
		}}).context(contextDir)

		require.Len(t, docs, 1)
		assert.Equal(t, "more", docs[0].Label)
		assert.Contains(t, docs[0].Notice, "2-entry traversal limit")
	})

	t.Run("an exact traversal budget does not invent omitted entries", func(t *testing.T) {
		contextDir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(contextDir, "a"), 0o750))
		require.NoError(t, os.Mkdir(filepath.Join(contextDir, "b"), 0o750))

		docs := (&inputSnapshotter{limits: inputLimits{
			fileBytes: 10, totalBytes: 100, contexts: 10, entries: 2,
		}}).context(contextDir)

		// the budget is spent exactly, so nothing was omitted and no `more` entry may claim otherwise.
		// Directories holding no file are the same absence as no directory, and get no tab either.
		assert.Empty(t, docs)
	})
}

// an absent or empty context/ is the ordinary case, so it costs no tab. A directory that could not be
// read is not that: the reader has no other way to learn the snapshot failed to show what agents saw.
func TestInputSnapshotter_absentContextHasNoTab(t *testing.T) {
	root := t.TempDir()
	scope := filepath.Join(root, "scope.md")
	require.NoError(t, os.WriteFile(scope, []byte("scope"), 0o600))
	s := &inputSnapshotter{limits: inputLimits{fileBytes: 1024, totalBytes: 4096, contexts: 10, entries: 20}}

	t.Run("no context directory", func(t *testing.T) {
		docs := s.load(reviewContext{Scope: scope})
		require.Len(t, docs, 1, "goal, profile and context are all optional, so an absent one costs no tab")
		assert.Equal(t, "scope", docs[0].Label)
	})

	t.Run("empty context directory", func(t *testing.T) {
		assert.Empty(t, s.context(t.TempDir()))
	})

	t.Run("an unreadable directory keeps its tab", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		blocked := filepath.Join(t.TempDir(), "gone")
		docs := s.context(blocked)
		require.Len(t, docs, 1, "a failure to read is not an absence")
		assert.Equal(t, "context", docs[0].Label)
		assert.Contains(t, docs[0].Notice, "cannot read context directory")
	})

	t.Run("a symlinked directory keeps its tab", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real")
		require.NoError(t, os.Mkdir(target, 0o750))
		link := filepath.Join(root, "link")
		require.NoError(t, os.Symlink(target, link))

		docs := s.context(link)
		require.Len(t, docs, 1, "an untraversed link is information, not an absence")
		assert.Equal(t, "context/", docs[0].Label)
		assert.Equal(t, "symlinked directory not traversed", docs[0].Notice)
	})
}

func TestInputSnapshotter_unsafeAndUnusualInputs(t *testing.T) {
	t.Run("terminal controls are never displayed", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("safe\x1b[31munsafe"), 0o600))

		docs := (&inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 100, contexts: 2, entries: 10}}).
			load(reviewContext{Scope: scope})

		assert.Empty(t, docs[0].Content)
		assert.Contains(t, docs[0].Notice, "binary or unsafe text")
		assert.NotContains(t, docs[0].Notice, "\x1b")
	})

	t.Run("CRLF is normalized and a bare carriage return is rejected", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("first\r\nsecond"), 0o600))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 100, contexts: 2, entries: 10}}
		docs := s.load(reviewContext{Scope: scope})
		assert.Equal(t, "first\nsecond", docs[0].Content)

		require.NoError(t, os.WriteFile(scope, []byte("trusted\rspoofed"), 0o600))
		s = &inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 100, contexts: 2, entries: 10}}
		docs = s.load(reviewContext{Scope: scope})
		assert.Empty(t, docs[0].Content)
		assert.Contains(t, docs[0].Notice, "binary or unsafe text")
	})

	t.Run("truncation before the newline keeps a CRLF document visible", func(t *testing.T) {
		root := t.TempDir()
		scope := filepath.Join(root, "scope.md")
		require.NoError(t, os.WriteFile(scope, []byte("12345\r\nlast"), 0o600))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 6, totalBytes: 100, contexts: 2, entries: 10}}
		docs := s.load(reviewContext{Scope: scope})

		assert.Equal(t, "12345", docs[0].Content)
		assert.Equal(t, "truncated: showing 5 of 11 bytes", docs[0].Notice)
	})

	for _, tt := range []struct {
		name       string
		fileLimit  int64
		totalLimit int64
	}{
		{name: "per-file truncation keeps complete UTF-8", fileLimit: 2, totalLimit: 100},
		{name: "aggregate truncation keeps complete UTF-8", fileLimit: 100, totalLimit: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			scope := filepath.Join(root, "scope.md")
			require.NoError(t, os.WriteFile(scope, []byte("a€b"), 0o600))

			docs := (&inputSnapshotter{limits: inputLimits{
				fileBytes: tt.fileLimit, totalBytes: tt.totalLimit, contexts: 2, entries: 10,
			}}).load(reviewContext{Scope: scope})

			assert.Equal(t, "a", docs[0].Content)
			assert.Equal(t, "truncated: showing 1 of 5 bytes", docs[0].Notice)
		})
	}

	t.Run("file symlinks are read and directory symlinks are not traversed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks requires privileges on Windows")
		}
		root := t.TempDir()
		contextDir, targetDir := filepath.Join(root, "context"), filepath.Join(root, "target-dir")
		require.NoError(t, os.Mkdir(contextDir, 0o750))
		require.NoError(t, os.Mkdir(targetDir, 0o750))
		target := filepath.Join(root, "target.txt")
		require.NoError(t, os.WriteFile(target, []byte("linked text"), 0o600))
		require.NoError(t, os.Symlink(target, filepath.Join(contextDir, "file-link.txt")))
		require.NoError(t, os.Symlink(targetDir, filepath.Join(contextDir, "dir-link")))
		require.NoError(t, os.Symlink(filepath.Join(root, "missing"), filepath.Join(contextDir, "broken")))

		s := &inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 1000, contexts: 10, entries: 20}}
		docs := s.context(contextDir)

		require.Len(t, docs, 3)
		assert.Equal(t, "broken", docs[0].Label)
		assert.Contains(t, docs[0].Notice, "cannot read")
		assert.Equal(t, "dir-link/", docs[1].Label)
		assert.Equal(t, "symlinked directory not traversed", docs[1].Notice)
		assert.Equal(t, "linked text", docs[2].Content)
	})

	t.Run("a symlinked context root is listed but not traversed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks requires privileges on Windows")
		}
		root := t.TempDir()
		targetDir, contextLink := filepath.Join(root, "target-dir"), filepath.Join(root, "context")
		require.NoError(t, os.Mkdir(targetDir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(targetDir, "design.md"), []byte("hidden"), 0o600))
		require.NoError(t, os.Symlink(targetDir, contextLink))

		docs := (&inputSnapshotter{limits: inputLimits{fileBytes: 100, totalBytes: 1000, contexts: 10, entries: 20}}).
			context(contextLink)

		require.Len(t, docs, 1)
		assert.Equal(t, "context/", docs[0].Label)
		assert.Equal(t, "input/context", docs[0].Path)
		assert.Equal(t, "symlinked directory not traversed", docs[0].Notice)
	})
}
