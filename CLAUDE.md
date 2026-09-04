# revmux — project notes

revmux runs a structured multi-agent code review by spawning and supervising `claude --print` and
`codex exec` subprocesses, then returns findings.
It exists because agent fan-out driven from inside an AI coding session is unobservable and unrecoverable:
agents go silent for minutes, sometimes never return, and the caller has no timeout, no kill, no retry and no progress.

A subprocess does not make the model faster.
What it buys is control — a watchdog that notices a stall, a kill and retry the caller owns,
a live view of every agent, per-agent token counts, and a run archive to debug a bad review afterwards.

**Status: the initial build is complete.**
The layout and rules below describe what is on disk; the build sequence that produced it is
`docs/plans/completed/20260726-revmux-initial-build.md`.
`site/docs.html` and `site/reference.html` are the user-facing description of the same thing, and `README.md`
is the synopsis: what revmux is, why, install, a quick start, and one compact table per subject with the site
carrying the full one.
A change to a flag, a roster key, an exit code or the JSON shape belongs in the site and in the README only
where the README states it.

## Working norms

**When writing or editing these notes — this file and `.claude/rules/*.md` — use semantic line breaks:
one sentence per line, never a giant single-line bullet.**
A 3000-character bullet is unreadable in a diff and impossible to review.

This file holds only what is specific to revmux.
If a note would be equally true of any Go project, it does not belong here.

## Build and test commands

- Build: `make build` (output: `.bin/revmux`)
- Install: `make install` (symlinks `.bin/revmux` into `$BINDIR`, default `/usr/local/bin`; `make uninstall` removes it)
- Test: `make test` (race detector + coverage, excludes mocks)
- Lint: `make lint`, which is `lint-go` plus `lint-scripts`.
  **`golangci-lint` alone is not the whole gate** — it is Go-only, and the shipped shell scripts have
  their own CI job that pipes `shellcheck` through `xargs`, so any output at all fails it, info-level
  findings included. `lint-scripts` copies that command verbatim; run `make lint`, never the
  `golangci-lint` line by itself, or a green local run still reddens master.
- Format: `make fmt`
- Generate mocks: `go generate ./...`
- Vendor after adding deps: `go mod vendor`

## Project structure

`app/` is the composition root (`package main`), split by concern.

- `app/main.go` — entrypoint + `run()`
- `app/config.go` — go-flags options, INI parsing, precedence, the config template and `--dump-defaults`
- `app/defaults/config` — the embedded, fully commented-out INI template `revmux init` materializes;
  it lives here rather than under `app/prompt/defaults/` because it is settings, not prompt content
- `app/introspect.go` — the `revmux config` subcommand and the catalog it prints
- `app/newcmd.go` — the `revmux new` subcommand, which scaffolds a round and prints the paths it created
- `app/initcmd.go` — the `revmux init` subcommand, and the `--init` flag routed into it, which materialize
  `./.revmux/` and print what is in it
- `app/statscmd.go` — the `revmux stats` subcommand, which prints the corpus `app/archive` aggregates
- `app/cleanupcmd.go` — the `revmux cleanup` subcommand, the one thing in revmux that removes anything
- `app/treewriter.go` — the one materializer `revmux init` and `--dump-defaults` both write prompt files
  through, contained by an `os.Root` on its destination
- `app/artifacts.go` — the artifacts `package main` owns: `manifest.json`, `report.md`, `findings.json`
- `app/progress.go` — the non-TTY event subscriber (timestamped lines to stderr), plus the run's closing
  summary, which the pipeline emits no event for
- `app/executor/` — supervised subprocess execution for claude and codex
- `app/prompt/` — front matter and roster parsing, lens composition, `{{VAR}}` substitution, `go:embed` defaults
- `app/pipeline/` — the three stages, fan-out, stagger, degrade policy, typed event channel
- `app/finding/` — `Finding` and `Report` types, the per-stage JSON schemas, markdown and JSON rendering
- `app/task/` — the layout constants for the task directory and the run archive alike, task and round
  enumeration, `task.md` parsing, name validation, round scaffolding
- `app/frontmatter/` — the `---` block scanner `app/prompt` and `app/task` share, so its CRLF and
  empty-block cases have one implementation rather than two that drift
- `app/archive/` — one round's artifacts, written into `<task>/<round>/` beside the caller's `input/`,
  and — in `stats.go` and `collect.go` — the corpus read back out of them, aggregated across every round
  of every task under the tasks root
- `app/ui/` — bubbletea TUI, single `Model` with state grouped into sub-structs, files split by concern
- `app/*/mocks/` — moq-generated, never edited by hand

`.claude-plugin/` and `plugins/codex/` ship the **caller** as a skill, one tree per harness.
They contain no Go and are not built; they are documentation plus four shell scripts.
The two trees carry duplicate copies of `references/` and `scripts/` on purpose — a plugin has to be
self-contained once installed, so a shared directory is not available to them.
**A change to one must be made to the other**, and every intended divergence is in `SKILL.md` and is a
capability one harness has and the other does not: script-path resolution, the harness's own way of
asking a question, and whether round preparation is delegated.
Both hand the git measuring, the task match and the context writing to one subagent, so a dozen tool
calls the user has no use for stay out of the session that reports the review — Claude Code through the
Agent tool, codex through its own built-in `spawn_agent` subagents, which are on by default in 0.146.0 and
work under `codex exec`.
The codex text names no tool: which backend is live there is chosen by model metadata rather than by the
`multi_agent_v2` feature flag, so it authorizes the workflow in words — "spawn one subagent", "wait", "use
only its final summary" — and a hardcoded `collaboration.spawn_agent` would be wrong for half the models.
Anything **not** rooted in such a capability is drift rather than divergence, and the review procedure —
what gates, which profile a re-review picks, how a finding is presented — is the same text in both.

