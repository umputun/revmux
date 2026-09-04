# Invoking revmux — flags, profiles, timing and trust

## The shape of a call

```
revmux new  --task <id> --run <name>                                          > paths.json
revmux      --task <id> --run <name> [--profile <name> | --lenses a,b] [--no-tui] [flags] > findings.json
```

`--task` and `--run` are both required and neither has a default; everything else does. `revmux new`
creates the round and reports the paths to fill; the review reads what was written there and creates
nothing. See `task-dir.md`.

Four more subcommands take no round at all — `revmux config`, `revmux stats`, `revmux init` and
`revmux cleanup`. Each prints JSON on stdout, runs no pipeline and exits before one exists; all four are
covered below.

## Run it in the background, and do not poll

A review is not a fast command. The find stage runs several agents in parallel with a launch stagger,
then synthesis is one more model call, then verification is another fan-out. **Three to fifteen
minutes is normal**, and the `comprehensive` profile with a codex peer sits at the upper end of that.

Most agent harnesses cap a foreground shell command well below that, so a foreground call will be cut
off partway through. Launching it in the background is the only reliable pattern:

```bash
revmux --task pr-123 --run 01-initial --no-tui > /tmp/revmux-pr-123.json 2> /tmp/revmux-pr-123.log
```

- run it with the harness's background flag, then wait for the completion notification
- **do not poll in a loop** and do not sleep-and-check; a background launch reports its own exit
- redirect stdout to a file — that is the report, and it is the only thing on stdout
- redirect stderr to a second file — that is the progress renderer, useful for diagnosing a bad run
- `--no-tui` explicitly, so the plain stderr renderer is what runs rather than depending on whether a
  tty happened to be openable

Timeouts are configurable if a run is genuinely stuck rather than slow: `--idle-timeout` (default
`2m`) kills and retries an agent that has produced no output for that long, and `--hard-timeout`
(default `20m`) caps a single attempt. Raising them makes a stalled run take longer to fail, not more
likely to succeed.

## Relay the milestones while it runs

A headless run says nothing for ten minutes, and a user who cannot see the TUI has no other signal
that anything is happening. **This is for that form only.** An overlay run puts the TUI in front of
him, so a relay there narrates what he is already watching and is noise by definition.

Watch the round's `events.jsonl`: it is the run's own decision record,
written a line at a time as the pipeline emits each event, and being structured it can be filtered
down to the milestones exactly rather than by matching words in prose.

```bash
tail -n +1 -F <round_dir>/events.jsonl \
  | grep -E --line-buffered '"kind":"(stage|agent_started|agent_done|agent_retried|agent_degraded|dropped|rate_limit)"'
```

Those seven kinds are the whole vocabulary worth passing on — a stage boundary, an agent starting or
finishing, a retry, a degrade, what synthesis dropped, a rate limit. `agent_activity` and `agent_progress` are the model's own
prose and tool calls; they arrive continuously, and relaying them turns the feed into a firehose the
user has to read to find the two lines that mattered.

The stderr log renders the same events for a human, but its agent column is ANSI-painted and its text
is model prose, so a filter over it matches whatever an agent happened to say. Read the log to
diagnose a run; filter `events.jsonl` to follow one.

`events.jsonl` is created when the pipeline starts, a moment after launch, so the watch uses `-F`
rather than `-f` to survive that gap. A run that fails before then writes none, and its own exit
reports that.

**Report what happened, at most once a minute, and nothing in between.** One short line folding
together every event since the last one, rather than a line each: four agents start within seconds of
one another at launch, and four messages saying so is the same noise arriving faster. A retry, a
degrade or a rate limit is the exception and goes out when it arrives, since it changes what the user
would do next. A quiet stretch is an agent thinking:
there is nothing to report, and a line reporting it is worse than silence, because it is the same
status the user already had, restated on a timer. Never send a heartbeat, a "no change since the last
check", or a restatement of a milestone already relayed — those are what makes a progress feed
something the user turns off.

## Overlay mode — running it with the TUI on screen

revmux ships a live TUI: a status table with one row per supervised process (name, state, elapsed,
last activity), a per-agent scrollback tab for each, and a findings browser once the report arrives.
It renders to the **tty**, and is gated on the tty being openable — never on stdout being a terminal,
which is false in exactly the `> findings.json` case where a user is most likely watching.

