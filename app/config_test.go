package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

// testRun is the round every context fixture is written into: only its containment under the task is
// ever under test, never the name itself.
const testRun = "round-1"

func TestParseArgs_defaults(t *testing.T) {
	home := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(home, "cfg")})
	require.NoError(t, err)

	assert.Equal(t, "pr-1", o.Task)
	assert.Equal(t, 2*time.Minute, o.IdleTimeout)
	assert.Equal(t, 20*time.Minute, o.HardTimeout)
	assert.Equal(t, 30*time.Second, o.StaggerDelay)
	assert.Equal(t, 4, o.MaxParallel)
	assert.Equal(t, 6, o.VerifyGroups)
	assert.Equal(t, "dir", o.VerifyGroupBy, "a code review groups by directory unless the caller says otherwise")
	assert.Equal(t, "./.revmux/tasks", o.TasksDir)
	assert.Equal(t, "comprehensive", o.Profile)
	assert.Equal(t, 0, o.MinConfidence)
}

func TestParseArgs_cleanInstallHasNoZeroKnob(t *testing.T) {
	home := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(home, "cfg")})
	require.NoError(t, err)

	assert.Positive(t, o.IdleTimeout, "a zero idle timeout is a disabled watchdog")
	assert.Positive(t, o.HardTimeout, "a zero hard timeout is a disabled watchdog")
	assert.GreaterOrEqual(t, o.MaxParallel, 1, "a zero max-parallel is a zero-capacity semaphore")
	assert.GreaterOrEqual(t, o.VerifyGroups, 1)

	set, err := o.promptSet()
	require.NoError(t, err, "the default profile must resolve on a clean install")
	require.NotNil(t, set)
}

func TestParseArgs_lensesAcceptCommasAndRepetition(t *testing.T) {
	home := isolate(t)
	cfg := filepath.Join(home, "cfg")

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "comma separated", args: []string{"--lenses", "bugs,impl"}, want: []string{"bugs", "impl"}},
		{name: "repeated", args: []string{"--lenses", "bugs", "--lenses", "impl"}, want: []string{"bugs", "impl"}},
		{name: "mixed with spaces", args: []string{"--lenses", "bugs, impl", "--lenses", "docs"}, want: []string{"bugs", "impl", "docs"}},
		{name: "absent", args: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := parseArgs(append([]string{"--task", "pr-1", "--config-dir", cfg}, tt.args...))
			require.NoError(t, err)
			assert.Equal(t, tt.want, o.Lenses)
		})
	}
}

func TestParseArgs_runnersAcceptCommasAndRepetition(t *testing.T) {
	home := isolate(t)
	cfg := filepath.Join(home, "cfg")

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", cfg, "--runners", "claude, agy", "--runners", "codex"})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude", "agy", "codex"}, o.Runners)

	o, err = parseArgs([]string{"--task", "pr-1", "--config-dir", cfg})
	require.NoError(t, err)
	assert.Nil(t, o.Runners, "absent means no filter")
}

func TestParseArgs_configSubcommand(t *testing.T) {
	home := isolate(t)
	cfg := filepath.Join(home, "cfg")

	t.Run("the command word selects the catalog", func(t *testing.T) {
		o, err := parseArgs([]string{"--config-dir", cfg, "config"})
		require.NoError(t, err)
		assert.True(t, o.showConfig)
		assert.Equal(t, cfg, o.layers.user, "the command still resolves the layers it reports")
	})

	t.Run("a review still parses with no command word", func(t *testing.T) {
		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", cfg})
		require.NoError(t, err)
		assert.False(t, o.showConfig)
		assert.Equal(t, "pr-1", o.Task)
	})

	t.Run("only the command word selects it", func(t *testing.T) {
		o, err := parseArgs([]string{"--config-dir", cfg, "confgi"})
		require.NoError(t, err)
		assert.False(t, o.showConfig, "a typo must not print a catalog in place of the review that was asked for")
	})
}

