package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/prompt"
)

func TestParseArgs_initSubcommand(t *testing.T) {
	t.Run("the command word selects materialization", func(t *testing.T) {
		isolate(t)
		o, err := parseArgs([]string{"init"})
		require.NoError(t, err)
		assert.True(t, o.showInit)
		assert.False(t, o.showNew)
		assert.False(t, o.showConfig)
	})

	t.Run("a review still parses with no command word", func(t *testing.T) {
		isolate(t)
		o, err := parseArgs([]string{"--task", "pr-1", "--run", "01-initial"})
		require.NoError(t, err)
		assert.False(t, o.showInit)
	})

	t.Run("nothing is created during parsing", func(t *testing.T) {
		dir := isolate(t)
		_, err := parseArgs([]string{"init"})
		require.NoError(t, err)
		assert.Equal(t, []string{"."}, treeOf(t, dir), "Execute records the selection, run does the writing")
	})
}

func TestRun_init(t *testing.T) {
	t.Run("a fresh directory gets the settings template and every prompt file", func(t *testing.T) {
		dir := isolate(t)
		p, r := initialize(t)

		assert.Contains(t, r.stdout.String(), "\n  \"dir\"", "indented, since a human reads it occasionally")
		assert.Equal(t, filepath.Join(dir, projectDirName), p.Dir)
		assert.Equal(t, filepath.Join(p.Dir, configFileName), p.Config)
		assert.Equal(t, string(defaultConfig), readFile(t, p.Config), "settings ship commented out, never resolved")

		want := embeddedTree(t)
		got := make([]string, 0, len(p.Files))
		for _, f := range p.Files {
			rel, err := filepath.Rel(p.Dir, f.Path)
			require.NoError(t, err)
			got = append(got, filepath.ToSlash(rel))

			assert.True(t, f.Created, "%s was already there in a fresh directory", f.Path)
			assert.Equal(t, prompt.LayerEmbedded, f.Layer, f.Path)
			embedded, err := fs.ReadFile(prompt.Defaults(), filepath.ToSlash(rel))
			require.NoError(t, err)
			assert.Equal(t, string(embedded), readFile(t, f.Path), f.Path)
		}
		slices.Sort(got)
		assert.Equal(t, want, got, "every file the tree resolved is now local")
	})

	// the one that catches a front-matter-stripped write: a materialized tree that will not load is an
	// init that broke the project it just initialized
	t.Run("the materialized tree loads and every file resolves locally", func(t *testing.T) {
		isolate(t)
		p, _ := initialize(t)

		set, err := prompt.Load(prompt.LoadOpts{ProjectDir: p.Dir})
		require.NoError(t, err, "the written tree must load with no fallback to help it")
		for _, org := range set.Provenance() {
			assert.Equal(t, prompt.LayerProject, org.Layer, "%s fell back, so the tree is not self-sufficient", org.Path)
		}
		assert.Equal(t, []string{"agy-only", "claude-only", "codex-only", "comprehensive", "expert", "final", "focused", "grill-me", "triage", "trio"}, set.ProfileNames())
		for _, l := range set.Lenses() {
			assert.NotEmpty(t, l.Description, "lens %s lost its front matter on the way to disk", l.Name)
		}
	})

	t.Run("a second run creates nothing", func(t *testing.T) {
		dir := isolate(t)
		first, _ := initialize(t)
		was := map[string]string{}
		for _, f := range first.Files {
			was[f.Path] = readFile(t, f.Path)
		}
		before := treeOf(t, filepath.Join(dir, projectDirName))

		second, _ := initialize(t)
		assert.Equal(t, before, treeOf(t, filepath.Join(dir, projectDirName)))
		require.Len(t, second.Files, len(first.Files))
		for _, f := range second.Files {
			assert.False(t, f.Created, f.Path)
			assert.Equal(t, prompt.LayerProject, f.Layer, "%s now resolves from the tree init wrote", f.Path)
			assert.Equal(t, was[f.Path], readFile(t, f.Path), f.Path)
		}
	})

	t.Run("an existing project file is left byte-identical", func(t *testing.T) {
		dir := isolate(t)
		const mine = "---\ndescription: my own take on bugs\n---\nreport only what is provably wrong\n"
		require.NoError(t, os.MkdirAll(filepath.Join(dir, projectDirName, "lenses"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, projectDirName, "lenses", "bugs.md"), []byte(mine), 0o600))

		p, _ := initialize(t)
		path := filepath.Join(p.Dir, "lenses", "bugs.md")
		assert.Equal(t, mine, readFile(t, path), "an override is the reason the file is there")

		f := filesByPath(p.Files)[path]
		assert.False(t, f.Created)
		assert.Equal(t, prompt.LayerProject, f.Layer)
	})

	t.Run("a customized config is left alone", func(t *testing.T) {
		dir := isolate(t)
		writeConfig(t, filepath.Join(dir, projectDirName), "verify-groups = 42\n")

		p, _ := initialize(t)
		assert.Equal(t, "verify-groups = 42\n", readFile(t, p.Config))
		assert.NotEmpty(t, p.Files, "the prompt tree still materializes beside it")
	})

	t.Run("a user-layer file is materialized, not the embedded one", func(t *testing.T) {
		dir := isolate(t)
		const userBugs = "---\ndescription: the user's own bugs lens\n---\nchase only crashes\n"
		cfg := filepath.Join(dir, "cfg")
		require.NoError(t, os.MkdirAll(filepath.Join(cfg, "lenses"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(cfg, "lenses", "bugs.md"), []byte(userBugs), 0o600))

		p, _ := initialize(t, "--config-dir", cfg)
		path := filepath.Join(p.Dir, "lenses", "bugs.md")
		assert.Equal(t, userBugs, readFile(t, path), "init writes what resolved, not what is embedded")

		f := filesByPath(p.Files)[path]
		assert.True(t, f.Created)
		assert.Equal(t, prompt.LayerUser, f.Layer)
	})

	t.Run("nothing is written outside the project directory", func(t *testing.T) {
		dir := isolate(t)
		initialize(t)

		for _, rel := range treeOf(t, dir) {
			if rel == "." || rel == projectDirName {
				continue
			}
			assert.True(t, strings.HasPrefix(rel, projectDirName+string(filepath.Separator)),
				"%s is outside ./.revmux, and a read-only invocation must not touch the user's home either", rel)
		}
	})

	t.Run("a malformed prompt tree exits 2", func(t *testing.T) {
		dir := isolate(t)
		cfg := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(cfg, "lenses"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(cfg, "lenses", "bugs.md"), []byte("---\nnope:\n---\nbody\n"), 0o600))

		o, err := parseArgs([]string{"--config-dir", cfg, "init"})
		require.NoError(t, err)
		r := newRunOpts(t, o)
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String(), "half a payload would parse as a complete one")
		assert.Contains(t, r.stderr.String(), "load prompts")
		assert.NoDirExists(t, filepath.Join(dir, projectDirName), "the tree is resolved before anything is written")
	})

	t.Run("an unreadable prompt file exits 2", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a file with no permissions")
		}
		dir := isolate(t)
		cfg := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(cfg, "lenses"), 0o750))
		sealed := filepath.Join(cfg, "lenses", "bugs.md")
		require.NoError(t, os.WriteFile(sealed, []byte("---\ndescription: mine\n---\nbody\n"), 0o000))

		o, err := parseArgs([]string{"--config-dir", cfg, "init"})
		require.NoError(t, err)
		r := newRunOpts(t, o)
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String())
		assert.Contains(t, r.stderr.String(), "load prompts",
			"an override that cannot be read is not one to silently fall back past")
		assert.NoDirExists(t, filepath.Join(dir, projectDirName))
	})

	t.Run("an unwritable prompt directory exits 2", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		dir := isolate(t)
		writeConfig(t, filepath.Join(dir, projectDirName), "verify-groups = 42\n")
		require.NoError(t, os.Mkdir(filepath.Join(dir, projectDirName, "lenses"), 0o500))

		o, err := parseArgs([]string{"init"})
		require.NoError(t, err)
		r := newRunOpts(t, o)
		assert.Equal(t, 2, run(r.opts()))
		assert.Empty(t, r.stdout.String())
		assert.Contains(t, r.stderr.String(), filepath.Join(dir, projectDirName, "lenses", "adversarial.md"),
			"the failure names the file that could not be written, not merely that a write failed")
	})

	// a destination that cannot be looked at is not a destination that is absent, and treating the two the
	// same reports a write failure for a read that was refused
	t.Run("a destination that cannot be read is named as such", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		dir := isolate(t)
		lenses := filepath.Join(dir, projectDirName, "lenses")
		require.NoError(t, os.MkdirAll(lenses, 0o750))
		require.NoError(t, os.Chmod(lenses, 0o600))       // readable but not traversable
		t.Cleanup(func() { _ = os.Chmod(lenses, 0o750) }) //nolint:gosec // restores the temp dir for cleanup

		o, err := parseArgs([]string{"init"})
		require.NoError(t, err)
		r := newRunOpts(t, o)
		assert.Equal(t, 2, run(r.opts()))
		assert.Contains(t, r.stderr.String(), "read "+filepath.Join(lenses, "adversarial.md"))
	})

	// containment at the leaf alone is not containment: os.Lstat dereferences every directory above the
	// file it is handed, so a link occupying a directory name would send the whole tree wherever it points
	// while the reported paths still named ./.revmux/.
	// the leaf-link case this pairs with is TestTreeWriter_write's — the loader reads every project-layer
	// .md, so a link it can see fails the run two steps before materialization runs at all
	t.Run("a symlinked subdirectory is refused rather than followed", func(t *testing.T) {
		dir := isolate(t)
		outside := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, projectDirName), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, projectDirName, "lenses")))

		o, err := parseArgs([]string{"init"})
		require.NoError(t, err)
		r := newRunOpts(t, o)
		assert.Equal(t, 2, run(r.opts()), "a tree that cannot be materialized in place is a failed init")
		assert.Empty(t, r.stdout.String(), "no payload claims paths under ./.revmux/ that were written elsewhere")
		assert.Contains(t, r.stderr.String(), filepath.Join(dir, projectDirName, "lenses"))

		left, err := os.ReadDir(outside)
		require.NoError(t, err)
		assert.Empty(t, left, "every shipped lens would land here if the link were followed")
	})

	t.Run("an unwritable stdout exits 2", func(t *testing.T) {
		isolate(t)
		o, err := parseArgs([]string{"init"})
		require.NoError(t, err)
		r := newRunOpts(t, o)
		ro := r.opts()
		ro.stdout = failingWriter{}
		assert.Equal(t, 2, run(ro))
		assert.Contains(t, r.stderr.String(), "write init paths")
	})
}

