---
paths:
  - "app/config.go"
  - "app/main.go"
  - "app/introspect.go"
  - "app/newcmd.go"
  - "app/initcmd.go"
  - "app/statscmd.go"
  - "app/cleanupcmd.go"
  - "app/treewriter.go"
  - "app/artifacts.go"
  - "app/inputsnapshot.go"
  - "app/progress.go"
  - "app/archive/**"
---

## Config and CLI

CLI parsing with `jessevdk/go-flags`; the config file is INI parsed through the same library's `IniParser`,
so every setting has exactly one definition — the struct tag.

Precedence: CLI flags > `./.revmux/config` > `~/.config/revmux/config` > embedded defaults.
`--config-dir` overrides the user-level location.

The project layer is **auto-detected**, not flag-driven: if `./.revmux/` exists it is used, and if it does
not the layer is simply absent. There is no `--project-config-dir`, because the whole point is that
checking `.revmux/` into a repo makes every invocation inside it use those settings.

`./` means the **process working directory**, not `--workdir`. Everything path-relative resolves the same
way — the project config, `--tasks-dir`'s `./.revmux/tasks` default — so a reviewer can predict where
revmux looks without tracking which flag a given path hangs off. `--workdir` sets where the *subprocesses*
run and what `{{WORKDIR}}` expands to; reviewing a repo from outside it means passing `--config-dir` and
`--tasks-dir` as well, since by the same rule both would otherwise resolve against the caller's cwd rather
than the repo under review.

**`--config-dir ./.revmux` collapses the two layers into one, and that must be detected.**
Otherwise the same directory loads twice as both the user and project layer — harmless for a scalar, but
it makes "which layer supplied this" wrong, which is exactly what `revmux config` reports.
Compare the two after resolving to absolute paths **and** evaluating symlinks: on macOS a temp dir under
`/var` is really `/private/var`, so a lexical comparison misses the collision in precisely the tests that
would catch it. When they are the same path, drop the project layer.

Layers merge **per field**, not whole-file: a project config setting one key must not discard the user
config's other keys.

### What belongs in the config file

Runtime knobs only: `idle-timeout`, `hard-timeout`, `stagger-delay`, `max-parallel`, `verify-groups`,
`verify-group-by`, `tasks-dir`, `auto-exit`, `profile`.
The key is the long flag name verbatim, hyphens included — that is what `ini-name` is set to, and it is what
makes the key guessable from `--help`.

`tasks-dir` is a location, not review content, so it belongs here — a user who wants task directories on `/tmp`
sets it once rather than passing it on every invocation.

`--task` and `--run` are not in this list and must not be added: a config file naming the round to write
would make the same invocation review different context in different directories.

Everything that shapes a review — rosters, models, effort, prompt text, lenses — lives in markdown.
See `.claude/rules/prompts.md`. Do not add a roster or a model to the INI file.

- `no-ini:"true"` on meta flags that make no sense in a config file (`--init`, `--dump-defaults`, `--version`, `--config-dir`).
- `ini-name` tags so config keys match the long flag names; a user reading `--help` should be able to guess the key.
- Distinguish "explicitly false" from "not set" with a sentinel when a bool needs a per-field merge across layers.
  Without it, a project config can never turn off something the user config turned on.

### Flag description style

Minimal and atomic. State what the flag does, nothing more.

Never write "at startup", "on startup", "(mirrors the X toggle)", or a cross-reference to a runtime key binding
in a struct tag, the flag tables on the site, or godoc.
The description says what the flag does; runtime toggles are discovered from the key bindings, not from flag help.

In documentation use the `--flag=value` form for long flags that take a value, not `--flag value`.

### Context resolution

revmux never derives context — the caller writes it to disk and names it.
`--task <id>` selects a directory under `--tasks-dir` (default `./.revmux/tasks`), `--run <name>` selects one
round inside it, and `<task>/<run>/input/` is the only channel review context travels through.
There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags:
variables resolve to **paths**, so a flag carrying inline text could not be substituted without revmux
first writing it to a file, which would make revmux an author of context rather than a consumer of it.

**Context is per-round, so it is read from the round.**
Round 2 reviews the fixes for what round 1 found: a different scope, usually a different goal, sometimes a
different project profile. One set of files at task level is overwritten by whoever composes the next round,
and the record of what the previous round reviewed goes with it — the archive cannot recover it, since an
archived prompt carries the path and not the text.

`options.resolveContext` stats `<task>/<run>/input/` and returns the resolved absolute paths:

1. `scope.md` — required, and a missing or empty one is a load-time error, since a review with no scope is a caller bug
2. `goal.md`, `profile.md` — optional, absent resolves to the "none provided" placeholder
3. `context/` — optional directory the caller fills with as many files as it likes

**`profile.md` is the one context file with a layer under it.**
When the round carries none, `options.projectProfile` looks for `./.revmux/profile.md` and, finding one,
sets `reviewContext.ProfileSource` to it and `Profile` to `<round>/prompts/input-profile.md` — the path the
snapshot will occupy. `runOpts.materializeProfile` writes those bytes through the held archive at the top of
`review`, before the renderer is built, since the TUI's input snapshot is taken there and the agents read
the path later still.
It resolves through `projectDir`, **never** `o.layers.project`: `resolveLayers` empties that field when
`--config-dir ./.revmux` collapses the two layers, and keying on it would make the fallback silently vanish
under that one invocation while the file sat untouched on disk. `revmux config` reports the same resolution
as `paths.profile_fallback` and must use the same method, or the catalog and the run disagree.
The user layer is deliberately not searched: calibration that spans repositories describes none of them.
Nothing is ever written into `input/` — a snapshot placed there would be read as an explicit round-local
override by the next attempt on that round, and the authored project file would stop applying with nothing
saying so.