An agent's own shell has no tty, so the TUI never appears there. `scripts/launch-revmux.sh` runs
revmux in a terminal overlay instead:

```bash
scripts/launch-revmux.sh --task pr-123 --run 01-initial > findings.json
```

It takes every revmux flag, forwards them unchanged, and returns the report on stdout with revmux's
own exit code — so it is a drop-in substitute for the direct call, differing only in that the review
is visible while it runs.

### Backends, in detection order

| backend | detected by | how it opens |
|---|---|---|
| agterm | `$AGTERM_SESSION_ID`, or one Git checkout match, plus `agtermctl` | floating panel at 80%, or a pane overlay in a split, blocking |
| tmux window | `$TMUX` + `REVMUX_TMUX_WINDOW=1`, or agent-deck | server-owned window, survives a client drop |
| tmux popup | `$TMUX` | `display-popup -E` at 90% |
| zellij | `$ZELLIJ` | floating pane at 90% |
| herdr | `$HERDR_ENV=1` | new fullscreen tab in the caller's workspace |
| kitty | `$KITTY_LISTEN_ON` | overlay window |
| wezterm / kaku | `$WEZTERM_PANE` | bottom split at 90% |
| cmux | `$CMUX_SURFACE_ID` and friends | downward split |
| ghostty | `$TERM_PROGRAM=ghostty` | zoomed split via AppleScript |
| iTerm2 | `$ITERM_SESSION_ID` | split, direction chosen from pane geometry |
| Emacs vterm | `$INSIDE_EMACS=vterm` | new vterm buffer in its own frame |

Order matters where environments overlap: herdr is checked before kitty because herdr-in-kitty sets
`KITTY_LISTEN_ON`, and cmux before ghostty because cmux can expose Ghostty's environment variables.

When Codex drops AGTERM variables or runs from a temporary worktree, the launcher queries the live
tree and compares absolute Git common directories. It adopts the session and active pane only when
exactly one live session matches. Zero or multiple matches fall through to other backends.

Under agterm the floating panel is centered over the whole session, so in a **visible split** it covers
the sibling pane's work and frames the review inside something narrower than the pane it runs in.
There the launcher opens a pane-scoped overlay on `$AGTERM_PANE` instead, leaving the sibling live.
Both conditions are checked, never assumed: `--pane` reached `agtermctl` after v0.9.0, and reading the
split state needs `jq`. Without either, the floating panel stands — and so it does when agterm refuses
the pane open outright, since the split can go away between the check and the call.

Either shape gets a background tinted 3% toward blue — derived from the session's own color, else the
resolved theme's `background`. A pane overlay is always full-pane and would otherwise render as the
shell it replaced; the framed panel carries the same tint so a review looks like one tool whether or
not the session happened to be split.

### Environment overrides

| variable | default | effect |
|---|---|---|
| `REVMUX_AGTERM_PERCENT` | `80` | agterm floating panel size, 1-100; setting it also forces the floating panel in a split |
| `REVMUX_POPUP_WIDTH` | `90%` | tmux and zellij popup width |
| `REVMUX_POPUP_HEIGHT` | `90%` | tmux, zellij and wezterm popup height |
| `REVMUX_AUTO_EXIT` | `30s` | TUI self-close delay; `0` waits for the reader to quit |
| `REVMUX_TMUX_WINDOW` | unset | `1` forces window mode, `0` forces the popup |

### Why the launcher forwards PATH

revmux spawns `claude` and `codex` itself, and overlay backends start children from a server process
whose environment predates the user's shell rc files. Without forwarding, every agent degrades on a
binary that is plainly installed and the run exits `2`.