`site/` is `revmux.com`: three hand-written HTML pages, one stylesheet, self-hosted fonts and images, and no
build step. See the Website section below.

## Hard rules

**revmux runs a review and returns findings. Nothing else.**
It does NOT do scope detection, git operations, PR fetching, issue handling, or any source modification.
It has **zero VCS dependency** — no git library, no `git` subprocess, no repo walking.
All context (scope description, goal, project profile, prior rounds) is written to disk by the caller and passed in.
Agents run diff commands themselves; revmux only substitutes a path.
`./.revmux/profile.md` is the one piece of **caller review context** it opens, and only to copy its bytes
into the round so the archive holds them; it parses nothing and reads nothing out of them.
That is a rule about review context, not about file access — revmux also reads its config files, its whole
prompt tree, its own prior `findings.json`, and, under the TUI, the bounded input snapshot below.
If a change would make revmux read a repo, the change belongs in the caller.
See `.claude/rules/pipeline.md`.

**Review context arrives as a task round, with one repo-level default underneath it.**
`--task <id>` names a directory under `--tasks-dir` (default `./.revmux/tasks`) and `--run <name>` names one
round inside it. The caller fills that round's `input/` before revmux is invoked:

```
<tasks-root>/<id>/
├── task.md                      optional, front matter describing the task itself
├── 01-initial/                  one round; a round is a direct child of the task
│   ├── input/                   CALLER-written; revmux writes nothing into it, ever
│   │   ├── scope.md    → {{SCOPE}}    required
│   │   ├── goal.md     → {{GOAL}}     optional
│   │   ├── profile.md  → {{PROFILE}}  optional; non-empty overrides ./.revmux/profile.md
│   │   └── context/    → {{CONTEXT}}  optional, any number of files
│   └── …                        revmux-owned artifacts, see the archive rule below
└── 02-after-fix/
```

**`./.revmux/profile.md` is the one input resolved outside the round, because it is the one input that is
not about the round.**
What a project is and what counts as a real failure there is the same for every task in it, so making each
round carry a copy meant round 01 of every task — usually the broad sweep — ran on whatever the caller
generated that time. Every other variable stays round-only: a repo-level scope or goal would describe a
change rather than a project.
It is the **project** layer alone — `~/.config/revmux/profile.md` is not read, since calibration that spans
repositories describes none of them — and it resolves through `projectDir`, never through `layers.project`,
which the `--config-dir ./.revmux` collapse empties for provenance reasons while the file stays where it is.
A non-empty round `input/profile.md` wins outright, and absent-or-empty means absent in both places.
The bytes are copied into the round as `prompts/input-profile.md` and `{{PROFILE}}` names that copy:
the variable expands to a path, so a round pointed at the project file would carry no record of what
calibrated it once that file changed. That copy is revmux's own artifact and goes beside the composed
prompts — **not** into `input/`, which is the caller's. A generated file landing there would win as an
explicit round override on the next attempt, which is the silent replacement this whole mechanism exists
to prevent.

**Review context belongs to the round, not to the task.**
Round 2 reviews the fixes for what round 1 found, so its scope, goal and context are all different from
round 1's — and its profile may be, though that one usually resolves to the project file both rounds share.
There is deliberately no task-level layer between the project file and the round: a task-level `profile.md`
would be caller-written context that the next round's composition overwrites, which is exactly what this
rule forbids, while the project file is settings a repository checks in once.
Holding them at task level makes composing round 2 overwrite the record of what round 1 actually reviewed,
and no archive can reconstruct that afterwards — an archived prompt carries the path, not the text.

Both names are caller-chosen and semantic: `--task pr-123 --run after-fix`.
revmux allocates neither, and **`--run` has no default**: the round is where the caller's own context lives,
so revmux cannot name one he has not filled.
A round that has already run is a load-time error rather than an overwrite, because a round that went badly
is exactly what a reflection agent needs to read.
A loop re-runs one task under successive run names and accumulates rounds.
There are no `--goal`, `--goal-file`, `--profile-file` or `--context-file` flags —
one mechanism, nothing for revmux to author, and the only precedence anywhere in it is a non-empty
round `profile.md` over the project's.

**revmux writes only inside a round, and nothing it does in the course of a review deletes anything.**
`revmux new --task <id> --run <name>` is the one thing that creates any of this, and it prints the absolute
paths the caller writes to, so no caller composes a path out of the diagram above.
Every other path opens and never creates: a typo'd `--task` is an error rather than an empty task nobody
filled.
There is no pruning and no `--keep-runs`: nothing revmux does in the course of a review, or in `new`,
`init`, `config` or `stats`, removes anything.

**`revmux cleanup` is the sole exception, and it is dedicated to being one.**
It takes one `--task` and removes that task, so nothing removes anything as a side effect of doing
something else — which is the property the rule above is really about, and it survives having a command
whose entire purpose is stated in its name.
It removes the whole task rather than a round inside it, because a task's rounds are one review's history
and a reflection agent reads them together: a history that silently lost its early rounds is worse than
one that is gone, and it is `revmux stats` reading those rounds that would go quietly wrong.
**A failure measuring the root afterwards is not a failed removal**, so `total_mb_after` is simply omitted
rather than turning a completed removal into an error.
That holds inside `archive.Cleanup` and not above it: a payload that cannot be written to stdout still
exits `2`, and the task is gone. This is a command that removes old review logs — the state is visible in
the next `revmux stats`, and it is not worth a second reporting channel to say so.
It refuses a task any round of which is claimed by a live run — the marker lock `archive.claimRound`
takes, which the kernel drops when the holder dies, so a review running right now is distinguishable from
one that was killed.
**It is a check and not a guarantee**, and deliberately: the lock is released as the check moves on, and a
round with no marker yet carries nothing to lock at all, so a review that claims a round while the removal
runs loses it. Closing that needs handles carried through the removal, or a task-level lock across
`task.Scaffold`, `archive.New` and `Cleanup`. Neither is worth it here — what is lost is one run of a
review the user can start again, and this is a command for deleting old review logs.
`RemoveAll` is not atomic either, so a failed removal may leave part of the task behind; the error says it
may, never that it did, because from there the two are indistinguishable.