Absent `goal.md` or `profile.md` is **not an error**.
The run proceeds, and the variable resolves to a "none provided" placeholder the shipped profile bodies
instruct the agent to read as generic severity calibration.
That guarantee lives in the prompt text, not in Go — the report header carries the title, the scope path
and the degraded banner, and says nothing about calibration.

**An omitted `--run` is a load-time error, not a name revmux invents.**
The round holds the caller's own `input/`, so a round revmux named is a round nobody filled; the error names
the `revmux new` call that creates one.

**A `--run` naming a round that is not there gets that same message, and `resolveContext` is where it has to
come from.**
The round is stat'd before its `input/` is read, because the three files below it are all optional-or-empty
shaped: without the check a round nobody created is reported as `scope.md is required and must not be empty`,
which names a path two levels inside a directory that does not exist and reads as a scope the caller wrote
wrong. `archive.New` says it properly and never runs — `resolveContext` fails first.

The struct also carries `TaskDir`, and it is the **task** directory even though every context file sits two
levels below it.
`archive.History` enumerates the task's rounds from it, and the rounds are its children.
Pointing it at the round is the one mistake here with a silent failure mode: `History` finds no rounds,
returns an empty string, and the prior-round block is omitted from every composed prompt with no error
anywhere — a hard rule broken invisibly. A regression test asserts the block is non-empty when a prior round
exists, and it is there for exactly that.
Re-deriving the directory with `filepath.Dir(Scope)` elsewhere is the other way to get this wrong: two
resolutions that can disagree.

And it carries `WorkDir`, from `--workdir` and defaulting to the process working directory.
That one does not come from the task directory, but it belongs on the same struct: it is what `{{WORKDIR}}`
expands to and what `executor.Opts` sets as each subprocess's working directory, so the two must be the
same value. An agent told to review `{{WORKDIR}}` while its process runs somewhere else reads one tree and
reports on another. It is separate from `TaskDir` because `--tasks-dir` may point at `/tmp` while the code
under review is elsewhere — the review target and the context store are independent locations.

Return these as a struct, not as adjacent same-typed values.
`(scope string, goal string, profile string, err error)` is a transposition waiting to happen: swapping any two
compiles clean and silently feeds the project profile into `{{GOAL}}`.

### `--task` and `--run` are untrusted input

Both names come from the caller and are joined into filesystem paths, so they are validated before use,
not after. A name containing `..` or a path separator escapes the tasks root and lets revmux write over
caller-authored context — the exact thing "revmux writes only inside a round" forbids.

Reject any name that is empty, contains a path separator or `..`, is absolute, or begins with `.`.
Then verify containment on the resolved path rather than trusting the lexical check alone,
since a symlink inside the tasks root can still point outside it.

**`task.CheckName` is the single definition of that rule.**
`options.checkNames`, `archive.New` and `task.Scaffold` all delegate to it, and they pass the flag the name
came from — `--task` and `--run` — so one rule speaks with one vocabulary wherever it is applied.
Three copies of one security-relevant rule are three chances for one of them to drift, and the one that
drifts is the one nobody re-reads.

**`task.CheckContained` is the single definition of the containment check, for exactly the same reason.**
`options.taskDir` applies it to the task directory a review reads, which is the one place a path *string*
is all there is.
Two `EvalSymlinks`-plus-prefix implementations of one security-relevant rule are the same drift risk the
name rule is consolidated against; do not inline a second one.
**Nothing that writes uses it.** A resolve followed by a write is two operations, and the rename in
between is the whole attack — `task.Scaffold` and `archive.New` both walk down as nested `os.Root`s
instead, which contains every hop by construction.

**A round name passes `task.CheckRoundName` on top of it, which refuses the one reserved entry.**
A round is a direct child of the task directory and shares its namespace with the task's own `task.md`, so
`--run task.md` is refused: that round would be read as the task's metadata rather than as a round —
`Load` parses it, `Rounds` skips it, and `writeMeta` then refuses to write the real metadata over a
directory, so the round is unreachable as one rather than overwritten. The name is `metaFile` in
`app/task`, spelled once.

**`task.Scaffold` writes through nested `os.Root`s anchored at the tasks root, never by path.**
A check on a resolved path and the write that follows it are two operations, and the directory can be
swapped for a symlink in between — so `<tasks-root>/pr-1` replaced mid-scaffold put `task.md` outside the
root while `revmux new` reported success. `EvalSymlinks`-checking each entry closed nothing, because the
window is *after* the check.
`Scaffold` therefore `MkdirAll`s the tasks root, opens it as a root, and takes the task directory and then
the round as nested roots, creating what is missing through the handle above it. A swap after any hop
leaves the handle on the directory that hop accepted.

That also makes `revmux new` and the review path agree by construction rather than by two implementations
of one rule: both walk the same chain, so a task symlink with an **absolute** target is refused by both,
and a relative one to a sibling task is followed by both.

