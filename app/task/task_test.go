package task

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name string
		file string
		want Meta
	}{
		{name: "full front matter", file: "---\ndescription: OAuth rework\nurl: https://github.com/u/r/pull/1\n" +
			"branch: feature/oauth\nbase: 4ed3259\n---\n\nReviewing the token exchange path.\n",
			want: Meta{Description: "OAuth rework", URL: "https://github.com/u/r/pull/1", Branch: "feature/oauth", Base: "4ed3259"}},
		{name: "partial front matter", file: "---\ndescription: just a note\n---\n", want: Meta{Description: "just a note"}},
		{name: "body only", file: "reviewing the token exchange path\n", want: Meta{}},
		{name: "empty front matter", file: "---\n---\n\nbody text\n", want: Meta{}},
		{name: "empty file", file: "", want: Meta{}},
		{name: "crlf line endings", file: "---\r\ndescription: windows\r\n---\r\n\r\nbody\r\n", want: Meta{Description: "windows"}},
		{name: "no body after front matter", file: "---\nbranch: topic\n---", want: Meta{Branch: "topic"}},
		{name: "a rule in the body is not a second block", file: "---\nbase: 4ed3259\n---\n\nprose\n\n---\n\nmore\n",
			want: Meta{Base: "4ed3259"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, metaFile), []byte(tc.file), 0o600))
			got, err := Load(dir)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("absent file", func(t *testing.T) {
		got, err := Load(t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, Meta{}, got)
	})

	t.Run("absent directory", func(t *testing.T) {
		got, err := Load(filepath.Join(t.TempDir(), "nope"))
		require.NoError(t, err)
		assert.Equal(t, Meta{}, got)
	})
}

func TestLoad_errors(t *testing.T) {
	tests := []struct {
		name string
		file string
		msg  string
	}{
		{name: "unknown key", file: "---\ndescription: x\nrepo: github.com/u/r\n---\n", msg: "field repo not found"},
		{name: "malformed yaml", file: "---\ndescription: [unterminated\n---\n", msg: "front matter"},
		{name: "unterminated front matter", file: "---\ndescription: x\n\nbody with no closing marker\n", msg: "never closed"},
		{name: "a longer marker does not close the block", file: "---\ndescription: x\n----\n\nbody\n", msg: "never closed"},
		{name: "scalar where a mapping is wanted", file: "---\njust a string\n---\n", msg: "front matter"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, metaFile), []byte(tc.file), 0o600))
			_, err := Load(dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}

	t.Run("unreadable file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, metaFile), 0o750))
		_, err := Load(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read ")
	})
}