**`task.md` is stored and reported, never resolved.**
Its `description`, `url`, `branch` and `base` let a caller match an existing task instead of guessing at an id
— `pr123` beside `pr-123` silently forks the history — and `revmux config` reports them back.
revmux runs no git command and fetches nothing to check any of them.
That is the zero-VCS-dependency rule, and `task.md` is exactly where it would erode.
See `.claude/rules/config.md`.

**A run archive must be sufficient to audit the review that produced it, without re-running anything.**
Visibility is only half the job: these artifacts are also the input to a later self-reflection agent that
reads a task's history and proposes changes to the lens and profile text.
Answering "which lens text raised this finding" and "did synthesis drop something real" requires more than
the final report, so a round directory holds:

```
<tasks-root>/<id>/<run>/
├── input/            the caller's own scope, goal, profile and context for this round
├── manifest.json     resolved roster, per-lens prompt provenance + content hash,
│                     requested vs. actual model per agent, timings
├── prompts/          the prompt material this run used, composed and referenced alike,
│   ├── agents/       post-substitution — exactly the bytes the model saw
│   ├── stages/       split so a roster agent named `verify` cannot overwrite a stage prompt
│   └── input-profile.md  the project profile's bytes, when the round inherited one
├── stages/           findings after find, after synthesis, after verify
├── events.jsonl      revmux's own decisions: stalls, retries, degrades, stage changes
├── agents/           verbatim tees, own subdir for the same reason
│                     <agent>.jsonl claude stream-json, <agent>.log codex prose,
│                     <agent>.retry.jsonl the second attempt when one is retried
└── report.md, findings.json
```

`input/` is part of that record rather than a neighbour of it: the round the agents were pointed at is the
round on disk, so a reflection agent reading one round in isolation sees the scope it was reviewed against.

`manifest.json` doubles as the marker that claims the round.
`archive.New` creates it with `O_CREATE|O_EXCL`, which is atomically both the already-ran check and the mark
that tells a real round from a stray directory a caller left under the task.
It is created **empty** and filled in by the finished run, so the two states are distinguishable, and they
must stay that way: an empty marker is a claim its run never came back from — an interrupt, an unwritable
artifact, every source degraded.
Such a round is re-claimed rather than refused **only while it is otherwise empty**, since the caller's own
`input/` lives inside it and burning the round over a marker carrying nothing would cost him the context he
wrote.
The marker is written first and the record last, so a run that never came back may still have written
stages, prompts, tees and `events.jsonl` — and a second run over those produces one round holding two runs'
artifacts under a manifest describing only the second.
`task.CheckReclaim` refuses that round and names what it found; nothing is deleted to make it usable, and
the caller opens a new round and copies his `input/` across.
A round that ran is `task.HasRun`, and both the prior-round inventory and `revmux config` gate on that
rather than on the file merely existing, or the round being re-run appears in its own history.

Prompt text is resolved per file across three layers, so **which file won and what it contained** must be
recorded — two rounds of one task can use different lens text, and a reflection agent comparing rounds needs
to see that.
Anything that makes a round un-auditable after the fact — dropping the composed prompt, keeping only the
final findings, reusing a round directory — defeats the purpose even when the review itself is fine.

**A failed archive write fails the run (exit `2`), with exactly one carve-out.**
A report emitted next to a half-written archive is worse than no report: it reads as complete, and the gap
only surfaces later when someone tries to audit it.
For the same reason the archive is written synchronously and is never a second subscriber on the event
channel — a Go channel distributes rather than broadcasts, so a second reader would silently take a random
half of the events. See `.claude/rules/pipeline.md`.

That carve-out is about attribution, and it may not be widened.
A **per-agent tee** under `agents/` degrades that one source instead of failing the run: it is owned by that
agent's own goroutine and is the only artifact whose failure belongs to a single source, so it is reported
through the same banner and `degraded` list every other agent failure is, rather than throwing away the
other agents' completed work.
Everything else — the manifest, the composed prompts, the stage snapshots, `events.jsonl`, the report —
either lands or the run exits `2`.

`--task` and `--run` are caller-supplied and become filesystem paths, so they are validated before use:
no separators, no `..`, not absolute, and containment re-checked on the resolved path because a symlink
defeats the lexical test.
`task.CheckName` is the single definition of that rule — `package main`, `app/archive` and `revmux new` all
delegate to it rather than carrying a copy.
A round name additionally passes `task.CheckRoundName`, which refuses the one entry the task directory keeps
beside its rounds: `task.md`.
A round carrying that name is read as the task's own metadata rather than as a round, so it is invisible to
the prior-round inventory and unreachable by the name it was created under.
Roster agent names carry the same rule, applied at load in `prompt.AgentSpec.checkName` — but not the paths
`Archive.Writer` takes, which are relative and must allow a separator because `agents/`, `stages/` and
`prompts/` all need one.
Names revmux **derives** rather than reads — a verify group's label — are sanitized instead, since their
parts are directory names, or agent names under `--verify-group-by source`, taken from the findings and
there is nobody to return an error to.