**The round and its `input/` must additionally not be symlinks at all, and that is not containment.**
A link landing back *inside* the tasks root passes every containment check and still points this round's
reported paths at another round, whose caller-written `input/` the next `scope.md` overwrites.
Both `Scaffold` and `archive.New` `Lstat` the entry and refuse a link outright — the review path with
`requireInput`, which must not go back to `os.Root.Stat`: that **follows** a link landing inside the round,
so an `input -> real-input` would be usable for review while `Scaffold` refused it, and the archived
context would be an alias rather than this round's own directory.
The task directory above them is deliberately exempt: a relative link to a sibling task inside the root is
what `options.taskDir` and `archive.New` both accept.

**`task.md` is the one file `Scaffold` writes, and a check on directories does not cover it.**
A plain `os.WriteFile` follows a symlink at its last component, so `<task>/task.md -> /elsewhere/anything`
puts the template outside the tasks root while `revmux new` reports success. The entry is therefore read
with `Lstat` — a dangling link is a link, not a missing file — and created with `O_CREATE|O_EXCL` **through
the task's own handle**, which refuses one planted between the look and the write the same way the round's
claim does. Do not put it back on `Stat` plus `os.WriteFile`.

**`archive.New` re-establishes that containment structurally, and it starts at the tasks root.**
`options.taskDir` validates the resolved task path, but what it returns is a path *string*, and reopening
that by name is the check-then-open window this section exists to remove.
`archive.Opts` therefore carries `TasksDir`, `Task` and `Run` rather than a joined path, and `New` opens an
`os.Root` on the tasks root and takes the task directory, then the round, as nested roots.
An escaping symlink fails at open rather than passing a comparison, whenever it was planted.

**Nothing on the way down is created.**
The tasks root, the task directory and the round with the `input/` the caller filled are all his, and
`options.resolveContext` already refuses a round with no `scope.md`.
Creation lives in `revmux new` alone, and `resolveContext` must never gain a create-on-missing fallback: a
typo'd `--task` has to error rather than silently produce an empty task nobody filled.

Anchoring at the tasks root is stricter than `filepath.EvalSymlinks` in one case, and deliberately so:
`os.Root` resolves a symlink target *within* the root, so a task symlink with an **absolute** target is
refused even when that target sits inside the tasks root. A relative one landing inside still resolves and
works. Do not relax this back into a resolve-then-reopen — the exotic case is a task directory linked to a
sibling task, and the window it would reopen is the one the roots exist to close.

**Containment is necessary at the round but not sufficient, so the round must additionally not be a symlink
at all.**
`os.Root` follows a link that lands back *inside* the root, so a round linked to a sibling round satisfies
every containment check and still has every artifact of this run truncate one of that round's — destroying
exactly the bad round a reflection agent wants to read, without ever leaving the task directory.
`New` therefore `Lstat`s the round entry through the task handle and refuses a symlink outright, before
opening anything.

**Reading an entry and opening it are two operations, so the look is repeated against the open handle.**
A symlink planted between them is followed by `os.Root` whenever it lands back inside the parent — the case
the `Lstat` exists to refuse, reached by racing it rather than by defeating it.
`New` therefore re-reads the entry after `OpenRoot`, refuses a symlink, and matches it against the directory
actually opened with `os.SameFile`. That is `checkHandle`.
A swap after that point changes nothing: the handle stays on the directory the check accepted.

**The round is then claimed with an exclusive create, and that is what detects one that has already run.**
`OpenFile("manifest.json", O_CREATE|O_EXCL)` through the round handle is atomic where a look followed by a
write is not, and it leaves every artifact of the earlier round exactly as it was.
`input/` is required first, and its absence errors with the path the caller must create — a round without one
carries no scope and there is nothing to review.
Do not replace the exclusive create with a `Stat` plus a write, and do not make a **finished** round
reusable: a round that went badly is the one a reflection agent reads.

**The one carve-out is an empty marker over an otherwise empty round, and it is what makes an interrupted
round re-runnable.**
The claim creates `manifest.json` with no content and the finished run writes the record into it, so a
marker still empty is a claim its run never came back from — SIGINT on a long review, an unwritable
artifact, every source degraded. `claimRound` re-claims that round instead of refusing it.
Since the round now holds the caller's own `input/`, refusing it forever would cost him the scope, goal,
profile and context he wrote for it, on the one path where nothing of the review survived to be protected.

**An empty marker is also what a run starting right now leaves, and size cannot tell those two apart, so
the claim is an OS-level lock held for the run's lifetime.**
Left on size alone the carve-out was a hole: two `archive.New` calls on one prepared round both succeeded,
and the two runs then wrote and truncated the same artifacts until the last manifest won.
`claimRound` therefore takes a non-blocking exclusive lock on the marker — `flock` on unix, `LockFileEx` on
a byte past the record on windows, both in `tryLock` — and `Archive.Close` releases it.
A lock **refused** means a live run owns the round and this one is turned away; a lock **acquired** over an
empty marker means the run that claimed it is gone, because the kernel drops the lock when a process dies.
That is what makes an abandoned round distinguishable from a live one with nothing to clean up, no pid to
trust and nothing to delete.
The lock is taken on a marker this run just created as well: creating it and locking it are two operations,
and a racer reading it in between finds it empty. Both sides lock, so exactly one wins and the other is
refused whatever the interleaving.
Everything the reclaim decision rested on is then re-read **under** the lock — the marker's size, that the
entry is still the descriptor's own file, and the round's leftovers — since each was read before the lock
was held.
Do not swap `flock` for a pid file or a timestamp: both re-introduce the guess this replaces.
What stays arbitrated regardless is the case that matters most — a round that ran is never re-claimed,
however many callers ask for it at once.