func TestScaffold(t *testing.T) {
	t.Run("fresh task", func(t *testing.T) {
		root := t.TempDir()
		p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err)

		assert.Equal(t, []string{"task_dir", "task_file", "round_dir", "input_dir"}, p.Created)
		assert.Equal(t, filepath.Join(root, "pr-123"), p.TaskDir)
		assert.Equal(t, filepath.Join(root, "pr-123", metaFile), p.TaskFile)
		assert.Equal(t, filepath.Join(root, "pr-123", "01-initial"), p.RoundDir)
		assert.Equal(t, filepath.Join(root, "pr-123", "01-initial", InputDir), p.InputDir)
		assert.Equal(t, filepath.Join(p.InputDir, ScopeFile), p.Scope)
		assert.Equal(t, filepath.Join(p.InputDir, GoalFile), p.Goal)
		assert.Equal(t, filepath.Join(p.InputDir, ProfileFile), p.Profile)
		assert.Equal(t, filepath.Join(p.InputDir, ContextDir), p.Context)

		assert.DirExists(t, p.InputDir)
		assert.FileExists(t, p.TaskFile)
		assert.NoDirExists(t, p.Context, "revmux creates only what it names, the caller fills the rest")
		assert.NoFileExists(t, p.Scope)
	})

	t.Run("the template ships inert", func(t *testing.T) {
		root := t.TempDir()
		p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err)

		m, err := Load(p.TaskDir)
		require.NoError(t, err, "the shipped template must parse")
		assert.Equal(t, Meta{}, m, "every key is commented out, so a task nobody described carries no anchor")
	})

	t.Run("second round on an existing task", func(t *testing.T) {
		root := t.TempDir()
		_, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err)

		p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "02-after-fix"})
		require.NoError(t, err)
		assert.Equal(t, []string{"round_dir", "input_dir"}, p.Created, "the task above it was already there")
		assert.DirExists(t, p.InputDir)
		assert.DirExists(t, filepath.Join(root, "pr-123", "01-initial", InputDir), "the first round is untouched")
	})

	t.Run("an existing task.md is preserved", func(t *testing.T) {
		root := t.TempDir()
		_, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err)

		meta := filepath.Join(root, "pr-123", metaFile)
		require.NoError(t, os.WriteFile(meta, []byte("---\nbranch: feature/oauth\n---\n\nmine\n"), 0o600))

		p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "02-after-fix"})
		require.NoError(t, err)
		assert.NotContains(t, p.Created, "task_file")

		m, err := Load(p.TaskDir)
		require.NoError(t, err)
		assert.Equal(t, Meta{Branch: "feature/oauth"}, m, "what the caller wrote survives a second round")
	})

	t.Run("a round that has already run is refused", func(t *testing.T) {
		root := t.TempDir()
		p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(p.RoundDir, ManifestFile), []byte("{}"), 0o600))
		require.NoError(t, os.WriteFile(p.Scope, []byte("review the diff"), 0o600))

		_, err = Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already run")
		assert.FileExists(t, p.Scope, "the refused round keeps every artifact it had")
	})

	t.Run("a round claimed by a run that never finished is scaffolded again", func(t *testing.T) {
		root := t.TempDir()
		p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p.Scope, []byte("review the diff"), 0o600))
		// what an interrupted run leaves behind: the marker archive.New creates to claim the round, and
		// nothing written into it
		require.NoError(t, os.WriteFile(filepath.Join(p.RoundDir, ManifestFile), nil, 0o600))

		again, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err, "the caller's own input/ lives in this round, so it is not burned by an empty marker")
		assert.Empty(t, again.Created, "everything the round needs was already there")
		assert.Equal(t, "review the diff", readFile(t, p.Scope), "and what he wrote is untouched")
	})

	// archive.New refuses a marker that is not a regular file, so accepting one here hands the caller a
	// round the review path will not open — and the round it hands back names a write through the link
	t.Run("a round whose marker is a symlink is refused", func(t *testing.T) {
		for _, content := range []string{"the goal the caller wrote", ""} {
			root := t.TempDir()
			p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(p.Goal, []byte(content), 0o600))
			require.NoError(t, os.Symlink(p.Goal, filepath.Join(p.RoundDir, ManifestFile)))

			_, err = Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
			require.Error(t, err, "a run claiming this round would fill the marker in through the link")
			assert.Contains(t, err.Error(), "not a regular file")
			assert.Equal(t, content, readFile(t, p.Goal), "and the file it names is left exactly as it was")
		}
	})

	// the marker is written first and filled last, so the run that never came back may still have written
	// most of the archive. Scaffolding over it would hand the caller a round archive.New then refuses
	t.Run("a round an interrupted run already wrote artifacts into is refused", func(t *testing.T) {
		root := t.TempDir()
		p, err := Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p.Scope, []byte("review the diff"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(p.RoundDir, ManifestFile), nil, 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(p.RoundDir, "stages"), 0o750))
		stage := filepath.Join(p.RoundDir, "stages", "1-found.json")
		require.NoError(t, os.WriteFile(stage, []byte("what the dead run found"), 0o600))

		_, err = Scaffold(Round{TasksDir: root, Task: "pr-123", Run: "01-initial"})
		require.Error(t, err, "a second run here would leave one round holding two runs' artifacts")
		assert.Contains(t, err.Error(), "still holds what it wrote (stages)")
		assert.Contains(t, err.Error(), "open a new round")
		assert.Equal(t, "what the dead run found", readFile(t, stage), "and nothing was deleted to make the round usable")
		assert.Equal(t, "review the diff", readFile(t, p.Scope), "the input/ the message says copies across is intact")
	})
}

