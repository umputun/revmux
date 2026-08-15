package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/task"
)

// taskName is what every test calls its task directory, since only its containment inside the tasks root
// is ever under test and never the name itself.
const taskName = "pr-123"

// the round layout, spelled out here rather than imported: these tests are what pins the on-disk names,
// so reading them from the same constants the implementation joins its paths from would assert nothing.
// Every test file in this package uses these rather than app/task's, so the pinning is not half-done.
const (
	inputDir        = "input"
	manifestFile    = "manifest.json"
	findingsFile    = "findings.json"
	scopeFile       = "scope.md"
	metaFile        = "task.md"
	eventsFile      = "events.jsonl"
	foundFile       = "stages/1-found.json"
	synthesizedFile = "stages/2-synthesized.json"
	verifiedFile    = "stages/3-verified.json"
)

func TestNew(t *testing.T) {
	t.Run("rejects a run name that cannot be one path component", func(t *testing.T) {
		tests := []struct {
			name string
			run  string
			want string
		}{
			{name: "empty", run: "", want: "is empty"},
			{name: "absolute", run: "/etc", want: "is absolute"},
			{name: "separator", run: "a/b", want: "path separator"},
			{name: "parent", run: "..", want: "parent directory"},
			{name: "hidden", run: ".hidden", want: "starts with a dot"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := New(task.Round{TasksDir: t.TempDir(), Task: taskName, Run: tt.run})
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("no tasks directory", func(t *testing.T) {
		_, err := New(task.Round{Task: taskName, Run: "round-1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tasks directory is empty")
	})

	t.Run("rejects a task name that cannot be one path component", func(t *testing.T) {
		tests := []struct {
			name string
			task string
			want string
		}{
			{name: "empty", task: "", want: "is empty"},
			{name: "absolute", task: "/etc", want: "is absolute"},
			{name: "separator", task: "a/b", want: "path separator"},
			{name: "parent", task: "..", want: "parent directory"},
			{name: "hidden", task: ".hidden", want: "starts with a dot"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := New(task.Round{TasksDir: t.TempDir(), Task: tt.task, Run: "round-1"})
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("a task directory that is not there is an error, never one revmux authors", func(t *testing.T) {
		root := t.TempDir()

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err, "the whole task is the caller's, and he did not write this one")
		assert.Contains(t, err.Error(), "open task directory")
		assert.NoDirExists(t, filepath.Join(root, taskName))
	})

	t.Run("a tasks root that is not there is an error too", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "absent")

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "open tasks directory")
		assert.NoDirExists(t, root)
	})

	t.Run("a round symlink landing back inside the task directory is rejected too", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		kept := filepath.Join(round, "report.md")
		require.NoError(t, os.WriteFile(kept, []byte("the round that went badly"), 0o600))
		require.NoError(t, os.Symlink("01-initial", filepath.Join(filepath.Dir(round), "02-after-fix")))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "02-after-fix"})
		require.Error(t, err, "containment is satisfied here, so only refusing the link keeps 01-initial safe")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.NoFileExists(t, filepath.Join(round, manifestFile), "the earlier round was never claimed")

		data, readErr := os.ReadFile(kept) //nolint:gosec // path built from t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "the round that went badly", string(data), "nor were its artifacts truncated")
	})

	t.Run("a second round beside an existing one is accepted", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		second := filepath.Join(filepath.Dir(round), "02-after-fix")
		require.NoError(t, os.MkdirAll(filepath.Join(second, inputDir), 0o750))

		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "02-after-fix"})
		require.NoError(t, err, "a task accumulates rounds side by side")
		assert.Equal(t, second, a.dir)
	})

	t.Run("a symlinked task directory landing inside the tasks root is followed", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real")
		require.NoError(t, os.MkdirAll(filepath.Join(target, "round-1", inputDir), 0o750))
		require.NoError(t, os.Symlink("real", filepath.Join(root, taskName)))

		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err, "a task directory may legitimately be a link, as long as it stays in the root")
		assert.FileExists(t, filepath.Join(target, "round-1", manifestFile))
		assert.Equal(t, "round-1", filepath.Base(a.dir))
	})

	t.Run("a task symlink with an absolute target is refused even inside the tasks root", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real")
		require.NoError(t, os.MkdirAll(filepath.Join(target, "round-1", inputDir), 0o750))
		require.NoError(t, os.Symlink(target, filepath.Join(root, taskName)))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err, "an absolute target cannot be resolved inside the root, so it is not walked")
		assert.NoFileExists(t, filepath.Join(target, "round-1", manifestFile))
	})

	t.Run("a symlinked task directory pointing outside the tasks root is refused", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(outside, "round-1", inputDir), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(root, taskName)))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.Error(t, err, "the whole chain is anchored at the tasks root, so this cannot be walked into")
		assert.NoFileExists(t, filepath.Join(outside, "round-1", manifestFile), "nothing was written out there")
	})
}