func TestParseArgs_unknownFlag(t *testing.T) {
	isolate(t)

	_, err := parseArgs([]string{"--nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse arguments")
}

func TestParseArgs_precedenceAcrossFourLayers(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	writeConfig(t, user, "max-parallel = 9\nverify-groups = 3\nstagger-delay = 1s\nprofile = user-profile\n")
	writeConfig(t, filepath.Join(dir, projectDirName), "max-parallel = 7\nprofile = project-profile\n")

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--idle-timeout", "5m", "--max-parallel", "11"})
	require.NoError(t, err)

	assert.Equal(t, 11, o.MaxParallel, "the command line beats both files")
	assert.Equal(t, 5*time.Minute, o.IdleTimeout, "the command line beats the built-in default")
	assert.Equal(t, "project-profile", o.Profile, "the project layer beats the user layer")
	assert.Equal(t, 3, o.VerifyGroups, "a user key the project layer does not set survives")
	assert.Equal(t, time.Second, o.StaggerDelay, "layers merge per key, not per file")
	assert.Equal(t, 20*time.Minute, o.HardTimeout, "a key no layer sets keeps its built-in default")
}

func TestParseArgs_knobOriginsNameTheWinningLayer(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	writeConfig(t, user, "verify-groups = 3\nmax-parallel = 9\nprofile = user-profile\n")
	writeConfig(t, filepath.Join(dir, projectDirName), "max-parallel = 7\nprofile = project-profile\n")

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--idle-timeout", "5m"})
	require.NoError(t, err)

	want := map[string]string{
		"idle-timeout":    originFlag,
		"profile":         originProject,
		"max-parallel":    originProject,
		"verify-groups":   originUser,
		"hard-timeout":    originDefault,
		"stagger-delay":   originDefault,
		"tasks-dir":       originDefault,
		"auto-exit":       originDefault,
		"verify-group-by": originDefault,
	}
	assert.Equal(t, want, o.knobOrigins)
	assert.Len(t, o.knobOrigins, len(knobNames()), "every knob reports an origin")
}

func TestParseArgs_knobOriginIsNotFooledByStructDefaults(t *testing.T) {
	dir := isolate(t)

	// --profile passed with exactly its built-in value must still report as a flag
	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user"), "--profile", "focused"})
	require.NoError(t, err)
	assert.Equal(t, originFlag, o.knobOrigins["profile"])
	assert.Equal(t, originDefault, o.knobOrigins["verify-groups"])
}

func TestParseArgs_configDirOnTheProjectDirYieldsOneLayer(t *testing.T) {
	dir := isolate(t)
	project := filepath.Join(dir, projectDirName)
	writeConfig(t, project, "verify-groups = 5\n")

	t.Run("collapsed", func(t *testing.T) {
		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", project})
		require.NoError(t, err)
		assert.Empty(t, o.layers.project, "one directory must not load as two layers")
		assert.True(t, sameDir(o.layers.user, project))
		assert.Equal(t, 5, o.VerifyGroups)
		assert.Equal(t, originUser, o.knobOrigins["verify-groups"])
	})

	t.Run("distinct", func(t *testing.T) {
		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user")})
		require.NoError(t, err)
		assert.True(t, sameDir(o.layers.project, project), "the project layer is auto-detected in the working directory")
		assert.Equal(t, originProject, o.knobOrigins["verify-groups"])
	})
}

func TestParseArgs_absentProjectDirDropsTheLayer(t *testing.T) {
	dir := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user")})
	require.NoError(t, err)
	assert.Empty(t, o.layers.project)
	assert.NotEmpty(t, o.layers.user, "the user layer is a location, present whether or not it holds a file")
}

func TestParseArgs_defaultUserDirComesFromHome(t *testing.T) {
	dir := isolate(t)

	o, err := parseArgs([]string{"--task", "pr-1"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "home", ".config", "revmux"), o.layers.user)
}

func TestParseArgs_relativePathsFollowCwdNotWorkdir(t *testing.T) {
	dir := isolate(t)
	writeConfig(t, filepath.Join(dir, projectDirName), "verify-groups = 5\n")
	elsewhere := t.TempDir()

	o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", filepath.Join(dir, "user"), "--workdir", elsewhere})
	require.NoError(t, err)

	assert.True(t, sameDir(o.layers.project, filepath.Join(dir, projectDirName)),
		"the project layer is auto-detected in the working directory, never under --workdir")
	assert.Equal(t, 5, o.VerifyGroups)
	assert.Equal(t, "./.revmux/tasks", o.TasksDir, "--tasks-dir resolves against the same cwd")
}

func TestParseArgs_badConfigValue(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")

	t.Run("unparseable value", func(t *testing.T) {
		writeConfig(t, user, "max-parallel = lots\n")
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lots", "a bad value is rejected, never silently defaulted")
	})

	t.Run("unknown key", func(t *testing.T) {
		writeConfig(t, user, "no-such-knob = 1\n")
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no-such-knob")
	})

	t.Run("meta flag rejected as a key", func(t *testing.T) {
		writeConfig(t, user, "task = pr-9\n")
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.Error(t, err, "a no-ini flag is not a config key")
	})
}

func TestParseArgs_verifyGroupBy(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")

	t.Run("source is accepted", func(t *testing.T) {
		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--verify-group-by", "source"})
		require.NoError(t, err)
		assert.Equal(t, "source", o.VerifyGroupBy)
		assert.Equal(t, originFlag, o.knobOrigins["verify-group-by"])
	})

	t.Run("an unknown value is rejected on the command line", func(t *testing.T) {
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user, "--verify-group-by", "agent"})
		require.Error(t, err, "an unknown mode must not silently fall back to grouping by directory")
		assert.Contains(t, err.Error(), "agent")
		assert.Contains(t, err.Error(), "dir")
	})

	t.Run("an unknown value is rejected in a config file", func(t *testing.T) {
		writeConfig(t, user, "verify-group-by = agent\n")
		_, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.Error(t, err, "the INI layer parses as defaults, which must still validate the choice")
		assert.Contains(t, err.Error(), "agent")
	})

	t.Run("a config file supplies it like any other knob", func(t *testing.T) {
		writeConfig(t, user, "verify-group-by = source\n")
		o, err := parseArgs([]string{"--task", "pr-1", "--config-dir", user})
		require.NoError(t, err)
		assert.Equal(t, "source", o.VerifyGroupBy)
		assert.Equal(t, originUser, o.knobOrigins["verify-group-by"])
	})
}