func TestCheckReclaim(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		msg     string
	}{
		{name: "the claim alone", entries: []string{ManifestFile}},
		{name: "the caller's input beside it", entries: []string{InputDir, ManifestFile}},
		{name: "a run's stage snapshots", entries: []string{InputDir, ManifestFile, "stages"}, msg: "(stages)"},
		{name: "per-agent tees", entries: []string{InputDir, ManifestFile, "agents"}, msg: "(agents)"},
		{name: "the decision log", entries: []string{InputDir, ManifestFile, "events.jsonl"}, msg: "(events.jsonl)"},
		{name: "a rendered report", entries: []string{InputDir, ManifestFile, ReportFile}, msg: "(" + ReportFile + ")"},
		{name: "findings without a manifest record", entries: []string{FindingsFile}, msg: "(" + FindingsFile + ")"},
		{name: "everything a dead run left", entries: []string{InputDir, ManifestFile, "agents", "events.jsonl", "prompts"},
			msg: "(agents, events.jsonl, prompts)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.entries {
				if filepath.Ext(name) == "" {
					require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o750))
					continue
				}
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
			}
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)

			err = CheckReclaim(dir, entries)
			if tc.msg == "" {
				require.NoError(t, err, "a round carrying nothing but the claim and the caller's own input is re-usable")
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg, "the message names what was found, so the caller can look at it")
			assert.Contains(t, err.Error(), dir)
		})
	}
}