**What the marker means is `task.CheckMarker`, and it is one predicate read with `Lstat`.**
`HasRun`, `task.Scaffold` and `archive.claimRound` all ask it, and it refuses anything that is not a regular
file before it looks at the size. A marker that is a symlink is the case that makes this load-bearing rather
than tidy: `os.Root.Stat` **follows** a link landing back inside the round, so `manifest.json -> input/goal.md`
reads as the goal's size — non-empty looks like a round that ran, empty passes the claim outright — and the
write that fills the marker in then truncates the caller's own context through the link.
Reading the size without first refusing a non-regular entry is the bug this consolidation exists to prevent,
and a copy of the predicate that omits the check is that bug reintroduced.

**An empty marker says the run never finished, not that it never wrote, so the round must also be clean.**
The marker is created first and filled last, while the stage snapshots, the composed prompts, the per-agent
tees and `events.jsonl` all land during the run. Re-claiming such a round writes only what this run
produces and then claims the lot with a manifest naming a roster that never wrote the leftovers beside it —
one directory holding two runs, with nothing on disk saying so, which is precisely the un-auditable archive
`CLAUDE.md` forbids.
`task.CheckReclaim` therefore refuses a round holding anything but `input/` and the marker, naming what it
found. Both `claimRound` and `task.Scaffold` call it on the entries they read through their own handle on
the round, so `revmux new` and the review path agree about which rounds are still open.
**The refusal must never become a delete.** Removing the leftovers would destroy the evidence of the run
that wrote them, and nothing on the review path removes anything — `revmux cleanup` is the one destructive
command, it is reached only by being asked for by name, and it takes a whole task rather than a round.
The caller opens a new round and copies his `input/` across. Nor may the check be narrowed to a list of names revmux happens to write today — an
artifact added later would then be missed, and the two-runs-in-one-round case is exactly what it exists to
catch.

**Everything that counts rounds calls `task.Rounds`, never its own walk over the task directory.**
It gates on `task.HasRun` rather than on the marker being there: `archive.History` is resolved before the
round is claimed, so a re-run of a round whose first attempt was interrupted would otherwise find its own
empty marker and list the round being written as one of its own prior rounds.
`revmux config` reporting it is the same error in a different place — a caller reading `rounds` would treat
a re-runnable round as one already spent and mint a name beside it.
Both read the enumeration from one function so "what counts as a round" cannot be answered twice.

**The round handle is then held for the whole run, and that is the point.**
A path checked once and reopened by name on every write is a path another process can rename and replace
with a symlink in between; a handle keeps referring to the directory it was opened on and refuses any name
that leaves it, so containment holds for the run's lifetime rather than for the instant it was measured.
`Archive.Close` releases it, deferred in `run` once every artifact is on disk, along with the locked marker
that says the round is this run's — so releasing early is what lets a later run re-claim a round this one
never finished.
The tasks-root and task handles are closed inside `New`, since nothing is written through them and nothing
below needs them — `archive.History` takes a path rather than the handle.
Do not reintroduce a path-string `resolve` plus `filepath.EvalSymlinks` here — that is the check-then-open
window the roots exist to remove, and it needs `/var` versus `/private/var` reasoning the handles do not.

`options.taskDir` still runs, and still returns the joined path: `reviewContext.TaskDir` is what the prompt
variables and the prior-round inventory read. What it must not be is the thing the archive opens — that
takes `--tasks-dir`, `--task` and `--run` and joins them itself, so the boundary is enforced by the open
rather than inherited from a string.

**Roster agent names get the same treatment, but at a different layer — do not conflate the two.**
An agent name comes from the roster and becomes one path *component*, so the empty / separator / `..` /
absolute / leading-`.` rules above apply to it, and they apply **before** any filename is built.
An agent called `events` would otherwise collide with `events.jsonl`, which is why per-agent streams live
in their own subdirectory as well.

That check is `prompt.AgentSpec.checkName`, run at **load**, not in the archive. Load is the earliest point
still ahead of any filename, it is where every other roster-entry rule already lives, and it covers the
`--lenses` override for free because that path validates through the same method.
An invalid agent name is therefore a startup error, like every other bad front-matter value.

`Archive.Writer` itself validates something else: it takes a clean **relative path** and rejects only what
was never an artifact path — empty, absolute, or climbing out lexically. Anything that leaves the round
by following a symlink is refused by the round's own handle when it is traversed, so `Writer` does no
symlink resolution of its own. A separator is legal there and must be, since
`prompts/agents/`, `prompts/stages/`, `stages/` and `agents/` all need one.
Making `Writer` reject separators would make "`Writer` accepts `prompts/agents/x.md`" and "a separator in
a name is rejected" mutually unsatisfiable — two tests that both have to pass.

A **derived** string that becomes a filename is sanitized rather than rejected, and a verify group's label
is the one case of it. Its parts are directory names taken from the findings, so there is no author to send
an error back to and refusing one would fail a stage over the shape of a path under review. Everything
outside `[a-zA-Z0-9_.-]` collapses to a dash, leading and trailing `-` and `.` are trimmed, and an empty
result becomes `root` — so the label is safe by construction, never by validation.
Reject caller-**authored** names; sanitize revmux-**derived** ones. Do not swap the two.