func TestDefaultConfig_holdsEveryKnobCommentedOut(t *testing.T) {
	body := string(defaultConfig)
	fields := map[string]reflect.StructField{}
	for f := range reflect.TypeFor[options]().Fields() {
		if long := f.Tag.Get("long"); long != "" {
			fields[long] = f
		}
	}

	for _, name := range knobNames() {
		want := "# " + name + " = " + fields[name].Tag.Get("default")
		assert.Contains(t, body, want, "knob %s must appear in the template with its default", name)
	}

	keys, err := iniKeys(filepath.Join("defaults", configFileName))
	require.NoError(t, err)
	assert.Empty(t, keys, "the template ships fully commented out, so --init can replace it on upgrade")
}

func TestKnobNames_iniNameMatchesLongName(t *testing.T) {
	for f := range reflect.TypeFor[options]().Fields() {
		if f.Tag.Get("long") == "" || f.Tag.Get("no-ini") != "" {
			continue
		}
		assert.Equal(t, f.Tag.Get("long"), f.Tag.Get("ini-name"), "field %s: a config key must match its flag", f.Name)
		assert.NotEmpty(t, f.Tag.Get("default"), "field %s: a knob with no default resolves to a zero value", f.Name)
	}
	assert.Equal(t, []string{"idle-timeout", "hard-timeout", "stagger-delay", "max-parallel",
		"verify-groups", "verify-group-by", "tasks-dir", "auto-exit", "profile"}, knobNames())
}