func TestScaffold_containment(t *testing.T) {
	t.Run("a task symlinked out of the tasks root creates nothing at the target", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "evil")))

		_, err := Scaffold(Round{TasksDir: root, Task: "evil", Run: "01-initial"})
		require.Error(t, err, "the review path refuses this task, so creating one would be a task revmux cannot read")
		assert.Contains(t, err.Error(), "inside the tasks root", "the walk is anchored there and cannot leave it")
		assert.Equal(t, []string{"."}, treeOf(t, outside), "nothing of the layout was written out there")
	})

	t.Run("a round symlinked out of the task directory creates nothing at the target", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-1"), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "pr-1", "01-initial")))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01-initial"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a symlink")
		assert.Equal(t, []string{"."}, treeOf(t, outside), "input/ was never created through the link")
	})

	t.Run("a task symlinked to a sibling inside the tasks root is followed", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "real"), 0o750))
		require.NoError(t, os.Symlink("real", filepath.Join(root, "pr-1")))

		p, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01-initial"})
		require.NoError(t, err, "a link landing back inside the root is what options.taskDir accepts too")
		assert.DirExists(t, filepath.Join(root, "real", "01-initial", InputDir))
		assert.Equal(t, []string{"task_file", "round_dir", "input_dir"}, p.Created)
	})

	// task.md is the one file Scaffold writes, so it is the one entry a containment check on directories
	// never covers: a plain write follows a link at its last component straight out of the tasks root
	t.Run("a task.md symlinked out of the tasks root creates nothing at the target", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-1"), 0o750))
		require.NoError(t, os.Symlink(filepath.Join(outside, "pwned.md"), filepath.Join(root, "pr-1", metaFile)))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01-initial"})
		require.Error(t, err, "a dangling link is followed by the create, which writes the template out there")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.Equal(t, []string{"."}, treeOf(t, outside), "the template was never written through the link")
	})

	t.Run("a task.md symlinked onto an existing file leaves it alone", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		target := filepath.Join(outside, "notes.md")
		require.NoError(t, os.WriteFile(target, []byte("the caller's own file"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-1"), 0o750))
		require.NoError(t, os.Symlink(target, filepath.Join(root, "pr-1", metaFile)))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01-initial"})
		require.Error(t, err, "task.md is read back as this task's metadata, and that file is not it")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.Equal(t, "the caller's own file", readFile(t, target))
	})

	t.Run("an input symlinked out of the round creates nothing at the target", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		round := filepath.Join(root, "pr-1", "01-initial")
		require.NoError(t, os.MkdirAll(round, 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(round, InputDir)))

		p, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01-initial"})
		require.Error(t, err, "the caller would author his whole review context outside the tasks root")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.Empty(t, p.Scope, "no path outside the root is handed back to write through")
		assert.Equal(t, []string{"."}, treeOf(t, outside), "and nothing of the layout was written out there")
	})

	// a link landing back inside the tasks root passes containment, and points this round's reported paths
	// at another round — whose input/ the caller then writes his next scope over
	t.Run("a round symlinked to a sibling round is refused", func(t *testing.T) {
		root := t.TempDir()
		first := filepath.Join(root, "pr-1", "01-initial", InputDir)
		require.NoError(t, os.MkdirAll(first, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(first, ScopeFile), []byte("round 1"), 0o600))
		require.NoError(t, os.Symlink("01-initial", filepath.Join(root, "pr-1", "02-after-fix")))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "02-after-fix"})
		require.Error(t, err, "archive.New refuses this round, so scaffolding it produces one revmux cannot open")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.Equal(t, "round 1", readFile(t, filepath.Join(first, ScopeFile)), "the round it points at is untouched")
	})

	// archive.New walks down from the tasks root as nested os.Roots, which resolves a symlink target only
	// within the root and so refuses an absolute one outright. A resolve-then-compare here accepts it, and
	// `revmux new` would hand back a task the review path can never open
	t.Run("a task symlink with an absolute target is refused, the same way the review path refuses it", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real")
		require.NoError(t, os.MkdirAll(target, 0o750))
		require.NoError(t, os.Symlink(target, filepath.Join(root, "pr-1")))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01-initial"})
		require.Error(t, err, "an absolute target cannot be resolved inside the root, so it is not walked")
		assert.NoDirExists(t, filepath.Join(target, "01-initial"), "nothing of the round was written through it")
	})

	// the containment checks and the writes that follow them are separate operations, so a task directory
	// swapped for a symlink in between is followed by every write below it. Writing through a handle opened
	// on the directory the check accepted closes that window: the handle keeps pointing at what was checked
	t.Run("a task directory swapped for a symlink mid-scaffold is never written through", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		taskDir := filepath.Join(root, "pr-1")

		done := make(chan struct{})
		swapped := make(chan struct{})
		go func() {
			defer close(swapped)
			for {
				select {
				case <-done:
					return
				default:
				}
				_ = os.RemoveAll(taskDir)
				_ = os.Symlink(outside, taskDir)
				_ = os.Remove(taskDir)
				_ = os.Mkdir(taskDir, 0o750)
			}
		}()

		for i := range 300 {
			_, _ = Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "round-" + strconv.Itoa(i)})
		}
		close(done)
		<-swapped

		assert.Equal(t, []string{"."}, treeOf(t, outside), "no layout entry was ever created through the link")
	})

	t.Run("an input symlinked to another round's input is refused", func(t *testing.T) {
		root := t.TempDir()
		first := filepath.Join(root, "pr-1", "01-initial", InputDir)
		require.NoError(t, os.MkdirAll(first, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(first, ScopeFile), []byte("round 1"), 0o600))
		second := filepath.Join(root, "pr-1", "02-after-fix")
		require.NoError(t, os.MkdirAll(second, 0o750))
		require.NoError(t, os.Symlink(first, filepath.Join(second, InputDir)))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "02-after-fix"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a symlink")
		assert.Equal(t, "round 1", readFile(t, filepath.Join(first, ScopeFile)))
	})
}

// TestScaffold_created pins the JSON `revmux new` prints: the shipped skill reads `created` and looks the
// path up by that name, so a field whose entry names something other than its own json tag hands the
// caller a key that is not in the payload.
func TestScaffold_created(t *testing.T) {
	root := t.TempDir()
	p, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01-initial"})
	require.NoError(t, err)

	tags := map[string]bool{}
	for f := range reflect.TypeFor[Paths]().Fields() {
		tags[f.Tag.Get("json")] = true
	}
	for _, name := range p.Created {
		assert.True(t, tags[name], "created carries %q, which is not a json tag on Paths", name)
	}
	assert.Equal(t, []string{"task_dir", "task_file", "round_dir", "input_dir"}, p.Created,
		"a fresh task creates every one of them")
}