// TestNew_roundLayout covers the layout a round carries its own caller context in: the round is a direct
// child of the task directory and holds the input/ the caller filled, so the review it ran is auditable
// from the round alone rather than from a task-level scope.md a later round overwrites.
func TestNew_roundLayout(t *testing.T) {
	t.Run("a round holding only input/ is accepted", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")

		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.NoError(t, err, "a round with its input/ in place is all revmux ever needs")
		assert.Equal(t, round, a.dir, "the round is a direct child of the task directory")
		assert.FileExists(t, filepath.Join(round, manifestFile), "the marker claiming the round for this run")
		assert.DirExists(t, filepath.Join(round, inputDir), "what the caller wrote is left exactly as it was")
	})

	t.Run("a round reached through a symlink is refused", func(t *testing.T) {
		root, _ := taskUnder(t)
		outside := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(outside, inputDir), 0o750))
		require.NoError(t, os.Symlink(outside, filepath.Join(root, taskName, "01-initial")))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err, "every artifact of this round would be written wherever the link points")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.NoFileExists(t, filepath.Join(outside, manifestFile), "nothing of this run was written out there")
	})

	t.Run("a round already holding a manifest is refused as already run", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		require.NoError(t, os.WriteFile(filepath.Join(round, manifestFile), []byte(`{"run":"01-initial"}`), 0o600))
		kept := filepath.Join(round, "report.md")
		require.NoError(t, os.WriteFile(kept, []byte("the round that went badly"), 0o600))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err, "a bad round is exactly the one a reflection agent wants to read")

		data, readErr := os.ReadFile(kept) //nolint:gosec // path built from t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "the round that went badly", string(data))
	})

	// an interrupt, an unwritable artifact or every source degrading leaves the marker New created and
	// nothing in it. The caller's own input/ is inside that round, so refusing it forever would cost him
	// the context he wrote rather than a marker carrying nothing
	t.Run("a round claimed by a run that never finished is re-claimed", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		scope := filepath.Join(round, inputDir, scopeFile)
		require.NoError(t, os.WriteFile(scope, []byte("review the diff"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(round, manifestFile), nil, 0o600))

		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.NoError(t, err, "an empty marker is a claim, not a round that ran")
		t.Cleanup(func() { _ = a.Close() })

		data, readErr := os.ReadFile(scope) //nolint:gosec // path built from t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "review the diff", string(data), "and the round's own input/ is untouched")
	})

	// the claim is written first and the record last, so a run that never came back may still have written
	// most of the archive. A second run over it produces one round holding two runs' artifacts, under a
	// manifest naming only the roster of the second — and nothing on disk says so
	t.Run("a round claimed by a run that wrote before it died is refused", func(t *testing.T) {
		tests := []struct{ name, leftover string }{
			{name: "a stage snapshot", leftover: "stages/1-found.json"},
			{name: "a per-agent tee", leftover: "agents/bugs+impl.jsonl"},
			{name: "a composed prompt", leftover: "prompts/agents/bugs+impl.md"},
			{name: "the decision log", leftover: "events.jsonl"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				root, round := roundUnder(t, "01-initial")
				scope := filepath.Join(round, inputDir, scopeFile)
				require.NoError(t, os.WriteFile(scope, []byte("review the diff"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(round, manifestFile), nil, 0o600))
				left := filepath.Join(round, tc.leftover)
				require.NoError(t, os.MkdirAll(filepath.Dir(left), 0o750))
				require.NoError(t, os.WriteFile(left, []byte("what the dead run wrote"), 0o600))

				_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
				require.Error(t, err, "an empty marker says the run never finished, not that it never wrote")
				assert.Contains(t, err.Error(), "open a new round")

				data, readErr := os.ReadFile(left) //nolint:gosec // path built from t.TempDir
				require.NoError(t, readErr)
				assert.Equal(t, "what the dead run wrote", string(data), "the refusal deletes nothing")
				assert.FileExists(t, scope, "and the input/ the message says copies across is still there")
			})
		}
	})

	// os.Root.Stat follows a link that lands back inside the round, so reading the marker's size through
	// one reads the target's — and Writer("manifest.json") then truncates the caller's own file through it
	t.Run("a marker that is not a regular file is refused, whatever it points at", func(t *testing.T) {
		tests := []struct {
			name    string
			content string
		}{
			{name: "a link to a goal the caller wrote", content: "the goal the caller wrote"},
			{name: "a link to an empty file", content: ""},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				root, round := roundUnder(t, "01-initial")
				goal := filepath.Join(round, inputDir, "goal.md")
				require.NoError(t, os.WriteFile(goal, []byte(tc.content), 0o600))
				require.NoError(t, os.Symlink(filepath.Join(inputDir, "goal.md"), filepath.Join(round, manifestFile)))

				_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
				require.Error(t, err, "claiming this round would write this run's record into the caller's own file")
				assert.Contains(t, err.Error(), "not a regular file")

				data, readErr := os.ReadFile(goal) //nolint:gosec // path built from t.TempDir
				require.NoError(t, readErr)
				assert.Equal(t, tc.content, string(data), "and the file the link names is left exactly as it was")
			})
		}
	})

	t.Run("a round that ran is refused by every racer at once", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		record := []byte(`{"run":"01-initial"}`)
		require.NoError(t, os.WriteFile(filepath.Join(round, manifestFile), record, 0o600))

		var claimed atomic.Int64
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
				if err == nil {
					claimed.Add(1)
					_ = a.Close()
				}
			})
		}
		wg.Wait()

		assert.Zero(t, claimed.Load(), "the claim is one atomic create, so no racer takes a round that ran")
		data, readErr := os.ReadFile(filepath.Join(round, manifestFile)) //nolint:gosec // path built from t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, record, data, "nor did any of them truncate its record")
	})

	// `revmux new` refuses a symlinked input/, so accepting one here would make the two disagree about
	// which rounds are usable — and the round's archived context would be an alias rather than the
	// per-round directory the audit trail depends on
	t.Run("an input/ reached through a symlink is refused", func(t *testing.T) {
		root, taskPath := taskUnder(t)
		round := filepath.Join(taskPath, "01-initial")
		require.NoError(t, os.MkdirAll(filepath.Join(round, "real-input"), 0o750))
		require.NoError(t, os.Symlink("real-input", filepath.Join(round, inputDir)))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err, "the link lands back inside the round, so containment alone accepts it")
		assert.Contains(t, err.Error(), "is a symlink")
		assert.NoFileExists(t, filepath.Join(round, manifestFile), "the round was never claimed")
	})

	t.Run("a file where the round belongs is not a round", func(t *testing.T) {
		root, taskPath := taskUnder(t)
		round := filepath.Join(taskPath, "01-initial")
		require.NoError(t, os.WriteFile(round, []byte("not a directory"), 0o600))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a directory")
	})

	t.Run("a file where input/ belongs carries no review context", func(t *testing.T) {
		root, taskPath := taskUnder(t)
		round := filepath.Join(taskPath, "01-initial")
		require.NoError(t, os.MkdirAll(round, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(round, inputDir), []byte("not a directory"), 0o600))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(round, inputDir)+" is not a directory")
		assert.NoFileExists(t, filepath.Join(round, manifestFile), "the round was never claimed")
	})

	t.Run("a round named after the task's own metadata is refused by name", func(t *testing.T) {
		root, _ := roundUnder(t, metaFile)
		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: metaFile})
		require.Error(t, err, "a round carrying that name is read as the task's metadata")
		assert.Contains(t, err.Error(), "is reserved")
	})

	t.Run("a round the caller has not created names the round to create", func(t *testing.T) {
		root, taskPath := taskUnder(t)
		round := filepath.Join(taskPath, "01-initial")

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err, "the round holds the caller's own context now, so revmux authors no part of it")
		assert.Contains(t, err.Error(), round, "the message names the directory the caller must create")
		assert.NoDirExists(t, round)
	})

	t.Run("a round with no input/ names the path the caller must create", func(t *testing.T) {
		root, taskPath := taskUnder(t)
		round := filepath.Join(taskPath, "01-initial")
		require.NoError(t, os.MkdirAll(round, 0o750))

		_, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err, "a round with no input/ carries no scope, so there is nothing to review")
		assert.Contains(t, err.Error(), filepath.Join(round, inputDir))
	})

	// a task directory holds its rounds and its task.md, and anything else a caller left there is his
	t.Run("a stray file at task level is ignored", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(round), "notes.md"), []byte("mine"), 0o600))

		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.NoError(t, err, "revmux reads the round it was given and judges nothing else in the task")
		require.NoError(t, a.Close())
	})
}