`HOME`, `XDG_CONFIG_HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `TMPDIR` are forwarded for the same
reason — they decide where the CLIs and revmux look for configuration and auth.

`ANTHROPIC_API_KEY` is **not** forwarded. An `env KEY=VAL` prefix places the value in the process
argv, where any `ps` on the machine can read it. revmux strips that variable from its children by
default anyway, so overlay runs use interactive subscription auth. Use headless mode for key-based
auth, where the variable is inherited normally rather than passed on a command line.

### Auto-exit

revmux leaves the TUI open until the reader quits with `q` or `ctrl+c` (`--auto-exit=0s`). In an overlay opened on the
user's behalf, that blocks the launcher indefinitely if nobody returns to it, so the launcher injects
`--auto-exit=30s` unless a value was passed. `REVMUX_AUTO_EXIT=0` restores waiting for the reader.

### Recovering a report when the launcher dies

A run that **completed** archives itself before anything reaches stdout, so its `findings.json` and
`report.md` are in the round directory whatever happened to the launcher afterwards — a timeout on the
launcher, a closed terminal, a lost pipe. Read from there rather than re-running a completed review.

A run that was **killed** wrote no report at all: archiving happens after the pipeline returns, so there
is nothing on disk to recover. A `2` is usually the same, with one exception — the report reaches
stdout only after the round is archived, so a `2` whose stderr names a failure writing it (a full disk
on the redirect target, say) leaves a complete round behind. Check the round's `manifest.json` for
content before re-running.

Re-run under the **same** `--run` name only while nothing else was written into that round. That is
narrower than it sounds: the pipeline opens `events.jsonl` as its first act, before any agent launches,
so a run interrupted at any point after it started is refused. One holding `events.jsonl`, stage
snapshots, agent tees or composed prompts is refused under its own name, because a second run there
would leave one round holding two runs' artifacts under a manifest describing only the second. The
error names what it found; nothing is deleted to make the round usable. Open the next round and copy
the `input/` across.

## Exit codes — `1` is a normal outcome

| Code | Meaning |
|---|---|
| `0` | ran fine, no findings above `--min-confidence` |
| `1` | ran fine, **findings were reported** |
| `2` | tool error — nothing usable was produced |

`launch-revmux.sh` passes those through and adds two of its own, outside revmux's vocabulary:

| Code | Meaning |
|---|---|
| `3` | launcher failure — revmux never ran, or the overlay died before it finished |
| `127` | revmux not installed |

`3` is the one to retry: no review happened. A launcher must never exit `0`, `1` or `2`, since a
caller is told not to re-run on `1`.

**Exit `1` is success with findings. It is not a failure and must never be retried as one.** This is
the most common way to misuse revmux: a caller treats nonzero as an error, discards a complete report,
and re-runs a fifteen-minute review to get the same answer.

Exit `2` covers: bad config, an unreadable prompt tree, an omitted `--run`, a round with no `input/` or
an empty `scope.md`, a round that has already run, an unwritable run artifact, and the case where every
source degraded. It also covers a delivered `SIGINT`/`SIGTERM`. On `2`, read stderr — the message names
which of these it was.

The subcommands use `2` for their own tool errors and can never exit `1`, since none of them produces a
report: a `revmux stats --task` naming no task under the tasks root, a tasks root or task directory that
will not read, and an unwritable `./.revmux/` under `revmux init`.

## Choosing a profile

| profile | roster | when |
|---|---|---|
| `comprehensive` | `bugs+impl`, `arch+quality`, `docs+tests` on claude, plus an adversarial codex peer | default; a real change with real risk |
| `focused` | one `bugs` agent plus the codex peer | small or time-boxed change, correctness is the concern |
| `final` | `bugs+impl` plus the codex peer, nothing below major reported | last look before merging |
| `claude-only` | `bugs+impl`, `arch+quality`, `docs+tests`, `adversarial` — all on claude | codex is unavailable or unwanted |
| `codex-only` | the same four splits on codex, synthesis and verify included — no claude anywhere | claude is unavailable or unwanted |
| `grill-me` | `bugs+impl` and `architecture+quality`, each run once on claude and once on codex, every agent reading against the change | the user asked to be grilled; corroboration between two vendors on one lens pair is the point |
| `expert` | two agents at the highest effort — codex `gpt-5.6-sol:xhigh` and claude `fable:xhigh` — each carrying all eight lenses, both stages on fable | a plan, or a change nobody wants to get wrong. Both agents read everything, so agreement between them is real corroboration rather than two halves of one review |
| `triage` | `facts` (grounding + precedent), `thesis`, `antithesis` on claude, plus `cost` on codex | the subject is a filed item rather than a diff — an issue, a proposal, a discussion |

`--profile <name>`. The default is `comprehensive` and is itself a config knob.

`triage` is the one shipped profile that needs flags beside it: `--no-synthesis`, because every argument
a panel produces is single-source and the drop rule eats them, and `--verify-group-by source`, so each
panelist's case is verified apart from the case answering it. `references/triage.md` is the procedure.

`expert` needs no extra flags, and **is never selected on the caller's own judgment**. Two agents at
`xhigh` each applying every lens costs several times what `comprehensive` does, and nothing about the
subject earns it — not a plan, not a large diff, not a risky one. It runs when the user asked for it in
words, and not otherwise.

What its body does differently is only calibration: the severity bar rates what goes wrong if the thing
is built and run as written rather than what goes wrong at runtime, and the what-not-to-report block
distinguishes reviewing a change from reviewing a proposal. Every profile reviews whatever `scope.md`
points at, a plan included; `expert` is the one whose wording does not assume a diff.

## Lenses

| lens | covers |
|---|---|
| `bugs` | correctness defects — logic and boundaries, nil and bounds, concurrency, resource lifetime, error handling |
| `impl` | goal fit — whether the change does what it set out to do, is wired up, and is proportionate |
| `architecture` | conventions and organization — the project's own rules, established patterns, dependency and interface shape |
| `quality` | style, over-engineering, error handling and accidental duplication in code that already works |
| `docs` | documentation accuracy — doc comments against the code, and project docs the change leaves stale |
| `tests` | whether tests exist where a defect can hide, actually exercise the code, and survive concurrency |
| `comments` | the code's own stated rules — doc comments and inline notes the change was supposed to obey |
| `adversarial` | attacks the change looking for what a sympathetic reader would accept |
| `grounding` | whether what a filed item claims is true of the code as it stands today |
| `precedent` | how comparable asks were decided here before, read off the maintainer's own closing words |
| `thesis` | the strongest honest case that a filed item should be done or that its report is real |
| `antithesis` | the strongest case against it, and whether something simpler reaches the same goal |
| `cost` | what implementing it reaches into, and whether the work is proportionate to its value |

The last five read a filed item rather than a diff and are what `triage` composes. They are still
ordinary lenses — `--lenses grounding,cost` works — but under a code-review profile they have no item to
read.

`--lenses bugs,impl` replaces the profile's roster while keeping its body. Two things about it are
easy to get wrong:

- it produces **one** agent carrying every named lens, not one agent per lens. A caller naming two
  lenses is asking for a viewpoint, not for two corroborating votes.
- the synthesized entry inherits the selected profile's own runner, **binary included**, so
  `--profile codex-only --lenses bugs` runs on codex. A profile's per-entry models do not survive the
  override, and losing the second source loses every cross-source confidence boost.

Prefer a profile unless there is a specific reason to narrow. `--lenses docs` on a documentation-only
change is a good use; `--lenses bugs` to "go faster" is what `--profile focused` is for, and that
keeps the codex peer.

Ask `revmux config` for the authoritative list — it reports what resolved, including user overrides,
with each lens's own one-line description.

## Stages, and skipping them

1. **find** — the roster runs in parallel, staggered. Each agent returns structured findings.
2. **synthesize** — one model call. Merges every source, dedupes on `(file, line ±2)`, boosts
   confidence where distinct sources corroborate, splits out open questions and pre-existing issues,
   drops weak singletons.
3. **verify** — parallel agents grouped by directory, each seeing only its own group so it cannot
   anchor on a neighbour. Every finding comes back with a verdict. `--verify-group-by source` keys the
   groups by the agent that raised the finding instead, and skips the merge that folds thin directories
   together — a panel of one-argument agents reaches one verifier otherwise.

`--no-synthesis` passes findings through with attribution intact — raw, duplicated across sources, no
confidence boost. Useful when the question is "what did each source actually say".

`--no-verify` marks every finding `unverified` rather than silently claiming it was checked. Faster,
and appropriate when a human is going to read every finding anyway.

Skipping both makes revmux a parallel agent launcher with an archive. That is a legitimate use, but
the review quality is not comparable.

## Filtering

`--min-confidence=<n>` drops findings below that confidence. It filters **once, before anything
renders**, so the report, the findings browser and the exit code all agree — a finding the exit code
says is absent is never listed anywhere. Open questions, pre-existing and immaterial findings pass
through untouched.

`70` is a reasonable floor for "only show me things worth acting on". `0` (the default) shows
everything that survived verification.

## Where paths resolve from

Two distinct roots, and conflating them is a common failure:

- **the process working directory** governs the project config layer (`./.revmux/`), the project profile
  `./.revmux/profile.md` inside it, and the `./.revmux/tasks` default for `--tasks-dir`
- **`--workdir`** sets where the review subprocesses run and what `{{WORKDIR}}` expands to

Reviewing a repository from outside it therefore means passing `--workdir`, `--tasks-dir` **and**
`--config-dir` — otherwise the first resolves to the repo and the other two to wherever the caller
happens to be standing.

**`./.revmux/profile.md` is a fourth cwd-relative input and no flag relocates it.** Reviewing repo B from
a checkout of repo A, a round with no `input/profile.md` of its own inherits **A's** project profile, so B
is reviewed against A's bar. Write the round its own `input/profile.md` when reviewing from outside a
tree, or check `revmux config`'s `paths.profile_fallback` first — the run itself is not silent about it,
since the inherited bytes are archived as `prompts/input-profile.md` and labelled in the TUI, but that is
after the fact.

## `.revmux/` in a repository is executable trust

Prompt and lens files resolve per file: `./.revmux/`, then `~/.config/revmux/`, then the embedded
defaults. A checked-in `.revmux/lenses/bugs.md` replaces the shipped lens, and **that text becomes the
instructions a headless agent with a shell executes**.

So `.revmux/` is code, and running revmux inside a repository trusts it the way a `Makefile` there is
trusted.

**Before reviewing a branch or repository someone else wrote:** either read `.revmux/` first, or run
revmux from outside the tree. The project layer is read from the process working directory and never
from `--workdir`, so an invocation that stays outside never picks up the reviewed repository's own
`.revmux/`:

```bash
cd ~/reviews                        # outside the repo under review
revmux --task pr-123 \
       --workdir ~/src/untrusted-repo \
       --tasks-dir ~/reviews/.revmux/tasks \
       --config-dir ~/.config/revmux