func TestScaffold_errors(t *testing.T) {
	tests := []struct {
		name string
		opts Round
		msg  string
	}{
		{name: "no tasks dir", opts: Round{Task: "pr-1", Run: "01"}, msg: "tasks directory is empty"},
		{name: "empty task", opts: Round{Task: "", Run: "01"}, msg: "--task is empty"},
		{name: "task with a separator", opts: Round{Task: "a/b", Run: "01"}, msg: "--task"},
		{name: "task climbing out", opts: Round{Task: "..", Run: "01"}, msg: "parent directory"},
		{name: "empty run", opts: Round{Task: "pr-1"}, msg: "--run is empty"},
		{name: "run with a separator", opts: Round{Task: "pr-1", Run: `a\b`}, msg: "--run"},
		{name: "run climbing out", opts: Round{Task: "pr-1", Run: "../elsewhere"}, msg: "--run"},
		{name: "run named after the task's own metadata", opts: Round{Task: "pr-1", Run: "task.md"}, msg: "is reserved"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			o := tc.opts
			if o.TasksDir == "" && tc.name != "no tasks dir" {
				o.TasksDir = root
			}
			_, err := Scaffold(o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)

			entries, readErr := os.ReadDir(root)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "a name is refused before anything is created")
		})
	}

	// every layout entry is refused when something that is not a directory holds its name: reporting a
	// path the caller cannot write into only surfaces later, as a review that reads no scope
	t.Run("a file where a layout directory belongs", func(t *testing.T) {
		tests := []struct {
			name string
			path func(root string) string
		}{
			{name: "task directory", path: func(root string) string { return filepath.Join(root, "pr-1") }},
			{name: "round directory", path: func(root string) string { return filepath.Join(root, "pr-1", "01") }},
			{name: "input directory", path: func(root string) string { return filepath.Join(root, "pr-1", "01", InputDir) }},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				root := t.TempDir()
				path := tc.path(root)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
				require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0o600))

				_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01"})
				require.Error(t, err)
				assert.Contains(t, err.Error(), path+" is not a directory")
			})
		}
	})

	t.Run("a directory where task.md belongs", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pr-1", metaFile), 0o750))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01"})
		require.Error(t, err, "a directory carrying that name is not the task.md a caller reads back")
		assert.Contains(t, err.Error(), "is a directory")
	})

	t.Run("an unwritable tasks root", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		root := filepath.Join(t.TempDir(), "ro")
		require.NoError(t, os.Mkdir(root, 0o500))

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create ")
	})

	// the round and its input/ are created after task.md, so an unwritable task directory is what
	// reaches those two calls at all
	t.Run("an unwritable task directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		root := t.TempDir()
		taskDir := filepath.Join(root, "pr-1")
		require.NoError(t, os.Mkdir(taskDir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(taskDir, metaFile), []byte("---\n---\n"), 0o600))
		require.NoError(t, os.Chmod(taskDir, 0o500))       //nolint:gosec // a directory nothing may be created in
		t.Cleanup(func() { _ = os.Chmod(taskDir, 0o750) }) //nolint:gosec // restores the temp dir for cleanup

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(taskDir, "01"))
	})

	t.Run("an unwritable round directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		root := t.TempDir()
		round := filepath.Join(root, "pr-1", "01")
		require.NoError(t, os.MkdirAll(round, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "pr-1", metaFile), []byte("---\n---\n"), 0o600))
		require.NoError(t, os.Chmod(round, 0o500))       //nolint:gosec // a directory nothing may be created in
		t.Cleanup(func() { _ = os.Chmod(round, 0o750) }) //nolint:gosec // restores the temp dir for cleanup

		_, err := Scaffold(Round{TasksDir: root, Task: "pr-1", Run: "01"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(round, InputDir))
	})
}