func TestResolveContext_shapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	full := roundInput(t, root, "full", task.ScopeFile, task.GoalFile, task.ProfileFile)
	require.NoError(t, os.MkdirAll(filepath.Join(full, task.ContextDir), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(full, task.ContextDir, "ticket.md"), []byte("t"), 0o600))

	scopeOnly := roundInput(t, root, "scope-only", task.ScopeFile)

	roundInput(t, root, "no-scope", task.GoalFile)

	emptyScope := roundInput(t, root, "empty-scope")
	require.NoError(t, os.WriteFile(filepath.Join(emptyScope, task.ScopeFile), nil, 0o600))

	dirScope := roundInput(t, root, "dir-scope")
	require.NoError(t, os.Mkdir(filepath.Join(dirScope, task.ScopeFile), 0o750))

	emptyCtx := roundInput(t, root, "empty-context", task.ScopeFile)
	require.NoError(t, os.Mkdir(filepath.Join(emptyCtx, task.ContextDir), 0o750))

	fileCtx := roundInput(t, root, "file-context", task.ScopeFile)
	require.NoError(t, os.WriteFile(filepath.Join(fileCtx, task.ContextDir), []byte("x"), 0o600))

	noRound := filepath.Join(root, "no-round")
	require.NoError(t, os.MkdirAll(noRound, 0o750))

	tests := []struct {
		name    string
		task    string
		wantErr string
		check   func(t *testing.T, rc reviewContext)
	}{
		{name: "full round", task: "full", check: func(t *testing.T, rc reviewContext) {
			assert.Equal(t, full, rc.InputDir)
			assert.Equal(t, filepath.Join(full, task.ScopeFile), rc.Scope)
			assert.Equal(t, filepath.Join(full, task.GoalFile), rc.Goal)
			assert.Equal(t, filepath.Join(full, task.ProfileFile), rc.Profile)
			assert.Equal(t, filepath.Join(full, task.ContextDir), rc.Context)
			assert.Equal(t, filepath.Join(root, "full"), rc.TaskDir,
				"the task directory, since it is what the prior-round inventory enumerates")
		}},
		{name: "scope only", task: "scope-only", check: func(t *testing.T, rc reviewContext) {
			assert.Equal(t, filepath.Join(scopeOnly, task.ScopeFile), rc.Scope)
			assert.Empty(t, rc.Goal, "an absent goal is not an error")
			assert.Empty(t, rc.Profile)
			assert.Empty(t, rc.Context)
		}},
		{name: "empty context dir", task: "empty-context", check: func(t *testing.T, rc reviewContext) {
			assert.Empty(t, rc.Context, "an empty directory tells an agent less than the placeholder")
		}},
		{name: "missing scope", task: "no-scope", wantErr: "required and must not be empty"},
		{name: "empty scope", task: "empty-scope", wantErr: "required and must not be empty"},
		{name: "scope is a directory", task: "dir-scope", wantErr: "is a directory, want a file"},
		{name: "context is a file", task: "file-context", wantErr: "is a file, want a directory"},
		{name: "missing task directory", task: "nope", wantErr: "task directory"},
		{name: "round the caller never prepared", task: "no-round", wantErr: filepath.Join(noRound, testRun, task.InputDir)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := options{Task: tt.task, Run: testRun, TasksDir: root}
			rc, err := o.resolveContext()
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.check(t, rc)
		})
	}

	// a round is stat'd before its input/ is read, and a stat nobody could answer must fail rather than
	// read as "the round is not there" and send the caller to `revmux new` for a round he already has
	t.Run("an unstattable round is an error, never a pass", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads inside a directory with no execute bit")
		}
		roundInput(t, root, "blocked", task.ScopeFile)
		taskDir := filepath.Join(root, "blocked")
		require.NoError(t, os.Chmod(taskDir, 0o600))       // readable, not searchable: a name inside cannot be stat'd
		t.Cleanup(func() { _ = os.Chmod(taskDir, 0o750) }) //nolint:gosec // restores the temp dir for cleanup

		_, err := options{Task: "blocked", Run: testRun, TasksDir: root}.resolveContext()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stat "+filepath.Join(taskDir, testRun))
		assert.NotContains(t, err.Error(), "revmux new", "it is unreadable, not absent")
	})

	// a round nobody prepared has no input/ either, so reporting it as an empty scope names a path two
	// levels inside a directory that is not there — and archive.New, which says it properly, never runs
	t.Run("a round the caller never created names the call that creates one", func(t *testing.T) {
		_, err := options{Task: "no-round", Run: testRun, TasksDir: root}.resolveContext()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "revmux new --task no-round --run "+testRun)
		assert.Contains(t, err.Error(), filepath.Join(noRound, testRun, task.InputDir))
	})

	t.Run("a file where the round belongs is not a round", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "file-round"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "file-round", testRun), []byte("x"), 0o600))

		_, err := options{Task: "file-round", Run: testRun, TasksDir: root}.resolveContext()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a directory")
	})

	t.Run("tasks dir outside the working directory", func(t *testing.T) {
		away := roundInput(t, outside, "away", task.ScopeFile)
		o := options{Task: "away", Run: testRun, TasksDir: outside}
		rc, err := o.resolveContext()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(away, task.ScopeFile), rc.Scope)
	})
}