**Context variables expand to paths, never to content.**
`{{SCOPE}}` is the absolute path of `scope.md`, not the text inside it; the agent reads the file itself.
Prompt composition stats the caller's context files and never opens one, so no prompt can be bloated by a
large scope and the never-embed rule needs no per-variable judgment call. The TUI separately opens a
bounded startup snapshot for display; headless mode does not.
It does read its own prior `findings.json` files to build the history inventory — that is revmux reading
what revmux wrote, and the inventory carries counts, never findings text.
See `.claude/rules/prompts.md`.

**Prior rounds are injected into every composed prompt, and never as a `{{VAR}}`.**
revmux wrote them, so it hands them over rather than making each caller copy them forward.
A variable would be opt-in per file — any lens or profile omitting it silently loses the history — so the
composer appends the block, the same way the codex executor appends its own output contract.
The block is the task directory path plus a generated one-line inventory per round, so an agent can tell what
is there without opening anything, and it is omitted entirely on a first round.
Rounds are the task directory's own children, and one is a directory whose `manifest.json` carries a run's
record of itself — `task.HasRun`. Anything else under the task, `task.md` included, is not a round, and
neither is a round claimed by a run that never came back and left that marker empty.
**The re-evaluate-independently instruction is part of that injected block, never left to the profile body.**
An agent told a prior round flagged something tends to confirm it rather than judge it — the same anchoring
failure that makes codex a peer rather than a second pass — so the data and its guard must not be separable.

**OS-level work must never live in `app/ui`.**
The pipeline runs fully headless and emits typed events on a channel; the TUI is one subscriber and `--no-tui` is another.
No `exec.Command`, no file reads, no writes to stdout, no network in `app/ui` — not even for a small helper.
This is what makes the orchestrator testable with a mocked `CommandRunner` and no terminal.
See `.claude/rules/tui.md`.

**Executor and lens are orthogonal.**
Every roster entry composes lenses; its `model:` only selects which binary runs it, and which model of that
binary at what effort.
There is no codex-specific prompt file — the shipped `adversarial` entry composes
`lenses/adversarial.md`, and only its `model:` says codex runs it.
An agent is named for its lens, never for its binary: the exception is a profile whose agents carry
identical lens sets, where the runner is the only thing distinguishing them — `grill-me` and `expert`.
Lens text stays executor-agnostic; the output-contract difference (claude has `--json-schema`, codex does not)
is injected by the executor, never authored into a lens file.
A roster entry also carries an optional `color` — an ANSI-16 name or `#RRGGBB` — resolved in `app/prompt`
and handed to both renderers, so the TUI and `--no-tui` never color the same agent differently.

**One `model:` string is the whole runner selection, and a profile's covers the whole review.**
`<binary>[/<model>][:<effort>]` — `claude`, `claude/opus:high`, `codex/gpt-5.6-sol`. There is no `executor:`
key and no `effort:` key in any prompt file.
The three are one field because they are not independent: a model belongs to a binary, so separate keys let
a file state a pairing that cannot run, and every layer that inherited one without the other recreated it —
which is exactly how the `--lenses` override came to build a claude agent asked for `gpt-5.6-sol`.
`parseRunner` in `app/prompt/runner.go` is the only place a `Runner` is built and therefore the only place
the binary and effort vocabularies are checked; `Runner.or` is the only place one is inherited, it refuses
to carry a model across binaries, and the three fallback layers are applied in turn rather than folded
together — collapsing any two of them loses whichever the third turns out to be compatible with.

The stage files name **no** runner: they are text, and the profile's `model:` reaches the roster, the
`--lenses` agent and both stages alike, so `codex-only` is one line. A profile's optional `stages:` block is
for a deliberately mixed run and names each stage separately, so synthesis and verify can take different
models.
`app/pipeline` resolves through `Profile.Stage`, never `Set.Stage` — the latter answers what the file says,
which is not what this run will do — and `finding.StageRun` records the resolution, or a finished round
cannot say which binary produced its findings.
See `.claude/rules/prompts.md`.

**Codex is a peer source, not a second pass.**
It runs in parallel with the lens agents and its findings go through the same synthesis and verification.
Never introduce a separate codex-evaluation step or a rebuttal loop.
Primary/secondary ordering means the second reviewer sees the first's findings and anchors on them,
which destroys the independence the cross-source confidence boost depends on.
The fix-and-re-review loop lives in the caller, which re-runs revmux against the same `--task` under a new
`--run` name; revmux injects the prior rounds itself.

**Findings go to stdout as JSON, everything else to tty/stderr.**
revmux is driven by a caller model that parses what comes back, so the machine shape is the default and
`--markdown` opts into the rendered one. The two human-facing renderings are the TUI, on screen while
the run happens, and `report.md` in the archive, on disk afterwards — markdown on stdout served neither
and had to be parsed back out by everything that did read it.
The TUI renders to the tty, progress lines go to stderr, and only the report is written to stdout.
That is what makes `revmux > findings.json` work with the TUI running at the same time.
Never print a status message, warning or banner to stdout.
Gate the TUI on the tty being openable — never on stdout being a TTY, which is false whenever the report is redirected.
The only exceptions are the five subcommands — `revmux config`, `revmux new`, `revmux init`,
`revmux stats` and `revmux cleanup` — plus `--init`, which is the flag spelling of one of them, and
`--version`.
All of them print on stdout and exit before any pipeline, archive or TUI exists, so there is no report for
any of them to collide with.
Each writes it from `runOpts` through the injected writer rather than from its own `Execute`, which go-flags
calls during parsing while stdout is still the real `os.Stdout` and nothing can capture it.