func TestHasRun(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, dir string)
		want  bool
	}{
		{name: "no manifest", plant: func(*testing.T, string) {}},
		{name: "the empty marker a claim leaves", plant: func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, ManifestFile), nil, 0o600))
		}},
		{name: "a directory carrying the name", plant: func(t *testing.T, dir string) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, ManifestFile), 0o750))
		}},
		{name: "a link to a file with content", plant: func(t *testing.T, dir string) {
			target := filepath.Join(dir, InputDir, GoalFile)
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o750))
			require.NoError(t, os.WriteFile(target, []byte("the goal the caller wrote"), 0o600))
			require.NoError(t, os.Symlink(filepath.Join(InputDir, GoalFile), filepath.Join(dir, ManifestFile)))
		}},
		{name: "the record a finished run wrote", want: true, plant: func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"run":"01"}`), 0o600))
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.plant(t, dir)
			assert.Equal(t, tc.want, HasRun(dir))
		})
	}

	t.Run("an absent directory", func(t *testing.T) {
		assert.False(t, HasRun(filepath.Join(t.TempDir(), "nope")))
	})
}

// a marker that is not a regular file is refused by every caller of the predicate rather than read for its
// size: the size is the link target's, and the write that fills the marker in truncates whatever it names
func TestCheckMarker(t *testing.T) {
	tests := []struct {
		name  string
		plant func(t *testing.T, path string)
		ran   bool
		msg   string
	}{
		{name: "the empty marker a claim leaves", plant: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, nil, 0o600))
		}},
		{name: "the record a finished run wrote", ran: true, plant: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"run":"01"}`), 0o600))
		}},
		{name: "a directory carrying the name", msg: "not a regular file", plant: func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0o750))
		}},
		{name: "a link to an empty file", msg: "not a regular file", plant: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "goal.md")
			require.NoError(t, os.WriteFile(target, nil, 0o600))
			require.NoError(t, os.Symlink(target, path))
		}},
		{name: "a link to a file with content", msg: "not a regular file", plant: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "goal.md")
			require.NoError(t, os.WriteFile(target, []byte("content"), 0o600))
			require.NoError(t, os.Symlink(target, path))
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ManifestFile)
			tc.plant(t, path)
			fi, err := os.Lstat(path)
			require.NoError(t, err)

			ran, err := CheckMarker(path, fi)
			if tc.msg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.msg)
				assert.Contains(t, err.Error(), path)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.ran, ran)
		})
	}
}

func TestRounds(t *testing.T) {
	taskDir := t.TempDir()
	ran := func(t *testing.T, name string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Join(taskDir, name), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(taskDir, name, ManifestFile), []byte("{}"), 0o600))
	}
	ran(t, "02-after-fix")
	ran(t, "01-initial")
	require.NoError(t, os.MkdirAll(filepath.Join(taskDir, "03-pending", InputDir), 0o750))
	claimed := filepath.Join(taskDir, "04-interrupted")
	require.NoError(t, os.MkdirAll(claimed, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(claimed, ManifestFile), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, metaFile), []byte("---\n---\n"), 0o600))

	got, err := Rounds(taskDir)
	require.NoError(t, err)
	assert.Equal(t, []string{"01-initial", "02-after-fix"}, got,
		"a round is one a run completed in, and the names come back in review order")

	t.Run("an absent task directory has no rounds and is not an error", func(t *testing.T) {
		got, err := Rounds(filepath.Join(t.TempDir(), "nope"))
		require.NoError(t, err, "the caller writes the task directory, and the first round's review precedes it")
		assert.Empty(t, got)
	})

	t.Run("an unreadable task directory is an error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		dir := filepath.Join(t.TempDir(), "pr-1")
		require.NoError(t, os.MkdirAll(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) //nolint:gosec // restores the temp dir so cleanup can remove it
		_, err := Rounds(dir)
		require.Error(t, err, "no rounds and unreadable are the opposite advice to a caller picking a --run name")
	})
}