func TestResolveContext_rejectsEscapingNames(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	roundInput(t, root, "ok", task.ScopeFile)
	roundInput(t, outside, "elsewhere", task.ScopeFile)
	require.NoError(t, os.Symlink(filepath.Join(outside, "elsewhere"), filepath.Join(root, "linked")))

	tests := []struct {
		name    string
		opts    options
		wantErr string
	}{
		{name: "parent escape", opts: options{Task: "../escape", Run: testRun, TasksDir: root}, wantErr: "path separator"},
		{name: "bare parent", opts: options{Task: "..", Run: testRun, TasksDir: root}, wantErr: "references a parent directory"},
		{name: "separator", opts: options{Task: "a/b", Run: testRun, TasksDir: root}, wantErr: "path separator"},
		{name: "absolute", opts: options{Task: outside, Run: testRun, TasksDir: root}, wantErr: "absolute"},
		{name: "empty", opts: options{Task: "", Run: testRun, TasksDir: root}, wantErr: "--task is empty"},
		{name: "leading dot", opts: options{Task: ".hidden", Run: testRun, TasksDir: root}, wantErr: "starts with a dot"},
		{name: "run escapes too", opts: options{Task: "ok", Run: "../out", TasksDir: root}, wantErr: "--run"},
		{name: "run omitted", opts: options{Task: "ok", TasksDir: root}, wantErr: "revmux new --task ok"},
		{name: "symlink out of the root", opts: options{Task: "linked", Run: testRun, TasksDir: root},
			wantErr: "escapes the tasks root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.opts.resolveContext()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestResolveContext_absolutePathsAndNoFileOpened(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads an unreadable file, so the no-open proof needs an unprivileged user")
	}

	root := t.TempDir()
	dir := roundInput(t, root, "pr-1", task.ScopeFile, task.GoalFile)
	require.NoError(t, os.Chmod(filepath.Join(dir, task.ScopeFile), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, task.ScopeFile), 0o600) })

	o := options{Task: "pr-1", Run: testRun, TasksDir: filepath.Join(root, "..", filepath.Base(root))}
	rc, err := o.resolveContext()
	require.NoError(t, err, "resolution stats context files and never opens one")

	for name, path := range map[string]string{"scope": rc.Scope, "goal": rc.Goal, "taskdir": rc.TaskDir, "workdir": rc.WorkDir} {
		assert.True(t, filepath.IsAbs(path), "%s must be absolute, got %q", name, path)
	}
	for name, val := range rc.vars() {
		assert.True(t, filepath.IsAbs(val) || val == placeholderNone, "{{%s}} is %q, want a path or the placeholder", name, val)
	}

	_, readErr := os.ReadFile(rc.Scope)
	require.Error(t, readErr, "the fixture must be unreadable, or the assertion above proves nothing")
}

func TestResolveContext_workDir(t *testing.T) {
	root := t.TempDir()
	roundInput(t, root, "pr-1", task.ScopeFile)
	work := t.TempDir()

	t.Run("explicit", func(t *testing.T) {
		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root, WorkDir: work}.resolveContext()
		require.NoError(t, err)
		assert.True(t, sameDir(rc.WorkDir, work))
	})

	t.Run("defaults to the process working directory", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Equal(t, cwd, rc.WorkDir)
	})

	t.Run("missing", func(t *testing.T) {
		_, err := options{Task: "pr-1", Run: testRun, TasksDir: root, WorkDir: filepath.Join(work, "nope")}.resolveContext()
		require.Error(t, err)
	})

	t.Run("not a directory", func(t *testing.T) {
		file := filepath.Join(work, "file")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
		_, err := options{Task: "pr-1", Run: testRun, TasksDir: root, WorkDir: file}.resolveContext()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})
}

func TestReviewContext_vars(t *testing.T) {
	rc := reviewContext{Scope: "/t/scope.md", WorkDir: "/repo"}
	assert.Equal(t, prompt.Vars{
		"SCOPE": "/t/scope.md", "GOAL": placeholderNone, "PROFILE": placeholderNone,
		"CONTEXT": placeholderNone, "WORKDIR": "/repo",
	}, rc.vars())

	full := reviewContext{Scope: "/t/scope.md", Goal: "/t/goal.md", Profile: "/t/profile.md",
		Context: "/t/context", WorkDir: "/repo"}
	assert.Equal(t, prompt.Vars{
		"SCOPE": "/t/scope.md", "GOAL": "/t/goal.md", "PROFILE": "/t/profile.md",
		"CONTEXT": "/t/context", "WORKDIR": "/repo",
	}, full.vars())
}