// TestNew_liveRound covers the two things an empty manifest.json has to mean at once. It is the claim an
// interrupted run left, so that round is re-runnable under the same --run and the caller keeps the input/
// he wrote into it. It is also what a run starting right now leaves, and two runs sharing one round
// truncate each other's artifacts until the last manifest wins. Size alone cannot tell those apart, so the
// claim is held as an exclusive lock on the marker for the run's lifetime — a lock the kernel drops when
// the process dies, which is exactly the abandoned case.
func TestNew_liveRound(t *testing.T) {
	t.Run("a round a live run holds is refused rather than shared", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		scope := filepath.Join(round, inputDir, scopeFile)
		require.NoError(t, os.WriteFile(scope, []byte("review the diff"), 0o600))

		first, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = first.Close() })

		_, err = New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.Error(t, err, "both runs would write the same artifacts and the last manifest would win")
		assert.Contains(t, err.Error(), "is being written by a run holding it")
	})

	t.Run("the round is re-claimable once the run holding it is gone", func(t *testing.T) {
		root, round := roundUnder(t, "01-initial")
		scope := filepath.Join(round, inputDir, scopeFile)
		require.NoError(t, os.WriteFile(scope, []byte("review the diff"), 0o600))

		first, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.NoError(t, err)
		require.NoError(t, first.Close(), "an interrupted run leaves the marker empty and the lock released")

		second, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
		require.NoError(t, err, "nobody holds the round, so the empty marker is a claim that never came back")
		t.Cleanup(func() { _ = second.Close() })
		data, readErr := os.ReadFile(scope) //nolint:gosec // path built from t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "review the diff", string(data), "and the caller's own input/ is untouched")
	})

	t.Run("exactly one racer claims a round nobody has run", func(t *testing.T) {
		root, _ := roundUnder(t, "01-initial")

		var mu sync.Mutex
		claimed := []*Archive{}
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "01-initial"})
				if err != nil {
					return
				}
				mu.Lock()
				claimed = append(claimed, a)
				mu.Unlock()
			})
		}
		wg.Wait()
		t.Cleanup(func() {
			for _, a := range claimed {
				_ = a.Close()
			}
		})

		assert.Len(t, claimed, 1, "a round is one run's, however many callers ask for it at once")
	})
}