`--tasks-dir` and `--config-dir` are different roots and must not be conflated:
the first holds per-review context and run artifacts, the second holds config and the prompt tree.

### Mode gating at the composition root

When a flag is meaningful in some modes and not others, resolve it to a concrete value in `package main`
through a method on `options`, and pass the resolved value down.
Downstream code takes the resolved value and must not re-derive it from the raw options.

### Filtering happens once

`--min-confidence` filters the report in `package main` before rendering, and the rendered report and the
exit code are both computed from that filtered set.
A rendering path that ignores the threshold while the exit code honors it produces a report listing
findings the exit code claims are absent.

**The findings browser is one of those rendering paths — the filter goes before `renderer.finish`, not
after it.** `runOpts.review` applies it, because `finish` is where the report crosses into `app/ui` and
anything applied in `run` afterwards arrives too late. Filtering there instead puts the TUI and stdout in
open disagreement about the same run, which is precisely what this section exists to forbid.

### The report on stdout

JSON by default; `--markdown` renders it instead. The caller is a program — a model composing an
invocation and parsing the result — and the human-facing views are the TUI and the archived `report.md`.
A markdown default made every programmatic consumer parse prose back into structure, and gave a human
nothing the other two did not already give them better.

Both forms are archived regardless of the flag, so the choice affects stdout alone.

### Exit codes

- `0` — no findings above the threshold
- `1` — findings above the threshold
- `2` — tool error: bad config, unreadable prompt tree, an omitted `--run`, a round with no `input/` or an
  empty `scope.md`, a round that has already run or is held by a live run, an unwritable run artifact, or
  **every source degraded**

`1` is a normal outcome, not a failure. Callers script against this, so do not repurpose these values.

The subcommands share `2` for their own tool errors — a `revmux stats --task` naming no task under the root,
a tasks root or task directory that will not read, an unwritable `./.revmux/` under `revmux init` — and none
of them can exit `1`: there is no report, so there is no threshold to be above.

Two of those deserve spelling out, because the obvious implementation gets both wrong.

**Every source degraded is not a clean empty report.** The degrade policy continues past a dead agent, but a
run where nothing reported has no review in it, and returning `0` tells a scripted caller the code is fine.

**A failed archive write fails the run.** `CLAUDE.md` requires a run archive sufficient to audit the review
without re-running it, so a report emitted alongside a half-written archive is worse than no report: it looks
complete, and the gap only surfaces later when someone tries to audit it. Either every required artifact is
written or the run exits `2`.

**A per-agent tee under `agents/` is the one artifact that carve-out does not cover.** It is opened, written
and closed by one agent's own goroutine, so a failure there is attributable to exactly one source, and
`finder.attempt` reports it through `sourceResult` like any other agent fault: retry once, then degrade.
That keeps a full roster's completed work rather than discarding it over one unwritable file, and the loss
is still loud — the banner names the source and `degraded` carries it.
It does **not** route through `Pipeline.fail`, and widening it to do so would break the tested degrade path.
Every whole-file artifact — manifest, composed prompts, stage snapshots, `events.jsonl`, report — does.

### Nothing deletes as a side effect

There is no pruning and no `--keep-runs`. A review, `new`, `init`, `config` and `stats` remove nothing,
ever, and no `--force`, `--overwrite` or reclaim-on-write is added to any of them.
A round holds the caller's own `input/`, so anything that deleted a round in the course of doing something
else would delete the record of what that round reviewed — the one artifact the archive rule exists to
preserve — and it would do it while the caller was asking for a review.

`revmux cleanup` is the sole destructive path, and keeping it a dedicated command is what makes that rule
statable at all: growth is still bounded by the user, who now names the task rather than composing an
`rm -rf` out of a layout he should not have to know.

Do not widen it into a flag on anything else, and do not give a running review a way to reach it.

### Config-management flags

- `--init` materializes `./.revmux/` with the config commented out and every prompt file as it **resolved**,
  ready to customize, and prints the paths as JSON on stdout.
- `--dump-defaults <dir>` extracts the **embedded** prompt tree for comparison or as a starting point for overrides.
- Neither ever overwrites a prompt file the user has customized.
- The config is the deliberate exception, and it is the reason settings ship commented out: one holding no
  uncommented key is rewritten with the current template, so an upgrade can still move a default nobody set.
  Only a config carrying an actual setting is left alone, and `created` in the payload therefore describes
  `files[]` — the config is reported as a path.

**`--init` and `revmux init` are one implementation, not two spellings that each materialize a tree.**
The flag routes into `runOpts.writeInitPaths` exactly as the subcommand does, so what the two write cannot
diverge — and it is the flag, not the subcommand, that a launcher script is handed as "any other revmux flag".

**They also share the writer, and that is what keeps "never overwrites" one rule rather than two.**
`treeWriter` opens the destination as an `os.Root` and creates every file through it, so a symlink occupying
a directory name is refused where it sits instead of sending the tree wherever it points — a path joined and
written to contains its last component alone, since `os.Lstat` dereferences every directory above it.
The entry check is `Lstat` and the create is `O_CREATE|O_EXCL`, the pair `task.Scaffold` writes `task.md`
with: a dangling link is a link rather than a missing file, and `Stat` plus `os.WriteFile` follows it.
Hardening one materializer and leaving the other is how the hole came back a level down; there is one now,
and a second copy of it is the same bug waiting.