func TestOptions_checkNames(t *testing.T) {
	t.Run("an omitted run names the round and how to create one", func(t *testing.T) {
		err := options{Task: "pr-1"}.checkNames()
		require.Error(t, err, "there is no default: the round is where the caller's own context lives")
		assert.Contains(t, err.Error(), "revmux new --task pr-1 --run <round>")
		assert.Contains(t, err.Error(), task.InputDir)
	})

	t.Run("both names go through the one validator", func(t *testing.T) {
		tests := []struct {
			name string
			opts options
			want string
		}{
			{name: "task empty", opts: options{Run: "round-1"}, want: "--task is empty"},
			{name: "task separator", opts: options{Task: "a/b", Run: "round-1"}, want: "--task"},
			{name: "run parent", opts: options{Task: "pr-1", Run: ".."}, want: "--run"},
			{name: "run hidden", opts: options{Task: "pr-1", Run: ".hidden"}, want: "starts with a dot"},
			// a round is a direct child of the task directory, so one carrying the task's own file name
			// is read as its metadata
			{name: "run named task.md", opts: options{Task: "pr-1", Run: "task.md"}, want: "is reserved"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := tt.opts.checkNames()
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
		require.NoError(t, options{Task: "pr-1", Run: "round-1"}.checkNames())
	})
}

func TestOptions_executorOpts(t *testing.T) {
	clk := &mocks.ClockMock{}
	o := options{IdleTimeout: time.Minute, HardTimeout: time.Hour, PreserveAPIKey: true, WorkDir: "/ignored"}

	got := o.executorOpts(reviewContext{WorkDir: "/resolved"}, clk)
	assert.Equal(t, executor.Opts{IdleTimeout: time.Minute, HardTimeout: time.Hour,
		WorkDir: "/resolved", PreserveAPIKey: true, Clock: clk}, got,
		"the subprocess runs where {{WORKDIR}} points, never where the raw flag does")
}

func TestOptions_promptOptsUsesResolvedLayers(t *testing.T) {
	o := options{layers: configLayers{project: "/p", user: "/u"}}
	assert.Equal(t, prompt.LoadOpts{ProjectDir: "/p", UserDir: "/u"}, o.promptOpts())
}

func TestOptions_promptSetUnknownProfile(t *testing.T) {
	_, err := options{Profile: "nope"}.promptSet()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve profile")
}

// the config was the last leaf on the init path still written by name. os.ReadFile reports a dangling link
// as an absent file and os.WriteFile then creates its target, so the two symlink cases are the write
// escaping ./.revmux/ entirely and an existing file outside it being truncated.
func TestOptions_initConfig(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, projectDirName, configFileName)

	// initConfig writes through the same handle the prompt tree is materialized with, so every case
	// here opens one rather than handing it a path
	initCfg := func(t *testing.T, out io.Writer) (string, error) {
		t.Helper()
		w, err := newTreeWriter(filepath.Join(dir, projectDirName))
		require.NoError(t, err)
		defer func() { _ = w.close() }()
		return options{}.initConfig(w, out)
	}

	out := &strings.Builder{}
	got, err := initCfg(t, out)
	require.NoError(t, err)
	assert.Equal(t, path, got, "the payload reports this path rather than composing it a second time")
	written, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	assert.Equal(t, defaultConfig, written)
	assert.Contains(t, out.String(), "wrote")

	t.Run("comment-only file is replaced", func(t *testing.T) {
		require.NoError(t, os.WriteFile(path, []byte("# only a comment\n"), 0o600))
		out := &strings.Builder{}
		got, err := initCfg(t, out)
		require.NoError(t, err)
		assert.Equal(t, path, got)
		written, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, defaultConfig, written)
	})

	t.Run("customized file is left alone", func(t *testing.T) {
		require.NoError(t, os.WriteFile(path, []byte("verify-groups = 42\n"), 0o600))
		out := &strings.Builder{}
		got, err := initCfg(t, out)
		require.NoError(t, err)
		assert.Equal(t, path, got, "a file left alone is still the path the caller reports")
		written, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, "verify-groups = 42\n", string(written))
		assert.Contains(t, out.String(), "customized")
	})

	t.Run("a dangling symlink at the config is refused rather than written through", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "elsewhere.ini")
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Symlink(outside, path))

		_, err := initCfg(t, io.Discard)
		require.Error(t, err, "a link is an entry, not a missing file")
		assert.Contains(t, err.Error(), "not a regular file")
		assert.NoFileExists(t, outside, "and its target sits outside the project, so nothing may land there")
	})

	t.Run("a symlink to an existing file is refused rather than truncated", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "notes.txt")
		// no `key = value` line, so iniKeysFrom finds nothing set and the rewrite branch is the one taken
		require.NoError(t, os.WriteFile(outside, []byte("# just a note\n"), 0o600))
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Symlink(outside, path))

		_, err := initCfg(t, io.Discard)
		require.Error(t, err)
		kept, readErr := os.ReadFile(outside) //nolint:gosec // path built from t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "# just a note\n", string(kept), "the file the link points at is left exactly as it was")
	})
}