```

This is worth doing unprompted for any review of code from an untrusted author.

## Config precedence

**Runtime knobs** — command line, then `./.revmux/config`, then `~/.config/revmux/config`, then the
built-in default. Layers merge per key, so a project config that sets one knob leaves the rest alone.
The project layer is auto-detected; no flag selects it and its absence simply drops it.

**Prompt and lens files** — `./.revmux/`, then `~/.config/revmux/`, then `go:embed` defaults, resolved
per file. Overriding one lens does not orphan the others, and deleting an override falls back to the
embedded copy rather than disabling the lens. To actually drop a lens, remove it from the profile
roster.

`--init`, and the identical `revmux init`, materialize `./.revmux/`: the commented-out config template
plus every prompt file as it **resolved**, with the paths printed as JSON on stdout.
`--dump-defaults <dir>` extracts the **embedded** prompt tree at an arbitrary path instead, which is how
a customized file is diffed against the shipped one. Neither overwrites a customized file, and a normal
run writes no config at all.

## `revmux config` — ask rather than guess

```bash
revmux config                       # what a bare invocation resolves to
revmux --profile focused config     # what THAT invocation would resolve to
```

Prints the resolved configuration as JSON and exits `0`. It runs no pipeline and creates nothing; it
reads the tasks root to list what is there, and writes nothing anywhere, so it is always safe to call.

It reports what **resolved**, not what ships: a user who overrode a lens sees his own text's
description. Use it to answer, without guessing:

- which profiles exist and what roster each one runs — `.profiles[]`
- what a lens covers — `.lenses[].description`
- which model and binary a given profile's stages use — `.profiles[].stages`, which is what actually
  runs under that profile. Top-level `.stages[]` is the prompt file's own metadata: a description, and a
  runner only if that file authored one, which the shipped pair do not
- which binary a `--lenses` override would run on — `.profiles[].runner`, the profile's own base runner.
  A profile may name a binary there that no authored agent or stage uses, so a preflight check that
  reads only the roster and the stages can clear a host the run then fails on
- which binaries and efforts a `model:` string may name — `.vocabulary`. A prompt file writes the
  runner as one value, `<binary>[/<model>][:<effort>]`, and the catalog reports the resolved parts
  separately
- which tasks already exist, what each covers and which rounds ran — `.paths.tasks`, whose entries carry
  `id`, `description`, `url`, `branch`, `base` and `rounds`; match on `url` or `branch` before minting
  a new id
- whether a knob came from a flag, the project config, the user config or the default —
  `.knobs[].source`

That last one distinguishes a deliberate setting from a default, which `--help` cannot.

`rounds` lists the rounds that ran to completion, so neither a round prepared but not yet reviewed nor
one whose review never finished is in it — both are still open under their own name, the interrupted
one only while its review left nothing behind.
`scripts/task-state.sh <task-id>` reports those, with each round's `input/` state.
A task whose `task.md` will not parse is still listed, with `meta_error` saying why its anchors are
empty; fix the file rather than minting a second id for the same subject.

An empty list always means empty. If the tasks root could not be read the reason is `.paths.tasks_error`,
if one task's directory could not be read it is `rounds_error` on that entry, and a `--workdir` that would
not resolve is `.paths.workdir_error`. Treat any of them as "unknown", never as "nothing is there".

`.paths.profile_fallback` is `./.revmux/profile.md` when the repo has one: every round with no non-empty
`input/profile.md` of its own inherits it, so do not write one per round. `.paths.profile_fallback_error`
means that file will not resolve — a round that inherits it would refuse to start, while one with a
non-empty `input/profile.md` is unaffected, since the round file wins before the project one is read.
`preflight.sh` does not gate on it: it runs before the round exists and cannot know which will win.

## `revmux new` — the only call that creates anything

```bash
revmux new --task pr-123 --run 01-initial
```

Creates the round and prints, as JSON on stdout, every path to write plus a `created` list naming which
of them this call made. Take the paths from that output; see `task-dir.md`.

It never overwrites: an existing `task.md` is left alone and a round that has already run is refused.
The review path creates nothing, so a typo'd `--task` there is an error rather than an empty task.

## `revmux init` — materialize the local tree

```bash
revmux init
```

Writes `./.revmux/`: the commented-out config template plus every prompt file as it currently
**resolved** — the winning layer's own bytes, front matter included. A user with `~/.config/revmux/`
overrides gets those copied down rather than the embedded text, so editing the result changes what
already runs instead of reverting it to what ships. `--init` is the identical flag form.

It prints JSON on stdout and writes nothing outside `./.revmux/`:

```json
{"dir": "…/.revmux", "config": "…/.revmux/config",
 "files": [{"path": "…/.revmux/lenses/bugs.md", "layer": "user", "created": true},
           {"path": "…/.revmux/prompts/synthesis.md", "layer": "project", "created": false}]}