**The config is written through that same handle, and it is the leaf where this was learned twice.**
`revmux init` opens one `treeWriter` on `./.revmux/` and hands it to `initConfig` and to the prompt
materialization alike, so nothing on the init path resolves a path instead of holding a root.
Written by name it was the last hole: `os.ReadFile` reports a dangling `config -> ../../elsewhere` as an
absent file and `os.WriteFile` then creates that target outside the project, while a link to an existing
file carrying no `key = value` line was truncated and replaced with the template — and the payload
reported `<cwd>/.revmux/config` in both cases, so the caller was told the tree was local.
The config takes `treeWriter.replace` rather than `write`, because a comments-only config is deliberately
rewritten and that is an `O_TRUNC` the `O_EXCL` in `write` exists to refuse.
Both are gated by `checkRegular`, which refuses an entry that is not a regular file: `os.Root` refuses a
link leaving the destination but follows one landing back inside it, and a config read and then truncated
through an alias is the case that distinction decides.

**Which layer each one reads is the whole difference between them, and it must stay that way.**
`--init` writes what resolved, so a user with an overridden lens gets his own text and a tree that loads with
no fallback under it; `--dump-defaults` writes the embedded bytes at an arbitrary path, which is the only way
to diff a customized lens against the shipped one. Pointing either at the other's layer removes the one thing
it is for.

**Those two are the only things that write config, and a normal run writes none of it.**
Loading never installs defaults into `~/.config/revmux/` as a side effect: the embedded copy is already the
bottom of the precedence chain, so materializing it on disk buys nothing and turns a read-only invocation
into one that touches the user's home directory. A user who wants files to edit asks for them.

### `revmux config`

revmux is driven by a caller model that has to compose an invocation without reading the source, so the
resolved configuration is machine-readable output rather than something to be reconstructed from `--help`
and a directory listing. `revmux config` prints it as JSON on stdout and exits `0`.

It is a **subcommand**, not another meta flag, because that is what a caller types and what `--help` then
documents. Register it with `go-flags` and set `parser.SubcommandsOptional = true` so a bare
`revmux --task pr-123` keeps working with no command word.

**It reports what resolved, never what is embedded.** A user who overrode one lens and added another must
see his own tree, or the catalog describes a review that will not happen. For the same reason every runtime
knob is reported with the precedence layer that supplied it, not only its value: whether `--stagger-delay`
is a default or a deliberate choice changes whether a caller should pass it.

Values a caller has to match exactly — the `executor` and `effort` vocabularies — are read from the same
constants `validate` uses. A second hardcoded copy here means a new effort level ships working but
undiscoverable.

**`paths.tasks` reports the task store, not just its location.** Each entry is a task's id, the `task.md`
front matter describing it, and the rounds already recorded under it.
A caller matches an in-flight review against that: with an id alone it cannot tell whether `pr-123` is the
task it means, and it mints `pr123` beside it.
`taskInfo` embeds `task.Meta` rather than copying its fields, so a new front-matter key surfaces here without
a second edit.
A task whose `task.md` is absent or will not parse is still listed with empty anchors — a task omitted from
the list is one a caller gives a second id to.
**A parse failure is reported on the entry as `meta_error`, never dropped.**
`task.Load` builds four error paths — an unknown key, malformed YAML, an unterminated block, an unreadable
file — and `revmux config` is their only production caller, so discarding the error makes every one of them
unreachable by a user: a typo'd `titel:` leaves all four anchors empty with nothing said on stdout, stderr
or the exit code, which reads as "this task was never described" and produces the duplicate id `task.md`
exists to prevent. The task stays in the list either way; only the reason is added.
Rounds come from `task.Rounds`, the same enumeration the prior-round inventory is built from, so neither a
directory a caller left under the task nor a round claimed by a run that never came back is reported as one.

**Which entries are tasks is decided through an `os.Root` on the tasks root, so this lists exactly what
`archive.New` can open.**
A task directory reached through a relative symlink to a sibling is accepted by `options.taskDir`,
`archive.New` and `task.Scaffold` alike — the id is an alias for the same directory, and a review really
does run under it. `os.ReadDir` alone reports such an entry as a non-directory and drops it, which is the
same wrong advice as omitting a task whose `task.md` would not parse: the caller sees no `alias`, mints a
second id for a task already in flight, and its history forks.
The reverse holds too — a link the archive cannot walk, absolute or leaving the root, is left out, since
listing it advertises a review that cannot run.
Aliasing is supported, in other words, and every path that reads a task agrees on it; `archive.History`
already followed the link, and this is what brings the catalog in line.
That decision is `task.List`, and it is the **only** enumerator: `revmux stats` aggregates the ids it
returns rather than walking the root itself, so the two commands cannot name different task sets. The
skill's `analyze-corpus.py` applies the same test rather than counting directories — a round whose marker
is empty was claimed and never finished, and counting it would put a round nobody reviewed into the
denominator of every rate the analysis reports.

**The same rule applies to every other failure this command can hit: an empty list must mean empty.**
An unreadable tasks root reported as `"tasks": []` is the identical wrong advice one level up — it reads as
"nothing is there" and mints a duplicate id — so it is `paths.tasks_error`, an unreadable task directory is
`rounds_error` on that entry, and a `--workdir` that will not resolve is `paths.workdir_error` beside the
raw value. An absent tasks root is the one clean case: it is a fresh install, not a failure to read one.