// metaFile is a task's own metadata one level down, so the tasks root reserves no name at all: a directory
// called task.md there is a task a review really runs under, and omitting it from the enumeration would
// hide a task revmux new can still scaffold.
func TestList(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"pr-1", "pr-2"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o750))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o600))

	got, err := List(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"pr-1", "pr-2"}, got,
		"only directories are tasks, and the ids come back in a stable order")

	t.Run("no name is reserved at the tasks root", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, metaFile), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o600))

		got, err := List(dir)
		require.NoError(t, err)
		assert.Equal(t, []string{metaFile}, got, "a directory is a task whatever it is called; a file is not")
	})

	t.Run("an absent tasks root is a clean install, not a failure to read one", func(t *testing.T) {
		got, err := List(filepath.Join(t.TempDir(), "nope"))
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a relative link to a sibling is a task the review path runs under", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "pr-1"), 0o750))
		require.NoError(t, os.Symlink("pr-1", filepath.Join(dir, "alias")))

		got, err := List(dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"alias", "pr-1"}, got,
			"archive.New opens the alias, and omitting it is how a caller mints a second id for it")
	})

	t.Run("a link the archive cannot walk is left out", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "pr-1"), 0o750))
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "away")))
		require.NoError(t, os.Symlink(filepath.Join(dir, "pr-1"), filepath.Join(dir, "absolute")))

		got, err := List(dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"pr-1"}, got,
			"listing a task no review can open advertises one that cannot run")
	})

	t.Run("an unreadable tasks root is an error, never an empty list", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		dir := filepath.Join(t.TempDir(), "tasks")
		require.NoError(t, os.MkdirAll(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) //nolint:gosec // restores the temp dir so cleanup can remove it

		got, err := List(dir)
		require.Error(t, err, "no tasks and unreadable are the opposite advice to a caller matching an id")
		assert.Empty(t, got)
	})
}

func TestCheckContained(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "real"), 0o750))
	require.NoError(t, os.Symlink("real", filepath.Join(root, "sibling")))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "away")))

	assert.NoError(t, CheckContained(root, filepath.Join(root, "real")))
	assert.NoError(t, CheckContained(root, filepath.Join(root, "sibling")),
		"a relative link to a sibling inside the root is what the review path accepts too")

	err := CheckContained(root, filepath.Join(root, "away"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes the tasks root")

	err = CheckContained(root, filepath.Join(root, "nope"))
	require.Error(t, err, "a path that cannot be resolved is not a contained one")
}

func TestCheckRoundName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		msg   string
	}{
		{name: "plain", input: "01-initial"},
		{name: "a name close to the reserved one", input: "task.md-2"},
		{name: "carries the lexical rules", input: "a/b", msg: "path separator"},
		{name: "the task's own metadata", input: "task.md", msg: "is reserved"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckRoundName("--run", tc.input)
			if tc.msg == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
			assert.Contains(t, err.Error(), "--run")
		})
	}
}

func TestCheckName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		msg   string
	}{
		{name: "plain", input: "pr-123"},
		{name: "with digits and underscore", input: "01_initial"},
		{name: "dot inside", input: "v1.2"},
		{name: "empty", input: "", msg: "is empty"},
		{name: "absolute", input: "/etc/passwd", msg: "is absolute"},
		{name: "forward separator", input: "a/b", msg: "path separator"},
		{name: "backslash separator", input: `a\b`, msg: "path separator"},
		{name: "parent", input: "..", msg: "parent directory"},
		{name: "parent inside", input: "a..b", msg: "parent directory"},
		{name: "leading dot", input: ".hidden", msg: "starts with a dot"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckName("--task", tc.input)
			if tc.msg == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
			assert.Contains(t, err.Error(), "--task")
		})
	}
}

// treeOf lists every path under dir, relative and sorted, so a test can assert a whole directory was
// left alone rather than naming the files it expects not to appear.
func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		require.NoError(t, relErr)
		out = append(out, rel)
		return nil
	})
	require.NoError(t, err)
	slices.Sort(out)
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	return string(data)
}