**revmux is driven by a caller model, so its configuration is machine-readable.**
`revmux config` reports what actually resolved — runtime knobs with the precedence layer that supplied
each, every profile with its full roster, every lens and stage with its `description:` one-liner, and the
`executor` and `effort` vocabularies read from the same constants that validate them.
A caller composing `--lenses` has no other way to learn what a lens covers, and a catalog describing the
embedded defaults while the user has overrides describes a review that will not happen.
`paths.tasks` carries the same idea down to the task store: each task's id, whatever its `task.md` says about
it, and the rounds already recorded under it.
That is what a caller matches an in-flight review against — an id alone leaves it minting `pr123` beside an
existing `pr-123`.

**`revmux init` materializes what resolved; `revmux stats` reports what the archive recorded.**
Between them a caller can localize the review configuration and then judge it from the runs it produced,
which is what makes the configuration something other than text authored once and never revisited.
`init` writes the winning layer's own bytes into `./.revmux/`, front matter included, and never overwrites a
prompt file — materializing the embedded text under a user who has overrides hands him a tree that reverts
his review, and a body-only write produces one the next `prompt.Load` rejects.
The config is the deliberate exception, because settings and prompt text are different kinds of thing: a
`./.revmux/config` holding no uncommented key is rewritten with the current template, which is what lets an
upgrade move a default the user never set. Only one carrying a setting is left alone, so `created` in the
payload describes the prompt files and the config is reported as a path.
`--dump-defaults` stays the opposite direction and the only way to reach the embedded bytes.
**Both write through `treeWriter`, which is one implementation of "skip what is already there, create what
is not" and one guarantee about where a write may land.**
Two copies of that rule diverge the moment one of them is hardened, and the divergence is silent: the paths
reported back name the destination whether or not the bytes went there.
Containment is structural rather than checked — an `os.Root` on the destination, the way `task.Scaffold`
walks the tasks root — because a joined path contains its last component alone, and `os.Lstat` dereferences
every directory above it.
`stats` takes every survivor and every per-lens number from the per-stage snapshots, never from
`findings.json`, which is the `--min-confidence`-filtered report: counting survivors there undercounts them,
and the agent that looks unproductive as a result is the one a reflection agent drops.
Two numbers come from elsewhere by design, and each says so in its godoc — the `report` entry in the stage
chain reads `findings.json` precisely to measure what that filter removed, and `retries` is recorded by
`events.jsonl` alone, since no stage snapshot carries a relaunch.
It opens no round, claims nothing and writes nothing, and it enumerates tasks through `task.List` exactly as
`revmux config` does, so the two can never name different task sets.
A round whose artifacts will not decode is named in `skipped` rather than dropped without a word: three
rounds where five ran reads as a smaller corpus, and the numbers that shrank are the ones a reflection agent
acts on.

**The layout is revmux's own detail, so a caller is handed paths rather than a shape to reproduce.**
`revmux new --task <id> --run <name>` creates the round and prints every path the caller writes to, absolute,
plus which of them this call created.
A caller that constructs `<tasks-dir>/<task>/<run>/input/scope.md` itself has reimplemented the layout from a
document, and the next layout change breaks it silently.

**A source is a process.**
The cross-source confidence boost counts distinct processes, never tags and never lenses.
An agent carrying two lenses that flags the same issue under both is still one source — it cannot corroborate itself.
The pipeline knows which process emitted which finding, so the count is structurally correct. Keep it that way.

The wire format enforces the distinction with two fields that must never be conflated.
`Finding.sources` holds **agent names** (`["bugs+impl", "adversarial"]`) and is the only input to the boost.
`Finding.lenses` holds the lens names that raised it (`["bugs", "adversarial"]`) and is informational —
it answers "why was this reported", never "how many independently agreed".
A `sources` array holding lens names inflates confidence on exactly the single-agent case the rule exists to catch.

**Go assigns `sources`, never the model.**
`find` overwrites the field on every parsed finding with the executing agent's name and validates `lenses`
against that agent's configured lens set.
**No schema ever exposes `sources`** — a field the model can fill is a field it will fill, and one agent
naming itself twice is self-corroboration.
`FinderSchema` omits `verdict` for the same reason; `VerifySchema` is the one place a model returns one,
because asking for a verdict is what that stage is for.
Stamping happens in `find`, not synthesis, or `--no-synthesis` runs carry invented attribution into the report.

## Keep-in-sync conventions

- A new CLI flag needs: the `options` struct tag and the flag table in `site/reference.html`.
  A runtime knob needs the knob table in `site/docs.html` as well, which is the same set with the reasons
  attached, and the README only where the README already states the thing being changed.
  An INI-backed one also needs a commented-out entry in `app/defaults/config`, the template `revmux init` writes —
  not `--dump-defaults`, which extracts the prompt tree and knows nothing about settings —
  plus the runtime-knob list in `.claude/rules/config.md`, which names the set literally and goes stale silently.
  It is reported by `revmux config` automatically: `knobs` is built by reflection over the `options` struct.
  A flag whose `choice:` vocabulary another package matches on spells that word twice, since a struct tag
  cannot name a constant: `--verify-group-by`'s `source` is `choice:"source"` in `app/config.go` and
  `groupBySource` in `app/pipeline/verify.go`. Renaming one side compiles and passes, and leaves the mode
  reachable by a flag value that silently behaves as the default.
- A new roster key needs: the `agentYAML` field it parses into, the `AgentSpec` field it resolves to,
  front-matter validation, and the profile examples in `site/docs.html` and `.claude/rules/prompts.md`.
