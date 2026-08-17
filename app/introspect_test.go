package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/archive"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

func TestOptions_knobs(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	writeConfig(t, user, "verify-groups = 3\n")

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--stagger-delay", "5s"})
	require.NoError(t, err)

	got := map[string]knob{}
	for _, k := range o.knobs() {
		got[k.Name] = k
	}

	require.Len(t, got, len(knobNames()), "every knob is reported")
	assert.Equal(t, knob{Name: "stagger-delay", Value: "5s", Source: originFlag}, got["stagger-delay"])
	assert.Equal(t, knob{Name: "hard-timeout", Value: (20 * time.Minute).String(), Source: originDefault}, got["hard-timeout"])
	assert.Equal(t, knob{Name: "verify-groups", Value: 3, Source: originUser}, got["verify-groups"])
	assert.Equal(t, knob{Name: "max-parallel", Value: 4, Source: originDefault}, got["max-parallel"])
	assert.Equal(t, knob{Name: "profile", Value: "comprehensive", Source: originDefault}, got["profile"])

	t.Run("no meta flag is reported as a knob", func(t *testing.T) {
		for _, k := range o.knobs() {
			assert.NotContains(t, []string{"task", "run", "markdown", "config-dir", "version"}, k.Name)
		}
	})
}

func TestOptions_catalogReportsTheResolvedTree(t *testing.T) {
	cfg := t.TempDir()
	lenses := filepath.Join(cfg, "lenses")
	require.NoError(t, os.MkdirAll(lenses, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(lenses, "bugs.md"),
		[]byte("---\ndescription: my own take on bugs\n---\nlook for bugs\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(lenses, "perf.md"),
		[]byte("---\ndescription: hot paths and allocations\n---\nlook for slowness\n"), 0o600))

	o := options{layers: configLayers{user: cfg}}
	set, err := prompt.Load(o.promptOpts())
	require.NoError(t, err)

	got := map[string]string{}
	for _, l := range o.catalog(set).Lenses {
		got[l.Name] = l.Description
	}

	assert.Equal(t, "my own take on bugs", got["bugs"], "an override is different text, never the default's summary")
	assert.Equal(t, "hot paths and allocations", got["perf"], "an added lens is composable, so it must be listed")
	assert.Contains(t, got, "tests", "overriding one lens does not orphan the others")
}

func TestOptions_catalogVocabulariesComeFromPrompt(t *testing.T) {
	set, err := prompt.Load(prompt.LoadOpts{})
	require.NoError(t, err)

	// compared against the accessors rather than a literal: a literal here would only assert that two
	// hardcoded lists agree, which is the drift the accessors exist to prevent
	c := options{}.catalog(set)
	assert.Equal(t, prompt.Executors(), c.Vocabulary.Executors)
	assert.Equal(t, prompt.Efforts(), c.Vocabulary.Efforts)
}

func TestOptions_catalogStages(t *testing.T) {
	set, err := prompt.Load(prompt.LoadOpts{})
	require.NoError(t, err)

	stages := map[string]stageInfo{}
	for _, s := range (options{}).catalog(set).Stages {
		stages[s.Name] = s
	}

	require.Len(t, stages, 2)
	for _, name := range []string{"synthesis", "verify"} {
		st, stErr := set.Stage(name)
		require.NoError(t, stErr)
		assert.Equal(t, stageInfo{Name: name, Description: st.Description,
			Executor: st.Executor, Model: st.Model, Effort: st.Effort}, stages[name])
		assert.NotEmpty(t, stages[name].Description, "a stage with no description hides which model judges the findings")
	}

	// the shipped stage files author no runner, and "" is outside the executor vocabulary: emitting it
	// puts a value no caller can act on where the catalog promises a resolved one
	raw, err := json.Marshal((options{}).catalog(set).Stages)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"executor"`,
		"an unauthored stage runner is omitted rather than reported empty")
}

// a caller picks a profile, so the runner it reports must be the one that profile resolves — the
// top-level entry describes only what a profile naming no override falls back to
func TestOptions_catalogProfileStages(t *testing.T) {
	set, err := prompt.Load(prompt.LoadOpts{})
	require.NoError(t, err)

	profiles := map[string][]stageRunner{}
	for _, p := range (options{}).catalog(set).Profiles {
		profiles[p.Name] = p.Stages
	}

	for name, stages := range profiles {
		require.Len(t, stages, 2, "%s reports every stage, not only the ones it overrides", name)
	}

	assert.Equal(t, []stageRunner{
		{Name: "synthesis", Executor: "codex", Model: "gpt-5.6-sol", Effort: "high"},
		{Name: "verify", Executor: "codex", Model: "gpt-5.6-sol", Effort: "high"},
	}, profiles["codex-only"])

	for _, st := range profiles["comprehensive"] {
		assert.Equal(t, "claude", st.Executor, "%s: a profile naming no override keeps the stage file's runner", st.Name)
	}
}

// the base runner is the only way anything outside app/prompt can tell which binary a --lenses run
// needs, so a profile whose roster and stages all override away from it is the case that matters
func TestOptions_catalogProfileRunner(t *testing.T) {
	cfg := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cfg, "prompts", "profiles"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "prompts", "profiles", "custom.md"),
		[]byte("---\ndescription: codex base, claude everywhere else\nmodel: codex/gpt-5.6-sol:high\n"+
			"agents:\n  - {name: a, lenses: [bugs], model: claude/opus:high}\n"+
			"stages:\n  synthesis: claude/opus:high\n  verify: claude/opus:high\n---\nbody\n"), 0o600))

	o := options{layers: configLayers{user: cfg}}
	set, err := prompt.Load(o.promptOpts())
	require.NoError(t, err)

	var got profileInfo
	for _, p := range o.catalog(set).Profiles {
		if p.Name == "custom" {
			got = p
		}
	}
	require.Equal(t, "custom", got.Name)
	assert.Equal(t, stageRunner{Executor: "codex", Model: "gpt-5.6-sol", Effort: "high"}, got.Runner,
		"the profile's own model, not what its roster or stages override to")
	for _, st := range got.Stages {
		assert.Equal(t, "claude", st.Executor, st.Name)
	}
	for _, a := range got.Roster {
		assert.Equal(t, "claude", a.Executor, a.Name)
	}

	// preflight reads this out of the marshaled payload, so the field has to survive encoding under
	// the name the script looks for
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var wire struct {
		Runner struct {
			Executor string `json:"executor"`
			Name     string `json:"name"`
		} `json:"runner"`
	}
	require.NoError(t, json.Unmarshal(raw, &wire))
	assert.Equal(t, "codex", wire.Runner.Executor)
	assert.Empty(t, wire.Runner.Name, "the base runner names no stage")
}

// an unreadable tasks root reported as no tasks at all is the advice that mints a duplicate id, the same
// failure an unreported meta_error produces one level down.
func TestOptions_paths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"pr-1", "pr-2"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o750))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o600))
	work := t.TempDir()

	o := options{TasksDir: root, WorkDir: work, layers: configLayers{project: "/p", user: "/u"}}
	got := o.paths()

	assert.Equal(t, root, got.TasksDir)
	assert.Equal(t, work, got.WorkDir)
	assert.Equal(t, "/u", got.ConfigDir)
	assert.Equal(t, "/p", got.ProjectDir)
	assert.Equal(t, []taskInfo{{ID: "pr-1", Rounds: []string{}}, {ID: "pr-2", Rounds: []string{}}}, got.Tasks,
		"only directories are tasks, and a --run collides with an existing round")

	// the collapse empties layers.project for provenance while the file stays where it is, so reading
	// the layer here would report no fallback on an invocation that still uses one
	t.Run("the profile fallback is reported and survives --config-dir ./.revmux", func(t *testing.T) {
		dir := isolate(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, projectDirName), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, projectDirName, task.ProfileFile),
			[]byte("# calibration"), 0o600))

		got := options{TasksDir: root, layers: configLayers{}}.paths()
		assert.Equal(t, filepath.Join(dir, projectDirName, task.ProfileFile), got.ProfileFallback)
		assert.Empty(t, got.ProjectDir, "the layer is collapsed, and the fallback is reported regardless")
	})

	t.Run("no project profile reports no fallback", func(t *testing.T) {
		isolate(t)
		got := options{TasksDir: root}.paths()
		assert.Empty(t, got.ProfileFallback)
		assert.Empty(t, got.ProfileFallbackError)
	})

	// reported as absent, this describes an inheriting round as uncalibrated when it would refuse to
	// start — the same wrong advice tasks_error and workdir_error exist to prevent one level up
	t.Run("a profile that will not resolve is an error rather than an absence", func(t *testing.T) {
		dir := isolate(t)
		require.NoError(t, os.MkdirAll(filepath.Join(dir, projectDirName, task.ProfileFile), 0o750))

		got := options{TasksDir: root}.paths()
		assert.Empty(t, got.ProfileFallback)
		assert.Contains(t, got.ProfileFallbackError, "want a file")
	})

	t.Run("a relative tasks root resolves against the working directory", func(t *testing.T) {
		dir := isolate(t)
		got := options{TasksDir: "./.revmux/tasks"}.paths()
		assert.Equal(t, filepath.Join(dir, ".revmux", "tasks"), got.TasksDir)
		assert.Empty(t, got.Tasks, "a clean install has no tasks and is not an error")
	})

	t.Run("an unresolvable workdir falls back to the raw value and says why", func(t *testing.T) {
		got := options{TasksDir: root, WorkDir: filepath.Join(root, "absent")}.paths()
		assert.Equal(t, filepath.Join(root, "absent"), got.WorkDir)
		assert.Contains(t, got.WorkDirError, "absent",
			"a workdir that will not resolve is a review that dies later, not a resolved path")
	})

	t.Run("an unreadable tasks root is reported, never as an empty one", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		blocked := filepath.Join(t.TempDir(), "tasks")
		require.NoError(t, os.MkdirAll(blocked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) }) //nolint:gosec // restores the temp dir for cleanup

		got := options{TasksDir: blocked}.paths()
		assert.Empty(t, got.Tasks)
		assert.Contains(t, got.TasksError, blocked)
	})
}

func TestOptions_tasks(t *testing.T) {
	root := t.TempDir()
	writeMeta := func(t *testing.T, id, body string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Join(root, id), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, id, "task.md"), []byte(body), 0o600))
	}
	round := func(t *testing.T, id, name string, claimed bool) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Join(root, id, name, task.InputDir), 0o750))
		if claimed {
			require.NoError(t, os.WriteFile(filepath.Join(root, id, name, task.ManifestFile), []byte("{}"), 0o600))
		}
	}

	writeMeta(t, "pr-123", "---\ndescription: OAuth token exchange rework\nurl: https://github.com/umputun/revmux/pull/123\n"+
		"branch: feature/oauth\nbase: 4ed3259\n---\n\nprose the caller wrote\n")
	round(t, "pr-123", "01-initial", true)
	round(t, "pr-123", "02-after-fix", true)
	round(t, "pr-123", "03-pending", false)

	writeMeta(t, "pr-9", "---\ndescription: only a description\n---\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bare"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o600))

	got, err := options{TasksDir: root}.tasks(root)
	require.NoError(t, err)

	require.Equal(t, []taskInfo{
		{ID: "bare", Rounds: []string{}},
		{ID: "pr-123", Meta: task.Meta{Description: "OAuth token exchange rework",
			URL: "https://github.com/umputun/revmux/pull/123", Branch: "feature/oauth", Base: "4ed3259"},
			Rounds: []string{"01-initial", "02-after-fix"}},
		{ID: "pr-9", Meta: task.Meta{Description: "only a description"}, Rounds: []string{}},
	}, got, "tasks come back in directory order, so a caller reading the list sees a stable one")

	t.Run("the anchors marshal under their front-matter names", func(t *testing.T) {
		data, err := json.Marshal(got[1])
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"pr-123","description":"OAuth token exchange rework",
			"url":"https://github.com/umputun/revmux/pull/123","branch":"feature/oauth","base":"4ed3259",
			"rounds":["01-initial","02-after-fix"]}`, string(data))
	})

	t.Run("a task with no task.md reports empty anchors, never a missing entry", func(t *testing.T) {
		data, err := json.Marshal(got[0])
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"bare","description":"","url":"","branch":"","base":"","rounds":[]}`, string(data),
			"the shape is stable, so a caller does not branch on which keys are present")
	})

	t.Run("an unparseable task.md is listed with the reason its anchors are empty", func(t *testing.T) {
		tests := []struct {
			name, id, body, want string
		}{
			{name: "unknown key", id: "broken-key", body: "---\nnope: unknown key\n---\n", want: "field nope not found"},
			{name: "malformed yaml", id: "broken-yaml", body: "---\ndescription: [unterminated\n---\n", want: "front matter"},
			{name: "unterminated block", id: "broken-block", body: "---\ndescription: x\n\nno closing marker\n",
				want: "never closed"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				writeMeta(t, tc.id, tc.body)
				got, tasksErr := options{TasksDir: root}.tasks(root)
				require.NoError(t, tasksErr)
				idx := slices.IndexFunc(got, func(ti taskInfo) bool { return ti.ID == tc.id })
				require.GreaterOrEqual(t, idx, 0, "a task omitted here is one a caller mints a second id for")
				assert.Equal(t, task.Meta{}, got[idx].Meta)
				assert.Contains(t, got[idx].MetaError, tc.want,
					"empty anchors alone read as a task nobody described, which is the opposite advice")
			})
		}
	})

	t.Run("an unreadable task.md is reported the same way", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "unreadable", "task.md"), 0o750))
		got, tasksErr := options{TasksDir: root}.tasks(root)
		require.NoError(t, tasksErr)
		idx := slices.IndexFunc(got, func(ti taskInfo) bool { return ti.ID == "unreadable" })
		require.GreaterOrEqual(t, idx, 0)
		assert.Contains(t, got[idx].MetaError, "read ")
	})

	t.Run("a round claimed by a run that never finished is not a round", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "burned", "01-initial"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "burned", "01-initial", task.ManifestFile), nil, 0o600))

		got, tasksErr := options{TasksDir: root}.tasks(root)
		require.NoError(t, tasksErr)
		idx := slices.IndexFunc(got, func(ti taskInfo) bool { return ti.ID == "burned" })
		require.GreaterOrEqual(t, idx, 0)
		assert.Empty(t, got[idx].Rounds, "the marker carries nothing, so that round is one to re-run")
	})

	// a task directory may be a relative symlink to a sibling, and both options.taskDir and archive.New
	// accept one: `revmux new --task alias` scaffolds under it and the round is written there. A catalog
	// omitting the id is the one that mints a second id for a task already in flight
	t.Run("a task reached through a symlink inside the tasks root is listed", func(t *testing.T) {
		require.NoError(t, os.Symlink("pr-9", filepath.Join(root, "alias")))

		got, tasksErr := options{TasksDir: root}.tasks(root)
		require.NoError(t, tasksErr)
		idx := slices.IndexFunc(got, func(ti taskInfo) bool { return ti.ID == "alias" })
		require.GreaterOrEqual(t, idx, 0, "a review runs under this id, so a caller composing one has to see it")
		assert.Equal(t, task.Meta{Description: "only a description"}, got[idx].Meta,
			"it is the same task under a second name, and the anchors say so")
	})

	t.Run("a task symlink the review path cannot walk is not listed", func(t *testing.T) {
		outside := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(outside, "01-initial"), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "away")))

		got, tasksErr := options{TasksDir: root}.tasks(root)
		require.NoError(t, tasksErr)
		idx := slices.IndexFunc(got, func(ti taskInfo) bool { return ti.ID == "away" })
		assert.Equal(t, -1, idx, "listing a task archive.New refuses advertises a review that cannot run")
	})

	t.Run("an absent tasks root is a clean install", func(t *testing.T) {
		got, tasksErr := options{}.tasks(filepath.Join(root, "absent"))
		require.NoError(t, tasksErr, "a tasks root nobody has written yet is not a failure to read one")
		assert.Empty(t, got)
	})

	t.Run("a task whose rounds cannot be read is listed with the reason", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		dir := filepath.Join(root, "sealed")
		require.NoError(t, os.MkdirAll(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) }) //nolint:gosec // restores the temp dir for cleanup

		got, tasksErr := options{TasksDir: root}.tasks(root)
		require.NoError(t, tasksErr, "one unreadable task is not an unreadable tasks root")
		idx := slices.IndexFunc(got, func(ti taskInfo) bool { return ti.ID == "sealed" })
		require.GreaterOrEqual(t, idx, 0)
		assert.Empty(t, got[idx].Rounds)
		assert.Contains(t, got[idx].RoundsError, dir,
			"no rounds and unreadable are the opposite advice to a caller picking a --run name")
	})

	// /revmux self reads both commands, so a task present in one and absent from the other is a
	// disagreement with nothing on stdout to explain it. This drives the aggregate rather than comparing
	// the catalog against the enumerator it is built from, which is true by construction
	t.Run("the catalog and revmux stats report the same task set", func(t *testing.T) {
		got, tasksErr := options{TasksDir: root}.tasks(root)
		require.NoError(t, tasksErr)
		ids := make([]string, 0, len(got))
		for _, ti := range got {
			ids = append(ids, ti.ID)
		}
		assert.Subset(t, ids, []string{"bare", "pr-123", "alias", "burned", "sealed"},
			"every task this tree carries is named, aliases and unreadable ones included")
		assert.NotContains(t, ids, "away", "a task the review path cannot open is advertised by neither")
		assert.NotContains(t, ids, "stray.txt")

		corpus, statsErr := archive.CollectStats(archive.StatsQuery{TasksDir: root})
		require.NoError(t, statsErr)
		var doc statsDoc
		data, marshalErr := json.Marshal(corpus)
		require.NoError(t, marshalErr)
		require.NoError(t, json.Unmarshal(data, &doc))
		assert.Equal(t, ids, taskIDs(doc), "one enumerator, so the two commands cannot disagree about what a task is")
	})
}