**`configCmd.Execute` does not print.** go-flags calls it from inside `parseArgs`, before the injected
writers and the loaded prompt tree exist, so it sets `opts.showConfig` and `runOpts.writeCatalog` does the
writing. A subcommand that writes from `Execute` writes to the real `os.Stdout` and cannot be tested.

### `revmux new`

`revmux new --task <id> --run <name>` creates the round and prints, as JSON on stdout, every path the caller
writes to — the task directory, `task.md`, the round, its `input/`, and the four context paths inside it —
plus which of them this call created.

**It exists so the layout stays revmux's own detail.** A caller that joins
`<tasks-dir>/<task>/<run>/input/scope.md` itself has reimplemented the layout from a document, and the next
change to it breaks that caller silently. One that writes to the paths it was handed does not.

It creates whatever is missing down the chain, `--tasks-dir` included — a first `revmux new` on a clean
checkout materializes `./.revmux/tasks/` as well, and the roots it makes are not in `created`, which names
only the four layout fields the payload carries.

It is the only place revmux creates review context, and the two constraints on it are not negotiable:
`resolveContext` must never gain a create-on-missing fallback, and `new` must refuse to overwrite.
It is idempotent at task level — a second round on an existing task creates only the round — it never
rewrites an existing `task.md`, and it refuses a round that has run, which is `task.HasRun` and not the
mere presence of the file: a round whose marker is still empty was claimed by a run that never came back
and is scaffolded again, the same way `archive.New` re-claims it — and, the same way, only while that run
left nothing else in it. Both call `task.CheckReclaim` and both classify the marker with `task.CheckMarker`,
so `new` never hands back a round the review path would refuse — the divergence to watch for is one side
reading the marker its own way, which is how a symlinked `manifest.json` came to be accepted by `new` and
refused by the review at the same time.
The `task.md` it writes ships fully commented out, the same way `--init` writes the config template: a task
nobody described carries no metadata rather than a placeholder anchor matching the wrong task.
Anything already occupying a layout path as something other than a directory — a file named `input`, a
directory named `task.md` — is an error rather than a path reported back, since a caller cannot write into
one and only finds out when the review reads no scope.

`newCmd.Execute` follows `configCmd.Execute` exactly — it records the selection and `runOpts.writeTaskPaths`
does the scaffolding and the writing, through the injected stdout.

### `revmux init`

What it materializes is in **Config-management flags** above; what belongs here is the two things about it
that are not a description of the output.

**It loads the prompt tree directly rather than through `promptSet`.** A caller initializing a project has
not chosen a `--profile` yet, and refusing to materialize a tree until he names a profile he cannot read the
catalog of is backwards.

**It writes the config template through `initConfig` into `io.Discard`, not to stderr.** That function
reports what it did in prose, and the prose is not the payload — `writeInitPaths` prints the paths as JSON
instead, config included. Handing it `o.stderr` puts a second, differently-shaped account of the same write
in front of a caller parsing the first.

### `revmux stats`

Read-only aggregation of the rounds revmux already archived, printed as JSON on stdout. It opens no round,
claims nothing and writes nothing anywhere, so it is safe to call at any point, including while a review is
running.

**It declares no `--task` flag of its own.** `StatsQuery` is built from `options.TasksDir` and
`options.Task`, the fields a review already fills, exactly as `writeTaskPaths` builds `task.Round`. A second
declaration on the subcommand parses both spellings and lands them in different fields, so
`revmux --task pr-1 stats` and `revmux stats --task pr-1` would disagree about which task was asked for and
nothing else in the suite would notice.

**`stageFlow` carries `reclassified` and `refined` because `in` and `out` cannot show what verification
does.** Both counts are the union of a report's four arrays, so moving a finding into `immaterial` or
`pre_existing` changes neither — measured over one corpus, verify dropped 2 of the 150 that reached it
while demoting 21
severities, which reads as a stage doing nothing. They are each stage's own contribution, taken as growth
over the stage before it rather than as a total, so the `report` entry reports neither: `findings.json`
still carries every verdict verification wrote, and counting them there would credit the `--min-confidence`
filter with work it only passed through.

**`SourceStat.Tokens` is not one quantity.** claude sums input, output and both cache counters; codex
scrapes the number after its own `tokens used` footer. The field is honest per agent and over time, and
does not support ranking agents of different executors against each other. Its godoc says so, because the
comparison is the natural thing to reach for and it is wrong.

**Every survivor and every per-lens number comes from the per-stage snapshots, never from `findings.json`.**
That file is the `--min-confidence`-filtered report and its survivors are split across four arrays; counting
them there undercounts, and the agent that looks unproductive as a result is the one a reflection agent
proposes dropping. Two numbers come from elsewhere by design, and each field's godoc names its artifact: the
`report` entry in the stage chain reads `findings.json` precisely to measure that filter's attrition, and
`retries` is in `events.jsonl` alone, since no snapshot records a relaunch.
Survivors come from the **last** snapshot the round carries rather than a fixed name, so a `--no-verify` run
is not read as one where nothing survived — and a run that skipped both stages reports `survived` equal to
`raised`, which is honest: nothing filtered anything.