- A new **profile-level** key needs the same four, in `profileYAML` and on `Profile`, plus the front-matter
  key table in `site/reference.html`, which enumerates where each key is accepted and goes stale silently.
  One that changes what a stage resolves to needs `revmux config` as a fifth site: `profileInfo.Stages`
  reports the resolution rather than the override, so a key it does not carry is invisible to the caller
  choosing the profile.
- A change to the `model:` grammar is `app/prompt/runner.go` plus every authored file that uses it: eight
  shipped profiles, both stage files, the model-string sections of `site/docs.html` and
  `site/reference.html`, `.claude/rules/prompts.md`, and the
  fixtures in `app/prompt` and `app/pipeline` tests. The parsed form reaches `AgentSpec`, `Stage` and
  `RunnerSpec` as three separate fields, so nothing downstream of the parser changes — which is what keeps
  `revmux config`, `manifest.json` and `finding.SourceStat` out of that list.
- A new pipeline `EventKind` needs a case in `app/progress.go` **and** in the TUI — in `agentState.track`
  for an agent-scoped kind, or in `Model.apply` for one that is not, since `apply` dispatches everything
  else to `track` and both switches end in a `default` that renders nothing.
  **Renaming one needs `app/archive` as a third site.** `collect.go` reads `events.jsonl` back through a
  local partial struct that spells `"agent_retried"` itself, rather than importing the orchestrator into
  the artifact package, so a rename that misses it compiles, passes and silently reports every agent's
  retry count as zero — which reads as supervision never having to intervene.
  The stage names are the same duplication for the same reason: `app/archive` spells `find`, `synthesis`,
  `verify` and `report` itself, and `stageFlow.Name` puts them in the JSON beside the ones `app/pipeline`
  fills `finding.Report.Stats.Stages[].Name` with. A rename on one side compiles and passes, and leaves one
  archive's two arrays calling the same stage different things.
  `manifest.json`'s `finished_at` is a third instance of it: `app/archive/size.go` decodes the marker
  through its own partial struct to date a task, rather than importing `package main`'s manifest type,
  so renaming that field in `app/artifacts.go` compiles and passes and leaves every task reporting no
  `last_run` — which reads as a corpus of reviews that never completed.
- A new subcommand needs: the `options` field with its `command:` tag, the `show*` selection field, the
  back-pointer in `parseArgs`, and the case in `run()`. Then seven places enumerate the set literally and go
  stale silently — the stdout carve-out above, the same list in `.claude/rules/config.md`, the
  "all five print JSON" sentence plus the table in README, the subcommand table in `site/reference.html`,
  the per-command sections and their sidebar entries in `site/docs.html`, the project-structure list in this
  file, and the subcommand sections in **both** skill trees.
- A new lens file needs an entry in at least one shipped profile, or nothing will ever run it.
  Then the lens table in `site/docs.html`, the two split by what they read in `site/reference.html`, the
  lens names the landing page's profiles section spells out in prose, the layout block in
  `.claude/rules/prompts.md`, the lens table in
  `references/invocation.md` in **both** skill trees, and three literal inventories in the tests: the name
  set in `prompt_test.go`, and in `defaults_test.go` both the count and the message enumerating every
  shipped lens by name.
  There are thirteen — eight reading a change, five reading a filed item — and nothing derives that number,
  so each site goes stale silently. `site/llms.txt` states the split rather than the names, and stays as it
  is unless the balance changes.
  The body is constrained by the shipped-file contracts: it opens with `## Lens: <name>`, carries a
  `description:` one-liner and no `{{VAR}}`, names no executor or output format, and mentions no prior
  round. `TestDefaults_NoShippedFileCarriesThePriorRoundBlock` iterates lenses as well as profiles, which
  is what any lens describing how a project decided something before will trip.
- A new **profile** needs: the file under `app/prompt/defaults/prompts/profiles/`, the shipped-profile
  table in README and the install paragraph naming which binaries each profile needs, the three profile
  tables on the site — `site/index.html`, `site/docs.html`, and `site/reference.html`, whose table carries
  the binaries as a column of its own — plus the prompt-tree diagram in `site/docs.html` and the
  layout block in `.claude/rules/prompts.md`.
  Then four sites in **each** skill tree, two per file: `SKILL.md`'s profile table and its "the user
  says" mapping, and `references/invocation.md`'s profile table and the executor count in its
  `Environment` bullet.
  That last one is the one this list exists for. It is a *count* rather than a row, so nothing about
  adding a profile draws attention to it, and `SKILL.md` points an executing agent at
  `references/invocation.md` as the authority on profiles — so a stale count there has the skill
  contradicting the binary.
  The `diff -r` rule below does not cover it: that governs mirroring an edit between the two trees,
  which is a different question from finding the file in the first place.
  Then the counts in this section, which name the set literally and go stale silently — the
  three bullets below and the `model:` grammar bullet above.
  Four tests enumerate the set: `prompt_test.go` twice, `initcmd_test.go` and `main_test.go` assert the
  exact names, and `defaults_test.go` asserts how many there are. Those are inventories and are meant to
  be literal. `TestDefaults_SeverityContract` is **not** — it derives the full profiles from
  `ProfileNames()` so a new one is guarded without being listed, which is what a contract check has to do
  and what a literal list there silently stopped doing.
  It names three exemptions, and each is a profile whose bar is deliberately not the shared one: `final`
  reports two severities rather than three, `triage` rates how much a point bears on a decision rather
  than what goes wrong at runtime, and `expert` reviews a plan as readily as a diff so it rates what goes
  wrong if the thing is built and run as written.
  Each is pinned by its own assertions instead, so an exemption removes a profile from the equality
  check and never from the test.
  **An exemption is for a bar that is meant to differ, never for one that has drifted** — a name added to
  that list to make a failing run pass is the mechanism the derived-from-`ProfileNames()` design exists to
  prevent, and the three that are there each name the review shape their bar is written for.