```

- `layer` is where the content came from: `project`, `user` or `embedded`
- `created` is false for a file already there — it is reported and left byte-identical, so a second
  run changes nothing. A file already local resolved from the project layer by definition, which is why
  a second run reports `project` for everything it wrote the first time
- `created` describes `files[]`. The config is reported as a path alone because it is materialized
  differently: it ships commented out, and one holding no uncommented key is rewritten with the current
  template so an upgrade can still move a default the user never set. One carrying an actual setting is
  left alone. Prompt files ship live, because they are the text agents execute

Take the paths from that output. Twelve prompt files and the config is the shipped tree's size, but a
user with his own lenses has more, and composing a path from this document rather than reading one
back is how a caller ends up writing a file nothing loads.

`--dump-defaults <dir>` is the other direction: it extracts the **embedded** tree at an arbitrary path,
which is how a customized file is diffed against the shipped one.

## `revmux stats` — what past rounds actually produced

```bash
revmux stats                    # every task under the tasks root
revmux stats --task pr-123      # one task
```

Aggregates the rounds revmux already archived and prints the result as JSON on stdout. It runs no
pipeline, spawns nothing and writes nothing, so it is always safe to call. An empty tasks root is a
valid empty document rather than an error; a `--task` naming no task under the root exits `2`.

```json
{"tasks": [{"id": "pr-123", "description": "the auth refactor", "rounds": 5,
            "size_mb": 6.6, "last_run": "2026-07-27", "skipped": [],
            "agents": [{"name": "bugs+impl", "raised": 8, "survived": 8, "corroborated": 5,
                        "degraded_rounds": 0, "retries": 0, "tokens": 10441185}],
            "lenses": [{"name": "bugs", "raised": 14, "ambiguous": 3,
                        "verdicts": {"confirmed": 4, "refined": 6, "unverified": 4}}],
            "stages": [{"name": "synthesis", "in": 62, "out": 46},
                       {"name": "verify", "in": 46, "out": 46},
                       {"name": "report", "in": 46, "out": 46}]}],
 "totals": {"rounds": 5, "size_mb": 6.6, "last_run": "2026-07-27",
            "skipped": [], "agents": [], "lenses": [], "stages": []}}