// New looks at the round entry and then opens it, and those are two operations: a symlink planted in
// between is followed by os.Root whenever it lands back inside the task, which is exactly the
// round -> earlier round case the look exists to refuse. These reproduce that window by opening the
// handle on the swapped entry first and only then running the check that has to catch it.
func TestCheckHandle(t *testing.T) {
	t.Run("a symlink swapped in before the open is caught after it", func(t *testing.T) {
		taskPath := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(taskPath, "01-initial", inputDir), 0o750))

		taskRoot, err := os.OpenRoot(taskPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = taskRoot.Close() })

		require.NoError(t, os.Symlink("01-initial", filepath.Join(taskPath, "02-after-fix")))
		round, err := taskRoot.OpenRoot("02-after-fix")
		require.NoError(t, err, "containment is satisfied, so the open itself cannot refuse this")
		t.Cleanup(func() { _ = round.Close() })

		err = checkHandle(taskRoot, round, "02-after-fix", filepath.Join(taskPath, "02-after-fix"))
		require.Error(t, err, "the handle pins 01-initial, and every artifact written would truncate one of its own")
		assert.Contains(t, err.Error(), "is a symlink")
	})

	t.Run("a symlink swapped back to a real directory is caught by identity", func(t *testing.T) {
		taskPath := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(taskPath, "01-initial"), 0o750))
		require.NoError(t, os.MkdirAll(filepath.Join(taskPath, "decoy"), 0o750))

		taskRoot, err := os.OpenRoot(taskPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = taskRoot.Close() })

		require.NoError(t, os.Symlink("01-initial", filepath.Join(taskPath, "02-after-fix")))
		round, err := taskRoot.OpenRoot("02-after-fix")
		require.NoError(t, err)
		t.Cleanup(func() { _ = round.Close() })

		// the link is gone by the time the entry is read again, so only comparing it to the directory
		// actually opened tells the two apart
		require.NoError(t, os.Remove(filepath.Join(taskPath, "02-after-fix")))
		require.NoError(t, os.Rename(filepath.Join(taskPath, "decoy"), filepath.Join(taskPath, "02-after-fix")))

		err = checkHandle(taskRoot, round, "02-after-fix", filepath.Join(taskPath, "02-after-fix"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was replaced")
	})

	t.Run("a round renamed away and replaced is caught by identity", func(t *testing.T) {
		taskPath := t.TempDir()
		taskRoot, err := os.OpenRoot(taskPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = taskRoot.Close() })

		require.NoError(t, taskRoot.Mkdir("01-initial", 0o750))
		round, err := taskRoot.OpenRoot("01-initial")
		require.NoError(t, err)
		t.Cleanup(func() { _ = round.Close() })

		require.NoError(t, os.Rename(filepath.Join(taskPath, "01-initial"), filepath.Join(taskPath, "moved")))
		require.NoError(t, os.MkdirAll(filepath.Join(taskPath, "01-initial"), 0o750))

		err = checkHandle(taskRoot, round, "01-initial", filepath.Join(taskPath, "01-initial"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was replaced")
	})

	t.Run("the real directory New opened passes", func(t *testing.T) {
		taskPath := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(taskPath, "01-initial"), 0o750))

		taskRoot, err := os.OpenRoot(taskPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = taskRoot.Close() })
		round, err := taskRoot.OpenRoot("01-initial")
		require.NoError(t, err)
		t.Cleanup(func() { _ = round.Close() })

		require.NoError(t, checkHandle(taskRoot, round, "01-initial", filepath.Join(taskPath, "01-initial")))
	})
}