- **The severity bar is duplicated in every profile body** and nothing composes it from one place:
  `comprehensive`, `codex-only`, `claude-only`, `focused` and `grill-me` carry a byte-identical
  `## Severity bar` section, `final` carries a two-severity variant of the same text, `triage` carries
  a bar that is not a variant of it at all — its severities rate how much a point bears on the decision,
  because it reads a filed item and there is no runtime for anything to go wrong at — and `expert` carries
  a third shape, rating what goes wrong if the thing is built and run as written, because what it reviews
  may be a plan rather than a change.
  A change to what a severity means is eight edits, and the five identical copies must stay identical —
  two profiles disagreeing about what `major` is means the same defect gates one review shape and not
  another, which reads as the model being inconsistent rather than the prompts being out of step.
  A code-review wording change stops at the five: propagating it into `triage` or `expert` is what the
  separate bars exist to prevent.
  `.claude/rules/prompts.md` calls the body "the shared preamble and severity bar"; shared across the
  agents of one run, not across profiles.
  **`## What not to report` is the second such block**, byte-identical in six of the eight, and it is the
  one a finder consults before writing anything down — so a rule added to one profile and not the others
  makes the same finding reportable under one review shape and suppressed under another.
  `triage` and `expert` are the exceptions and carry their own, since the shipped copy is written about
  diffs and defers to the linters and tests a review runs after: a panel reading an issue is doing
  neither, and `expert` may be reading a plan where nothing ran and no line was touched.
  `TestDefaults_WhatNotToReportContract` exempts both by name and asserts their blocks separately, exactly
  as the severity contract does — the equality check over the other six is not weakened to accommodate
  either.
  **`grill-me`'s `## Stance` section is the third**, and it duplicates a *lens* rather than another
  profile: roughly half of `lenses/adversarial.md` is copied into it verbatim, including the "Work the
  seams" bullets and the two paragraphs on titling the mechanism.
  The copy exists because that profile puts the adversarial stance on all four agents, where it is
  preamble rather than a lens — but the six profiles that name `adversarial` compose that file itself, so an
  edit there leaves `grill-me` stale and the two shapes then instruct agents differently about severity
  inflation in the profile whose whole premise is pushing against that bar.
  What is deliberately **not** copied stays not copied: `adversarial.md` tells its agent not to repeat
  what a careful first read surfaces, which is the opposite of what `grill-me` needs from four agents.
- A change to the task-directory layout starts at the constants in `app/task`, which `app/archive`,
  `app/pipeline` and `package main` join every path from — the run's own artifacts included, so a stage
  snapshot is named once and read back by that name.
  No layout name is spelled anywhere else. What does not follow them
  is everything that *describes* the shape: `task.Paths` and its JSON field names, the round tree in README,
  the two in this file, the round and archive trees on all three site pages, and both skill trees.
  **Seven files draw an actual tree diagram**, and that is the set a new artifact has to appear in:
  this file, `README.md`, `site/index.html`, `site/docs.html`, `site/reference.html`, and
  `references/output.md` in **each** skill tree. That last pair is the one this bullet used to miss — it
  points at `task-dir.md`, which describes `input/` and draws no archive tree at all, so an artifact added
  to the round is invisible to the two files a caller actually reads it out of.
- A new `task.md` front-matter key needs: the `Meta` field with both a `yaml` and a `json` tag, and the
  commented-out line in `app/task`'s scaffolded template.
  `revmux config` reports it for free, since `taskInfo` embeds `Meta` rather than copying its fields.
  Seven files enumerate the keys literally and do not, and they split by what they are enumerating.
  Describing `task.md` itself: `site/docs.html` (its `task.md` section **and** the `paths.tasks` sample
  payload in the `revmux config` section), the `task.md` key table in `site/reference.html`,
  `.claude/rules/prompts.md`, `references/task-dir.md` — in its
  `task.md` example and its "Each entry carries" line — and the `SKILL.md` step that writes it.
  Describing what a `paths.tasks` entry carries: `references/invocation.md`, and `SKILL.md`'s own
  "Entries carry" line, which is a second place inside a file the write step already put on the list.
  Then `scripts/task-state.sh`, in both its usage header and the hardcoded
  `for key in description url branch base meta_error rounds_error` loop.
  The last four are in **both** skill trees.
- Anything the shipped skill documents — a flag, a profile, the JSON shape, an exit code, the task
  directory layout — needs the same edit in **both** skill trees, since they hold duplicate copies of
  `references/` and `scripts/`. A `diff -r` of the two `references/` and `scripts/` directories must
  come back empty; only `SKILL.md` differs.
  A **new** reference file additionally needs the `references/` line in `plugins/codex/README.md`, which
  enumerates them by name and goes stale silently — the `SKILL.md` pointer to it is what an agent
  follows, so nothing fails when that listing drops one.
  A **new script** needs the same listing, the script sentence in README's skill section, the script list in
  `site/docs.html`, and a line under
  `plugins/codex/README.md`'s requirements when it needs anything the other scripts do not.
  **A script exists so the knowledge in it is not re-derived per session.** `analyze-corpus.py` is the
  case that made the rule: the readings it encodes were each got wrong by hand first, and ad-hoc `jq`
  over the archive reproduces those mistakes. Anything the skill would otherwise work out from the
  archive every time belongs in a script with tests, not in prose telling an agent how to work it out.