// --init is the flag spelling of the subcommand and shares its implementation, so what it writes and what
// it reports both have to be the subcommand's own.
func TestRun_initFlag(t *testing.T) {
	t.Run("the flag materializes exactly what the subcommand does", func(t *testing.T) {
		byFlag, byCommand := materialized(t, "--init"), materialized(t, "init")
		assert.Equal(t, byCommand, byFlag, "two materializations of one tree would drift")
		assert.Contains(t, byFlag, "lenses/bugs.md", "the prompt tree, not the settings template alone")
		assert.Equal(t, string(defaultConfig), byFlag[configFileName])
	})

	t.Run("the payload is JSON on stdout with nothing on stderr", func(t *testing.T) {
		dir := isolate(t)
		p, r := runInit(t, "--init")

		assert.Empty(t, r.stderr.String(), "the prose --init used to print landed on stderr; the payload does not")
		assert.Contains(t, r.stdout.String(), "\n  \"dir\"")
		assert.Equal(t, filepath.Join(dir, projectDirName), p.Dir)
		assert.Equal(t, filepath.Join(p.Dir, configFileName), p.Config)

		require.NotEmpty(t, p.Files)
		for _, f := range p.Files {
			assert.True(t, f.Created, f.Path)
			assert.Equal(t, prompt.LayerEmbedded, f.Layer, f.Path)
		}
	})
}