func TestOptions_dumpDefaults(t *testing.T) {
	dir := t.TempDir()
	out := &strings.Builder{}

	require.NoError(t, options{DumpDefaults: dir}.dumpDefaults(out))
	for _, rel := range []string{"prompts/profiles/focused.md", "prompts/synthesis.md", "prompts/verify.md",
		"lenses/bugs.md", "lenses/adversarial.md"} {
		assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(rel)))
	}
	assert.NotContains(t, out.String(), "defaults/", "the tree extracts without its embedded wrapper directory")

	t.Run("an existing file is never overwritten", func(t *testing.T) {
		path := filepath.Join(dir, "lenses", "bugs.md")
		require.NoError(t, os.WriteFile(path, []byte("mine"), 0o600))
		out := &strings.Builder{}
		require.NoError(t, options{DumpDefaults: dir}.dumpDefaults(out))
		body, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
		require.NoError(t, err)
		assert.Equal(t, "mine", string(body))
		assert.Contains(t, out.String(), "already present")
	})

	t.Run("unwritable target", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes into a read-only directory")
		}
		blocked := filepath.Join(t.TempDir(), "ro")
		require.NoError(t, os.Mkdir(blocked, 0o500))
		err := options{DumpDefaults: filepath.Join(blocked, "out")}.dumpDefaults(&strings.Builder{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dump defaults")
	})

	// this command takes a user-named destination, so it writes through the same treeWriter init does: one
	// rule for what an entry already there means, and one for where a write may land
	t.Run("a dangling symlink is left alone rather than written through", func(t *testing.T) {
		dest := t.TempDir()
		outside := filepath.Join(t.TempDir(), "elsewhere.md")
		require.NoError(t, os.MkdirAll(filepath.Join(dest, "lenses"), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(dest, "lenses", "bugs.md")))

		out := &strings.Builder{}
		require.NoError(t, options{DumpDefaults: dest}.dumpDefaults(out))
		assert.NoFileExists(t, outside, "the target sits outside the destination and the shipped text is not written there")
		assert.Contains(t, out.String(), filepath.Join(dest, "lenses", "bugs.md")+" already present")
		assert.FileExists(t, filepath.Join(dest, "lenses", "adversarial.md"), "the rest of the tree still extracts")
	})

	t.Run("a symlinked subdirectory leaving the destination is refused", func(t *testing.T) {
		dest := t.TempDir()
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(dest, "lenses")))

		err := options{DumpDefaults: dest}.dumpDefaults(&strings.Builder{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(dest, "lenses"))

		left, err := os.ReadDir(outside)
		require.NoError(t, err)
		assert.Empty(t, left, "every shipped lens would land here if the link were followed")
	})

	// --init writes what resolved; this one stays the escape hatch for the shipped text, which is the only
	// way a customized lens can be diffed against it
	t.Run("a project override does not change what is extracted", func(t *testing.T) {
		proj := isolate(t)
		mine := filepath.Join(proj, projectDirName, "lenses", "bugs.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(mine), 0o750))
		require.NoError(t, os.WriteFile(mine, []byte("---\ndescription: mine\n---\nonly crashes\n"), 0o600))

		dumped := filepath.Join(proj, "dumped")
		o, err := parseArgs([]string{"--dump-defaults", dumped})
		require.NoError(t, err)
		r := newRunOpts(t, o)
		require.Equal(t, 0, run(r.opts()))

		embedded, err := fs.ReadFile(prompt.Defaults(), "lenses/bugs.md")
		require.NoError(t, err)
		assert.Equal(t, string(embedded), readFile(t, filepath.Join(dumped, "lenses", "bugs.md")))
		assert.Empty(t, r.stdout.String(), "only --init reports a payload; this one narrates on stderr")
	})
}

func TestIniKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	body := "[Application Options]\n# comment = 1\n; other = 2\n\nverify-groups = 5\n  Max-Parallel = 7 \nnot-a-pair\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	keys, err := iniKeys(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"verify-groups", "max-parallel"}, keys)

	missing, err := iniKeys(filepath.Join(dir, "absent"))
	require.NoError(t, err)
	assert.Nil(t, missing, "an absent config file is not an error")
}

// isolate points the process at a temp directory for both the working directory and the home lookup, so
// no test can read or write the developer's own config.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	return dir
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, configFileName), []byte(body), 0o600))
}