```

`totals` is every task folded together and carries no `id`. The sample is abbreviated: a real run
lists every agent, every lens and every stage, and `totals` carries those same three arrays rather
than the empty ones above.

**Per agent** — `raised` is what it put on the table in `stages/1-found.json`; `survived` is what was
still there in the round's last stage snapshot, counted across all four of that report's arrays;
`corroborated` is the survived subset another agent independently reached. These are exact: revmux
stamps `sources` from the process that emitted the finding, so no model supplied them.

**Per lens** — `raised` is stage 1 only, since after synthesis a finding's `lenses` is a union across
merged findings from different agents. `ambiguous` is the subset attributable only by the raising
agent's whole lens set, which is what the find stage falls back to when the model named no valid lens.
**A per-lens number is only as good as its `ambiguous` share, so quote the two together.** `verdicts`
counts survivors, so a rejected finding is counted in `raised` and under no verdict. It widens the gap
between the two — but so does synthesis merging two findings that carry the same lens, and nothing here
tells them apart, so read the gap as attrition rather than as a count of rejections. `immaterial` in the
map is the separate "real, not worth fixing" signal.

**Per stage** — `in` and `out` for `synthesis`, `verify` and `report`, each the union of that report's
four finding arrays, plus `reclassified` and `refined` where a stage did either. Those two exist because
`in` and `out` cannot show verification: moving a finding into `immaterial` or `pre_existing` leaves the
total unchanged, so a stage that lowers a great many severities and rejects almost nothing reads as inert.
They are each stage's own contribution, so `report`, which only filters, carries neither. `report` carries the `--min-confidence` attrition. There is no `find` entry:
nothing goes into it. A run that skipped a stage has no entry for it, and one run with both
`--no-synthesis` and `--no-verify` reports `survived` equal to `raised` for every agent — nothing
filtered anything.

`rounds` is the rounds the numbers were read from. A round prepared but never run is not one, and
neither is one an interrupted run left half-written — those are skipped rather than counted at zero.
`skipped` names each round left out because its artifacts would not decode, with the reason: a corpus
that shrank is not a corpus that is simply smaller, and `rounds` is the denominator of everything
beside it.

`degraded_rounds` and `retries` come out zero on a healthy corpus, and zero means supervision never had
to intervene. It is an absence, not a finding, and nothing should be inferred from it.

`size_mb`, `last_run` and `description` are the three a cleanup decision reads rather than a review one:
the disk the task occupies — every round plus the caller's own `input/`, summed from file sizes, so it
reads a little under `du` — the `finished_at` of its newest round, and its `task.md` one-liner. A task
with no `task.md`, or one that will not parse, simply reports no description.

## `revmux cleanup` — remove a task once it is no longer worth keeping

```bash
revmux cleanup --task pr-123
```

Removes that task and everything under it, and prints what went as JSON. It is the only thing in revmux
that deletes anything: a review, `new`, `init`, `config` and `stats` remove nothing, so nothing is ever
removed as a side effect of doing something else.

```json
{"tasks_dir": "/repo/.revmux/tasks",
 "removed": [{"id": "pr-123", "rounds": 5, "size_mb": 6.6}],
 "total_mb_after": 6.4}