func TestArchive_Close(t *testing.T) {
	t.Run("releases the handle, leaving the artifacts on disk", func(t *testing.T) {
		root, round := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		w, err := a.Writer("report.md")
		require.NoError(t, err)
		_, err = w.Write([]byte("the report"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		require.NoError(t, a.Close())

		data, readErr := os.ReadFile(filepath.Join(round, "report.md")) //nolint:gosec // t.TempDir
		require.NoError(t, readErr)
		assert.Equal(t, "the report", string(data), "Close drops descriptors, never artifacts")
	})

	t.Run("the round root is unusable afterwards", func(t *testing.T) {
		root, _ := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)
		require.NoError(t, a.Close())

		_, err = a.Writer("report.md")
		require.Error(t, err, "the round root is closed")
	})

	t.Run("closing twice is not an error", func(t *testing.T) {
		root, _ := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)
		require.NoError(t, a.Close())
		require.NoError(t, a.Close(), "run defers it, so a caller closing early must not fail the run")
	})
}

func TestArchive_Writer(t *testing.T) {
	t.Run("accepts the nested paths every artifact needs", func(t *testing.T) {
		names := []string{
			"events.jsonl",
			"prompts/agents/bugs+impl.md",
			"prompts/stages/verify-app-executor.md",
			"stages/1-found.json",
			"agents/codex.log",
		}

		root, _ := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				w, err := a.Writer(name)
				require.NoError(t, err)
				_, err = w.Write([]byte("content of " + name))
				require.NoError(t, err)
				require.NoError(t, w.Close())

				data, err := os.ReadFile(filepath.Join(a.dir, filepath.FromSlash(name)))
				require.NoError(t, err)
				assert.Equal(t, "content of "+name, string(data))
			})
		}
	})

	t.Run("a second write to one name replaces rather than appends", func(t *testing.T) {
		root, _ := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		for _, body := range []string{"first attempt is longer", "second"} {
			w, wErr := a.Writer("stages/1-found.json")
			require.NoError(t, wErr)
			_, wErr = w.Write([]byte(body))
			require.NoError(t, wErr)
			require.NoError(t, w.Close())
		}

		data, err := os.ReadFile(filepath.Join(a.dir, "stages", "1-found.json"))
		require.NoError(t, err)
		assert.Equal(t, "second", string(data))
	})

	t.Run("rejects what escapes the run directory", func(t *testing.T) {
		tests := []struct {
			name     string
			artifact string
			want     string
		}{
			{name: "empty", artifact: "", want: "is empty"},
			{name: "absolute", artifact: "/etc/passwd", want: "is absolute"},
			{name: "parent", artifact: "../../scope.md", want: "escapes"},
			{name: "parent mid-path", artifact: "agents/../../../scope.md", want: "escapes"},
			{name: "the run root itself", artifact: ".", want: "escapes"},
		}

		root, _ := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := a.Writer(tt.artifact)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.want)
			})
		}
	})

	t.Run("a symlink inside the round defeats the lexical check and is still rejected", func(t *testing.T) {
		root, _ := roundUnder(t, "round-1")
		outside := t.TempDir()
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)
		require.NoError(t, os.Symlink(outside, filepath.Join(a.dir, "prompts")))

		_, err = a.Writer("prompts/agents/bugs.md")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "escapes")
		assert.NoFileExists(t, filepath.Join(outside, "agents", "bugs.md"))
	})

	t.Run("a round swapped for a symlink mid-run does not redirect a write", func(t *testing.T) {
		root, round := roundUnder(t, "round-1")
		taskPath, outside := filepath.Dir(round), t.TempDir()
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		// the containment check in New passed on the real directory; this is the swap it cannot see
		require.NoError(t, os.Rename(round, filepath.Join(taskPath, "moved")))
		require.NoError(t, os.Symlink(outside, round))

		w, err := a.Writer("stages/1-found.json")
		require.NoError(t, err, "the handle still points at the directory New opened")
		_, err = w.Write([]byte("findings"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		assert.NoFileExists(t, filepath.Join(outside, "stages", "1-found.json"))
		assert.FileExists(t, filepath.Join(taskPath, "moved", "stages", "1-found.json"))
	})

	t.Run("concurrent writers from several agents", func(t *testing.T) {
		root, _ := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				name := "agents/agent-" + strconv.Itoa(i) + ".jsonl"
				w, wErr := a.Writer(name)
				require.NoError(t, wErr)
				for range 20 {
					_, wErr = w.Write([]byte(`{"type":"system"}` + "\n"))
					require.NoError(t, wErr)
				}
				require.NoError(t, w.Close())
			})
		}
		wg.Wait()

		for i := range 8 {
			data, readErr := os.ReadFile(filepath.Join(a.dir, "agents", "agent-"+strconv.Itoa(i)+".jsonl"))
			require.NoError(t, readErr)
			assert.Len(t, data, 20*len(`{"type":"system"}`+"\n"))
		}
	})

	t.Run("a retried agent keeps both attempts, each parseable on its own", func(t *testing.T) {
		root, _ := roundUnder(t, "round-1")
		a, err := New(task.Round{TasksDir: root, Task: taskName, Run: "round-1"})
		require.NoError(t, err)

		// the stalled attempt is cut mid-line, which is exactly why it may not share a file with the retry
		attempts := map[string]string{
			"agents/bugs.jsonl":       "{\"type\":\"system\"}\n{\"type\":\"resu",
			"agents/bugs.retry.jsonl": "{\"type\":\"result\"}\n",
		}
		for name, body := range attempts {
			w, wErr := a.Writer(name)
			require.NoError(t, wErr)
			_, wErr = w.Write([]byte(body))
			require.NoError(t, wErr)
			require.NoError(t, w.Close())
		}

		first, err := os.ReadFile(filepath.Join(a.dir, "agents", "bugs.jsonl"))
		require.NoError(t, err)
		assert.Equal(t, attempts["agents/bugs.jsonl"], string(first))

		retry, err := os.ReadFile(filepath.Join(a.dir, "agents", "bugs.retry.jsonl"))
		require.NoError(t, err)
		var ev map[string]string
		require.NoError(t, json.Unmarshal(retry, &ev), "the retry parses on its own, unspliced")
		assert.Equal(t, "result", ev["type"])
	})
}

// taskUnder makes one task directory under its own tasks root, returning both: New is anchored at the
// root and names the task, while the assertions still need the directory the artifacts land in.
func taskUnder(t *testing.T) (root, taskPath string) {
	t.Helper()
	root = t.TempDir()
	taskPath = filepath.Join(root, taskName)
	require.NoError(t, os.MkdirAll(taskPath, 0o750))
	return root, taskPath
}

// roundUnder prepares one round the way a caller leaves it: a directory beside its siblings under the
// task, holding the input/ his review context goes in. New opens both and creates neither.
func roundUnder(t *testing.T, run string) (root, round string) {
	t.Helper()
	root, taskPath := taskUnder(t)
	round = filepath.Join(taskPath, run)
	require.NoError(t, os.MkdirAll(filepath.Join(round, inputDir), 0o750))
	return root, round
}