func roundInput(t *testing.T, root, taskID string, files ...string) string {
	t.Helper()
	dir := filepath.Join(root, taskID, testRun, task.InputDir)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("content of "+f), 0o600))
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // path built from t.TempDir
	require.NoError(t, err)
	return string(data)
}

func TestResolveContext_projectProfileFallback(t *testing.T) {
	// the project layer resolves against the process working directory, so each case chdirs into its own
	// tree and reads the cwd back: a temp dir under /var is really /private/var on macOS, and a path
	// built from t.TempDir() would not match the one filepath.Abs produces inside projectDir
	setup := func(t *testing.T, body string) (root, cwd string) {
		t.Helper()
		t.Chdir(t.TempDir())
		cwd, err := os.Getwd()
		require.NoError(t, err)
		if body != "" {
			require.NoError(t, os.MkdirAll(filepath.Join(cwd, projectDirName), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(cwd, projectDirName, task.ProfileFile),
				[]byte(body), 0o600))
		}
		return t.TempDir(), cwd
	}

	t.Run("a round with no profile inherits the project one", func(t *testing.T) {
		root, cwd := setup(t, "# project calibration\n")
		roundInput(t, root, "pr-1", task.ScopeFile)

		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cwd, projectDirName, task.ProfileFile), rc.ProfileSource)
		assert.Equal(t, filepath.Join(root, "pr-1", testRun, task.ProfileSnapshotFile), rc.Profile,
			"PROFILE must name the round's own snapshot, never a path outside the round")
	})

	t.Run("a round with a non-empty profile wins over the project one", func(t *testing.T) {
		root, _ := setup(t, "# project calibration\n")
		input := roundInput(t, root, "pr-1", task.ScopeFile, task.ProfileFile)

		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Empty(t, rc.ProfileSource, "nothing is snapshotted when the round carries a non-empty profile")
		assert.Equal(t, filepath.Join(input, task.ProfileFile), rc.Profile)
	})

	t.Run("an empty round profile inherits the project one", func(t *testing.T) {
		root, cwd := setup(t, "# project calibration\n")
		input := roundInput(t, root, "pr-1", task.ScopeFile, task.ProfileFile)
		require.NoError(t, os.WriteFile(filepath.Join(input, task.ProfileFile), nil, 0o600))

		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cwd, projectDirName, task.ProfileFile), rc.ProfileSource)
		assert.Equal(t, filepath.Join(root, "pr-1", testRun, task.ProfileSnapshotFile), rc.Profile,
			"an empty round file is absent for precedence, so PROFILE must name the project snapshot")
	})

	// the project file is never opened when the round has a non-empty profile, so a broken one must not fail an
	// invocation that would never have read it
	t.Run("a broken project profile is irrelevant when the round carries a non-empty profile", func(t *testing.T) {
		root, cwd := setup(t, "")
		require.NoError(t, os.MkdirAll(filepath.Join(cwd, projectDirName, task.ProfileFile), 0o750))
		input := roundInput(t, root, "pr-1", task.ScopeFile, task.ProfileFile)

		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Empty(t, rc.ProfileSource)
		assert.Equal(t, filepath.Join(input, task.ProfileFile), rc.Profile)
	})

	t.Run("a broken project profile fails a round that would inherit it", func(t *testing.T) {
		root, cwd := setup(t, "")
		require.NoError(t, os.MkdirAll(filepath.Join(cwd, projectDirName, task.ProfileFile), 0o750))
		roundInput(t, root, "pr-1", task.ScopeFile)

		_, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "want a file")
	})

	t.Run("no project profile leaves the placeholder", func(t *testing.T) {
		root, _ := setup(t, "")
		roundInput(t, root, "pr-1", task.ScopeFile)

		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Empty(t, rc.ProfileSource)
		assert.Empty(t, rc.Profile)
	})

	t.Run("an empty project profile is no project profile", func(t *testing.T) {
		root, cwd := setup(t, "")
		require.NoError(t, os.MkdirAll(filepath.Join(cwd, projectDirName), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(cwd, projectDirName, task.ProfileFile), nil, 0o600))
		roundInput(t, root, "pr-1", task.ScopeFile)

		rc, err := options{Task: "pr-1", Run: testRun, TasksDir: root}.resolveContext()
		require.NoError(t, err)
		assert.Empty(t, rc.ProfileSource, "empty and absent already mean the same thing inside a round")
		assert.Empty(t, rc.Profile)
	})
}