```

`total_mb_after` is absent when the tasks root could not be measured after the removal — it is taken once
the tree is gone, so its failure omits the number rather than failing a call that succeeded. `removed` is
always there.

It removes a **whole task**, never a round inside one: a task's rounds are one review's history and are
read together, so a task that lost its early rounds would keep being reported by `revmux stats` as the
whole record.

It refuses more than it removes, and nothing is removed on any refusal. A name that is not one task
directly under the tasks root — a path, a `..`, a round name, a typo — exits `2`. An absent `--task`
names the flag rather than meaning every task. A task a running review holds is refused — but that is a
check taken as it goes, not a lock held across the removal, so a review that claims a round after the
check has passed it loses that round. Don't run cleanup against a task a review is working on.

What to remove is decided from `revmux stats`. There is no age threshold, no size cap and no all-tasks
form: the decision is the user's, one task per call.

## Environment

revmux drives the model CLIs as subprocesses, so both must already be installed and authenticated:

- `claude` — every lens agent and both model stages run on it by default
- `codex` — needed when a profile, a roster entry or a stage names it in its `model:`. `claude-only`
  needs claude alone and `codex-only` needs codex alone; the other six shipped profiles need both.
  `preflight.sh <profile>` answers it for the profile that will actually run

`ANTHROPIC_API_KEY` is stripped from the child environment by default so `claude` uses interactive
subscription auth; `--preserve-anthropic-api-key` passes it through for key-based auth. `CLAUDECODE`
is always stripped — a `claude` child refuses to start when it believes it is a nested session, which
is exactly the situation when an agent invokes revmux.

Agent processes start in their own session, so the terminal never signals them directly; revmux tears
each process group down itself rather than leaving model CLIs running unsupervised after it exits.

## Full flag list

| Flag | Default | Description |
|---|---|---|
| `--task=<id>` | required | name of the task directory holding the review context |
| `--run=<name>` | required | name for this round of the review |
| `--lenses=<a,b>` | | lens set replacing the profile roster |
| `--workdir=<dir>` | working directory | directory the review subprocesses run in |
| `--min-confidence=<n>` | `0` | drop findings below this confidence |
| `--no-synthesis` | | skip the synthesis stage |
| `--no-verify` | | skip the verification stage |
| `--no-tui` | | disable the terminal UI |
| `--markdown` | | write the report as markdown instead of JSON |
| `--preserve-anthropic-api-key` | | pass `ANTHROPIC_API_KEY` to the model CLIs |
| `--config-dir=<dir>` | `~/.config/revmux` | directory holding the config file and the prompt tree |
| `--init` | | materialize the resolved config and prompt tree into `./.revmux/` |
| `--dump-defaults=<dir>` | | extract the embedded prompt tree into a directory |
| `--version` | | show version and exit |

These also read from the config file, under the same name as the flag:

| Flag | Config key | Default | Description |
|---|---|---|---|
| `--idle-timeout=<d>` | `idle-timeout` | `2m` | kill and retry an agent after this long with no output |
| `--hard-timeout=<d>` | `hard-timeout` | `20m` | kill an agent after this long, per attempt |
| `--stagger-delay=<d>` | `stagger-delay` | `30s` | how long to wait for the first agent before releasing the rest |
| `--max-parallel=<n>` | `max-parallel` | `4` | how many agents run at once |
| `--verify-groups=<n>` | `verify-groups` | `6` | cap on the number of verifier groups |
| `--verify-group-by=<k>` | `verify-group-by` | `dir` | key verifier groups by directory or by the agent that raised the finding (`source`) |
| `--tasks-dir=<dir>` | `tasks-dir` | `./.revmux/tasks` | root directory holding task directories |
| `--auto-exit=<d>` | `auto-exit` | `0s` | close the TUI this long after the report arrives; `0` waits for the reader to quit with `q` or `ctrl+c` |
| `--profile=<name>` | `profile` | `comprehensive` | profile naming the roster to run |

`--task` and `--run` are not config keys: a config file naming the round to write would make the same
command review different context in different directories.

## Cleaning up

Rounds accumulate and nothing removes them as a side effect. Reclaiming is `revmux cleanup`, above:

```bash
revmux cleanup --task <id>
```

Whole tasks rather than rounds, and refused while a running review holds one — safe at any other time.
Nothing links tasks together, and the prior-round inventory is rebuilt from whichever round directories
are present.