- **The skill is documentation of this binary, so a change to the binary updates it in the same commit.**
  It states revmux's flags, profiles, JSON field names, exit codes and archive layout as fact, and an
  agent executes what it says without checking. A skill describing a flag that no longer behaves that way
  is worse than one that omits it: the caller acts on it confidently and has to recover afterwards.
  Treat `.claude-plugin/skills/` and `plugins/codex/` as consumers of `app/config.go`, `app/finding/`
  and `app/archive/` the way `README.md` and `site/` are.
- **The plugin version is maintainer work, never a contributor's.**
  It is stated twice — `version:` in `.claude-plugin/plugin.json` and in the `plugins` entry of
  `.claude-plugin/marketplace.json` — and the two must match.
  A contributor's PR carries whatever version its base had, so a rebase leaves it stale rather than wrong;
  bump it after merging rather than asking him to.
- **How revmux is installed is stated in twelve places across eleven files**, and every one of them leads
  with Homebrew, since that is the path a reader should take.
  The README's install section; `site/index.html` twice, in the hero copy line and in the install section;
  `site/docs.html`'s install section; `site/llms.txt`; `SKILL.md`'s absent-revmux block, `preflight.sh`'s
  missing-binary hint and `launch-revmux.sh`'s, each in **both** skill trees; and the requirements list in
  `plugins/codex/README.md`.
  The two scripts print theirs at the moment a user is blocked, so a stale command there is read as the
  answer rather than as documentation.
  What backs it is `homebrew_casks:` in `.goreleaser.yml`, published to `umputun/homebrew-apps` when a `v*`
  tag is pushed. It is a cask rather than a formula — the `brews:` key is deprecated — so the tap entry lands
  in `Casks/`, a post-install hook clears the macOS quarantine flag from the unsigned binary, and Linux is
  served by the release archives and the `.deb` and `.rpm` packages rather than by brew, which supports
  casks on macOS only.
  `goreleaser check` is the gate on that file and must pass; `goreleaser release --snapshot --clean
  --skip=publish` renders the cask into `dist/homebrew/Casks/` without publishing anything.
- A new prompt input — a variable, an injected block, a per-agent knob — needs a matching record in
  `manifest.json` or the archived prompt, or a reflection agent cannot tell what shaped the review.
- Changing any of the three stage schemas means changing the embedded JSON under `app/finding/`
  (`finder-schema.json`, `synthesis-schema.json`, `verify-schema.json` — `schema.go` only embeds them),
  the `Report.JSON` shape, the report sample in README, the three on the site — the landing page's, the one
  in `site/docs.html` and the field table in `site/reference.html` — and every recorded executor fixture
  carrying a `structured_output`.
  `finder-schema.json` is the harder one: `app/executor/testdata/finder-schema.json` is the copy the live
  capture was recorded under and is authoritative, so both files move together or the executor tests assert
  a shape the CLI never emitted.

## Website

`revmux.com` is the static `site/` directory on Cloudflare Pages: no build step, no framework, no
repository deploy config, and canonical URLs omit `.html` because Pages redirects with 308.
Three pages carry the whole thing.
`site/index.html` is what revmux is and why, `site/docs.html` is the canonical user guide, and
`site/reference.html` is every flag, key, field, verdict and exit code with no explanation attached.
`site/llms.txt` is the crawler-oriented summary and discovery index.

**The site is documentation of this binary, so a change to the binary updates it in the same commit** —
the same rule the skill trees carry, and for the same reason.
The keep-in-sync bullets above name which page holds what; the short version is that anything a reader could
act on lives in `docs.html`, anything they would look up lives in `reference.html`, and the landing page
repeats only what it needs to make the case.
The README is the synopsis and keeps a compact table where the site keeps the full one, so a table that
exists in both is edited in both.

Assets are self-hosted and there is nothing to build.
`style.css` holds the whole design system as CSS custom properties: graphite ground, warm paper ink, amber
signal, and the severity palette (`--crimson`, `--amber`, `--slate`) doubling as the accent set, so a color
used anywhere on the site comes from a token rather than a literal.
Fonts are latin-subset woff2 of Newsreader and IBM Plex Mono, preloaded in every page head.
`assets/favicon.svg` is the source for both PNG favicons and `assets/revmux-og.svg` for the 1200x630 social
card; both are rasterized with `rsvg-convert`, and editing the SVG without re-rendering the PNG leaves the
two disagreeing.
Screenshots are WebP at 1600px wide, converted with `magick <src> -resize 1600x -quality 82`, and they carry
explicit `width`/`height` so the page does not shift as they load.

The only JavaScript is the copy-to-clipboard handler at the foot of `index.html`.
The docs and reference drawers, the mobile nav and the hero animation are CSS alone, so a page with scripts
disabled still works.
Every animation is disabled under `prefers-reduced-motion`.

## Subsystem notes (path-scoped rules)

Detailed per-subsystem engineering notes live in `.claude/rules/*.md`, each scoped with `paths:` frontmatter.

- `.claude/rules/executor.md` — subprocess supervision, verified `claude` and `codex` CLI behavior, stream decoding
- `.claude/rules/pipeline.md` — the three-stage contract, degrade policy, stagger, event channel
- `.claude/rules/prompts.md` — front matter, roster resolution, lens composition, config precedence
- `.claude/rules/tui.md` — bubbletea conventions and the lipgloss/ANSI traps
- `.claude/rules/config.md` — go-flags plus INI, flag description style, context resolution
- `.claude/rules/testing.md` — fixtures, mocks, and why no test may spawn a real model