**An empty tasks root is an empty document; a `--task` naming no task is an error.** The first is a project
that has never run a review, the second is a typo — and a typo answered with zeros reads as a task with no
history, which is the `pr123`-beside-`pr-123` failure one level down. An unreadable root or task directory
fails the call for the same reason `revmux config` refuses to report `"tasks": []` for one.
A round whose artifacts will not decode is still skipped rather than fatal: an interrupted run leaves
exactly that, and `Rounds` counts what was read, so it stays the denominator of everything beside it.
**Skipping it is not the same as saying nothing about it.** The round is named in `skipped` with the reason,
the way an entry carries `rounds_error`: a corpus that quietly shrank reads as a corpus that is smaller, and
the numbers that shrank are the ones a reflection agent acts on.

**Three numbers are not read from a round's findings at all, and they are what a caller deciding what to
reclaim reads.** `size_mb` is summed from file sizes rather than block counts, so the same task measures the
same on any filesystem; it covers every directory under the task, including rounds `Rounds` does not count
and the caller's own `input/`, because that is what the disk holds. `last_run` is the `finished_at` of the
most recent round's manifest rather than an mtime, so a copied or re-read tree keeps its answer.
`description` is `task.Load`'s, the same one `revmux config` reports — absent or unparseable leaves it empty
rather than failing the call, since a corpus is not where a caller should learn his `task.md` has a typo.
They are filled after the round fold rather than accumulated through it: a round skipped for unreadable
artifacts still occupies the disk it occupies.

**The totals fold exact bytes, never the rounded megabytes each task reports.** Adding tenths across a
corpus drifts against the threshold the user is comparing them to, so `taskStats` keeps an unexported
`sizeBytes` and `SizeMB` is derived from it at every level.

**`app/archive` decodes `events.jsonl` with a local partial struct, as `history.record` already does for
`findings.json`.** The artifact package must not import the orchestrator to read back what it wrote. The
cost is that `"agent_retried"` is spelled in a second place — and `"finished_at"` in a third, in `size.go` —
and CLAUDE.md's keep-in-sync list carries both.
**Only a name the round already tallied is counted under it** — the roster the snapshot recorded, plus the
sources its findings were stamped with, which `find` fills with the executing agent's own name.
The stages retry under that same event kind — synthesis emits one naming itself — so counting every name the
log carries invents a source no roster contains and reports it beside the agents that ran.
The kind is looked for in the raw line before anything is decoded: every other event is discarded, and a
findings event carries every finding's body through the decoder to reach a field almost no line matches.

### `revmux cleanup`

The one destructive path in revmux, and a command rather than a flag so that nothing removes anything as a
side effect of doing something else. `revmux cleanup --task <id>` removes that task and prints what went as
JSON: the id, the rounds it held and its size, all measured before the tree goes, plus what the tasks root
costs afterwards. `total_mb_after` is absent when the tasks root could not be
measured after the removal: that measurement happens once the tree is already gone, so its failure omits
the number rather than failing a call that succeeded.

**It removes the whole task, never a round.** A task's rounds are one review's history and a reflection
agent reads them together, so a task that silently lost its early rounds is worse than one that is gone:
`revmux stats` would keep reporting the remainder as the whole record, and the numbers that shrank are the
ones a suggestion is built on.

**It declares no `--task` flag of its own**, for the reason `revmux stats` does not — with two declarations
the one a caller passed is the one nothing reads, and here that removes nothing while reporting success.

**A name that is not one task under the root is an error, and nothing is removed on any of them.** It runs
`task.CheckName` and then requires the name to be in `task.List`, the same enumeration `revmux config` and
`revmux stats` report, so this can only remove what those two name. An empty `--task` names the flag rather
than meaning every task; a typo is an error rather than an empty removal, which would read as a task already
gone.

**It refuses a task while any of its rounds is claimed by a live run.** That is the marker lock
`claimRound` holds for a run's lifetime, tried on every round carrying a marker rather than only the ones
`HasRun` accepts — a live run's marker is still empty, so the rounds that matter most here are exactly the
ones the round enumeration leaves out. The kernel drops that lock when the process dies, so an abandoned
round is removable and one being written right now is not; do not swap it for a pid or a timestamp.

**It is a check, not a lock held across the removal, and that is a deliberate ceiling on this command.**
The lock is released as the check moves on, and a round `revmux new` prepared has no marker to lock, so a
review claiming a round mid-removal loses it. Carrying the handles through the removal closes half of that
and costs the plumbing to do it; a task-level lock across `task.Scaffold`, `archive.New` and `Cleanup`
closes the rest and is a concurrency contract of its own. Neither is proportionate to deleting old review
logs, where the cost of the race is one interrupted review.
For the same reason `os.Root.RemoveAll` being non-atomic is reported rather than engineered around: the
error says part of the task may remain, never that it did, because nothing at that point can tell the two
apart. Do not add a quarantine directory, a rename-then-purge commit point, or a preflight walk.

**The removal goes through an `os.Root` on the tasks root**, like every other write in revmux. The name
having been checked is not containment for an operation that walks a whole tree, and the check-then-remove
window is the one the roots exist to close.

**Those five subcommands are the only carve-outs in "stdout belongs to the report", along with `--init` and
`--version`.** None of them runs a pipeline, so there is no report to collide with and no TUI to gate; every
one of them prints and exits before either exists.