// initialize runs `revmux init` through the parser rather than a hand-built options value, so the
// project layer is whatever is on disk at the moment of the call — which is what makes a second run
// resolve from the tree the first one wrote.
func initialize(t *testing.T, args ...string) (initPaths, *runHarness) {
	t.Helper()
	return runInit(t, append(args, "init")...)
}

// runInit drives one materialization end to end. Both spellings go through it, so the test asserting they
// agree cannot pass by exercising two harnesses that differ.
func runInit(t *testing.T, args ...string) (initPaths, *runHarness) {
	t.Helper()
	o, err := parseArgs(args)
	require.NoError(t, err)
	require.True(t, o.showInit || o.Init, "neither spelling of init was selected by %v", args)

	r := newRunOpts(t, o)
	require.Equal(t, 0, run(r.opts()))
	assert.Empty(t, r.stderr.String(), "the paths are the whole output")

	var p initPaths
	require.NoError(t, json.Unmarshal([]byte(r.stdout.String()), &p), "the caller model parses this")
	return p, r
}

// materialized runs init in its own isolated project and returns the tree it left, keyed by the path
// relative to ./.revmux/ so two runs in two temp directories compare by content rather than by location.
func materialized(t *testing.T, args ...string) map[string]string {
	t.Helper()
	dir := isolate(t)
	runInit(t, args...)

	root := filepath.Join(dir, projectDirName)
	out := map[string]string{}
	for _, rel := range treeOf(t, root) {
		path := filepath.Join(root, rel)
		fi, err := os.Stat(path)
		require.NoError(t, err)
		if fi.IsDir() {
			continue
		}
		out[filepath.ToSlash(rel)] = readFile(t, path)
	}
	return out
}

func filesByPath(files []initFile) map[string]initFile {
	out := make(map[string]initFile, len(files))
	for _, f := range files {
		out[f.Path] = f
	}
	return out
}

// embeddedTree lists the shipped prompt files, so the materialized set is compared against the tree
// itself rather than against a count that a new lens silently invalidates.
func embeddedTree(t *testing.T) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(prompt.Defaults(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out = append(out, p)
		return nil
	})
	require.NoError(t, err)
	slices.Sort(out)
	return out
}
