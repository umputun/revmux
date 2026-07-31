---
name: revmux
description: Run a supervised multi-agent code review by composing a task directory and driving the revmux CLI, then report or act on the findings it returns. revmux spawns and watches parallel claude and codex subprocesses with stall detection, retry, per-agent progress and a full run archive; this skill is the caller that writes the review context, launches it, reads the JSON back, and re-runs it after fixes. It also has a self mode that reads what past rounds produced and proposes tuning changes to the local profiles, lenses and knobs, one suggestion at a time with the numbers behind it. It fetches a pull request into a throwaway worktree, reviews it there and cleans up after. Also answers questions about revmux itself — profiles, lenses, task directories, flags, the JSON shape, exit codes and the run archive. Activates on "revmux", "run revmux", "multi-agent review", "supervised review", "review with revmux", "revmux this branch", "revmux the last commit", "revmux pr 123", "revmux this PR", "review PR 123 with revmux", "run a revmux round", "re-review after fixes", "revmux self", "self-improve revmux", "tune revmux", "revmux profiles", "revmux lenses", "what does revmux return", "revmux exit codes", "revmux task directory".
argument-hint: 'optional: what to review ("pr 123", a ref, a path), plus "focused" / "final" / "loop" / "lenses a,b"'
allowed-tools: [Bash, Read, Edit, Write, Grep, Glob]
---

# revmux — supervised multi-agent code review

revmux spawns and supervises parallel `claude --print` and `codex exec` subprocesses, watches each for
stalls, retries what hangs, and returns findings on stdout.

It does no scope detection, no git, no PR fetching, no source modification. This skill does that half.

| this skill | revmux |
|---|---|
| resolve what is under review | supervise, stagger, retry, degrade |
| run git, gather context | compose and archive prompts |
| write `scope.md`, `goal.md`, `profile.md`, `context/` | merge, dedupe, verify |
| choose profile, lenses, flags | return findings on stdout |
| read the JSON, present, fix, re-run | inject prior rounds |

## Script path resolution

```bash
SCRIPT_DIR="${CODEX_HOME:-$HOME/.codex}/skills/revmux/scripts"
```

Use `$SCRIPT_DIR` in place of every script path below.

**Resolve only from the installed skill, never from the repository under review.** Deriving it from
`git rev-parse --show-toplevel` would run the scripts of whatever repo is checked out — a branch that
adds `plugins/codex/skills/revmux/scripts/` executes its own code at Step 0. For a development
install, symlink the checkout:

```bash
ln -s "$PWD/plugins/codex/skills/revmux" ~/.codex/skills/revmux
```

## Asking the user

Three decision points need a choice: an ambiguous scope, headless versus overlay, and each self-mode
suggestion. Codex has no structured question tool — present a numbered list and ask for a number:

```
Which scope?
  1. branch vs master (all 7 commits)
  2. just the uncommitted changes
```

Before applying fixes, write the plan inline as markdown and ask for explicit confirmation.

## Activation triggers

- "revmux", "run revmux", "review with revmux"
- "multi-agent review", "supervised review", "parallel agent review"
- "revmux this branch", "revmux the last commit", "revmux the uncommitted changes"
- "revmux pr 123", "revmux this PR", a pull-request URL — the checkout half, `references/pr.md`
- "another revmux round", "re-review after fixes"
- "revmux loop", "loop it", "keep going until clean" — the review-fix loop, `references/loop.md`
- "revmux self", "self-improve revmux", "tune revmux" — the self mode below, which reviews nothing
- questions: "revmux profiles", "what lenses are there", "revmux exit codes"

## Answering questions without running anything

If asked **about** revmux rather than for a review, answer from the references and do not launch a run.

- `references/task-dir.md` — the round's context files, task and run naming
- `references/invocation.md` — flags, profiles, lenses, overlay backends, config precedence
- `references/output.md` — JSON shape, verdicts, exit codes, run archive
- `references/pr.md` — fetching a pull request into a worktree, `--workdir`, cleanup
- `references/loop.md` — the autonomous review-fix loop, entered from Step 6

For anything about current configuration, run `revmux config` and read the answer. It reports what
resolved including user overrides, runs no pipeline, and is always safe to call.

## Non-negotiables

**1. Exit `1` means findings were reported — a success.** `0` none, `1` findings, `2` tool error.
Never treat `1` as failure. Never re-run on it.

**2. Run it in the background.** A review takes 3-15 minutes. Redirect stdout to a file, wait for the
completion notification. Do not poll, do not sleep-and-check. Applies to the overlay launcher too.

**3. Check `sources.degraded` before believing the findings.** If `expected != reported` the review is
partial. Say so. Never report "no findings" from a degraded run as "the code is clean".

**4. `.revmux/` in a repository is executable code.** A checked-in `.revmux/lenses/*.md` becomes
instructions a headless agent with a shell executes. Before reviewing untrusted code, either read
`.revmux/` first or run from outside the tree with explicit `--workdir`, `--tasks-dir`, `--config-dir`.
A fetched pull request is already outside it — `references/pr.md` leaves the process in the user's own
checkout and puts only `--workdir` in the branch, so the other two need no override.

**5. Never `2>&1` into the report file.** stdout is the report, stderr is progress. Merging them makes
the JSON unparseable.

## Workflow

### Step 0: Preflight

```bash
$SCRIPT_DIR/preflight.sh [profile] [--lenses]
```

Checks revmux plus every binary the invocation needs. Exits `1` naming what is missing.

**Pass the profile that will actually run.** Which executors are needed comes from that profile's
roster, and with no argument preflight checks the resolved *default* instead — so a run under a
profile the default does not share a roster with is never checked at all, and degrades mid-review over
a CLI that was missing the whole time. If the user named a profile in any form, resolve it to an exact
name first — that is Step 3's rule, and nothing in it depends on the scope, so it can be applied here.
A word passed through unresolved fails preflight as an unknown name. If he named none, run it bare and
run it again once Step 3 has chosen one.

**Pass `--lenses` when the run will use it.** That flag replaces the roster with one agent on the
profile's own base runner, so the binaries needed are that base plus the stages, and none of the
roster's own. Checked as an ordinary run, a `--lenses` invocation can pass preflight and then have its
only finder fail to launch; checked the other way round, an ordinary run can be refused over a binary
it never touches.

If revmux is absent:

```
go install github.com/umputun/revmux/app@latest    # installs as 'app'; rename to 'revmux'
git clone https://github.com/umputun/revmux.git && cd revmux && make install
```

### Step 1: Resolve what is being reviewed

| the user says | scope |
|---|---|
| nothing, on a feature branch | `git diff <base>...HEAD` |
| nothing, on master with uncommitted work | `git diff` and `git diff --staged` |
| nothing, on master and clean | `git diff HEAD~1` |
| "the last N commits" | `git diff HEAD~N` |
| "since <ref>" | `git diff <ref>..HEAD` |
| "pr 123", "this PR", a PR URL | `references/pr.md` — resolve, fetch into a worktree, review it there |
| a path | that subtree, as a diff plus a read list |

Run the git commands here to learn scale and file list.

**A pull request is a different shape, not a harder ref range.** revmux fetches nothing and checks
nothing out, so a PR has to be on disk before there is anything to review, and the checkout has to be
removed afterwards. Read `references/pr.md` and follow it — it covers steps 1 through 4 for that case
and hands back here at Step 5.

Ask only when genuinely ambiguous — a feature branch with uncommitted work is the standard case.

### Step 2: Open a round and write its context

Read `references/task-dir.md` first.

**Match an existing task before minting an id.** A second id for one subject runs as a first round with
no history.

```bash
revmux config | jq '.paths.tasks'
```

Entries carry `id`, `description`, `url`, `branch`, `base` and `rounds`. Match on `url` or `branch`
exactly; failing that, on `description` against the subject in hand. Reuse the matched `id` verbatim.

Derive an id only when nothing matches:

| reviewing | task id |
|---|---|
| a pull request | `pr-<number>` |
| an issue | `issue-<number>` |
| a branch | branch name with `/` replaced by `-` |
| a commit range | `since-<short-sha>` |
| working-tree changes | `wip-<branch>` |

No path separators, no `..`, no leading dot, not absolute — revmux rejects those at load.

```bash
$SCRIPT_DIR/task-state.sh <task-id>
```

Validates the id and reports the task's `task.md` anchors, every round, and each round's `input/`
state. Each round is `prepared` (never reviewed), `claimed` (a review started and never finished) or
`ran`. `revmux config` lists only the `ran` ones; a `prepared` round is open under its own name, and a
`claimed` one only while that review left nothing in it — revmux refuses the name and says what it
found if it did not.

**Name the round `NN-label`:** `01-initial`, `02-after-fix`, `03-final`. `NN` is one past the highest
round already there. Do not mix vocabularies across rounds of one task.

```bash
revmux new --task <id> --run <NN-label>
```

It prints the absolute path of every file to write, plus which of them it created:

```json
{"task_dir": "…", "task_file": "…/task.md", "round_dir": "…/01-initial",
 "input_dir": "…/01-initial/input", "scope": "…/input/scope.md", "goal": "…/input/goal.md",
 "profile": "…/input/profile.md", "context": "…/input/context",
 "created": ["task_dir", "task_file", "round_dir", "input_dir"]}
```

**Write to those paths and no others.** Never join a path and never create a directory the output did
not name.

- **`scope`** — required. What changed, the commands to see it, its scale, which files to read in
  full, what to ignore. Write commands in plainest form: `git diff master...HEAD`, never
  `git -c core.pager=cat diff ...` — a leading option defeats the child's permission prefix matching.
- **`goal`** — optional. What the change is for, plus a "this is correct only if…" list.
- **`profile`** — optional, reusable across the repo. What the software is, what a real failure
  looks like, where the project's rules live, which conventions are deliberate. Copy it into each
  round of the task.
- **`context`** — optional. Ticket text, design notes, commit list. The path is reported but not
  created.
- **`task_file`** — when `created` lists it, write the task's `task.md`: `description`, plus `url`,
  `branch` and `base` when known. That front matter is what the next session matches on.

Each round holds its own context, so a re-review writes a fresh `scope` in its own round rather than
editing an earlier one's. When `task-state.sh` reports the round as `scope=present`, read it before
writing over it.

### Step 3: Choose profile and flags

| profile | roster | when |
|---|---|---|
| `comprehensive` (default) | `bugs+impl`, `arch+quality`, `docs+tests`, codex peer | real change, real risk |
| `focused` | one `bugs` agent plus codex peer | small or time-boxed |
| `final` | `bugs+impl` plus codex peer, nothing below major | pre-merge |
| `claude-only` | the same four lens splits, all on claude | no codex available |
| `codex-only` | the same four lens splits on codex, and synthesis and verify with them | no claude available |

**A profile word is not a profile name.** Map whatever the user said onto the profiles `revmux config`
reports, matching the name first and the `description` second. revmux rejects an unknown `--profile` at
load and never guesses, so resolving is this skill's job — passing his word through unresolved just
fails the run.

| the user says | profile |
|---|---|
| full, everything, deep, thorough, the works | `comprehensive` |
| short, quick, fast, light, small, time-boxed | `focused` |
| last, pre-merge, before merge, strict | `final` |
| claude only, no codex, skip codex | `claude-only` |
| codex only, no claude, codex alone | `codex-only` |

Examples, not the list. Match on intent — breadth wants `comprehensive`, speed wants `focused`, a merge
gate wants `final` — and read the resolved catalog rather than this table, since a user with his own
profiles has names it does not carry. Name the profile picked and the word it came from. Ask only when
two are genuinely close, as "quick, before I merge" is between `focused` and `final`.

`--lenses a,b` produces **one** agent carrying both lenses and drops the codex peer, losing every
cross-source confidence boost. Prefer a profile unless narrowing is specifically wanted.

Also useful: `--min-confidence=70` for actionable-only, `--no-verify` when a human reads everything.

### Step 4: Run it — headless or overlay

Both return the same report on stdout and the same exit code.

**Headless — the default:**

```bash
revmux --task <id> --run <name> --no-tui > /tmp/revmux-<id>-<run>.json 2> /tmp/revmux-<id>-<run>.log
```

Launch in the background, wait for the notification.

Reviewing a fetched pull request adds `--workdir <worktree>` and changes nothing else — including that
the command is still run from the main checkout, which is what keeps the archive out of the directory
the cleanup deletes. `references/pr.md` has the reasoning.

**Before yielding, tell the user three things** — otherwise they sit for 10+ minutes with no signal:

1. what is running (task, profile, roster size) and the rough duration
2. the stderr log path, and that `tail -f <path>` shows live per-agent progress
3. that they can ask for status any time

On a status request, read the tail of the stderr log and `events.jsonl` in the round directory. Report
the stage, which agents are active, and elapsed. Never guess.

**Overlay — when the user wants to watch:**

```bash
$SCRIPT_DIR/launch-revmux.sh --task <id> --run <name> [any revmux flag]
```

Detects the terminal (agterm, tmux, zellij, herdr, kitty, wezterm/kaku, cmux, ghostty, iTerm2, Emacs
vterm), runs revmux with its TUI in an overlay, returns the report on stdout. Under agterm: floating
panel at 80% of the pane. Do not pass `--no-tui`; the script rejects it.

- it blocks for the whole review — background it exactly like the headless form
- **its exit codes: `0`/`1`/`2` are revmux's, `3` is a launcher failure, `127` is revmux not
  installed.** A `3` means no review happened — that is the one to retry.
- overrides: `REVMUX_AGTERM_PERCENT` (80), `REVMUX_POPUP_WIDTH`/`HEIGHT` (90%), `REVMUX_AUTO_EXIT`
  (30s; `0` waits for the reader to quit), `REVMUX_TMUX_WINDOW=1` for a disconnect-resilient tmux window

**Choose headless** unless the user asked to watch or is clearly at the terminal.

**If the launcher dies after a run completed, the report is not lost** — it is in the round directory
as `findings.json` and `report.md`. Read from there rather than re-running. A killed run wrote no
report, and most `2`s wrote none either — but the report reaches stdout only after the round is
archived, so a `2` whose stderr names a failure writing it leaves a complete round. Check the round's
`manifest.json` for content before re-running.

`--run` is required. A round that has already run is an error, not an overwrite — but a round whose
review never finished is not one of those, so a retry after exit `2` reuses the same name and the same
`input/`. That holds only while the dead review wrote nothing into the round, which is narrower than it
sounds: the pipeline opens `events.jsonl` as its first act, so anything interrupted after it started is
refused under its own name, and the answer is a new round with the `input/` copied across. It may not
be named `task.md`.

### Step 5: Read the result

Read `references/output.md` for the full shape.

1. **Exit code.** `2` means nothing usable — read the stderr log, which names the cause. If that cause
   is a failure writing the report to stdout, the round is complete and its `findings.json` is on disk;
   read it. Otherwise fix the cause and re-run: the same `--run` while that round holds nothing but its
   `input/`, a new round with the `input/` copied across once revmux refuses the name. `0` and `1` both
   completed.
2. **`sources`.** Non-empty `degraded` means partial; lead with that.
3. **`findings`.** Group by severity.
4. **`open_questions`**, **`pre_existing`** and **`immaterial`** — report each separately.

`sources` holds **agent names** and is the only evidence of independent corroboration. `lenses` holds
lens names and is informational. One agent flagging under two lenses is still one source.

### Step 6: Present, then propose

Lead with completeness, then severity counts, then each finding with location, argument and fix. Call
out findings with more than one entry in `sources`.

**Tag every finding with its surface and severity in one bracket before the title:** `[code, minor]`.
The first value is the surface: `code` for executable logic, `comments` for a comment or doc comment
inside a source file, `docs` for a project document, `tests` for test code, or another lowercase word
when none fits (`config`, `build`). revmux returns no surface field, so derive it from `file` and what
the body argues: a `.go` path whose body is about stale godoc is `comments`, not `code`. The second
value is the finding's exact `severity` field. Never omit either value or split them into separate
tags. Format the heading `[surface, severity] Title — file:line, conf N`.

Carry the same combined tags into the counts. "3 findings" says nothing about where the risk is;
"2 [code, major], 1 [docs, minor]" does.

Do not compress a finding to its title — the body carries the trigger and consequence.

**Then close with what is actionable and what to do about it.** A list of findings is not a decision,
and the reader should not have to re-derive one from it.

Actionable is `findings` alone — what revmux kept. Report the other three, and keep them out of the
count: `immaterial` is real and judged not worth the fix, `pre_existing` is real and not this change's,
`open_questions` wants a decision rather than an edit.

Recommend the option that fits the outcome:

| the run came back | recommend |
|---|---|
| `sources.degraded` non-empty | re-run the missing source first — a partial review's silence is not evidence, so nothing else is worth deciding yet |
| any `critical` or `major` | fix those, then a new round on the same task — or the loop below, if the user wants it run to the end |
| `minor` only, verdicts not `unverified` | fix now, or note for later — verification weighed each fix's cost against its consequence, so what is left is a question of timing |
| `minor` only, verdicts `unverified` | say nothing was checked, and weigh each one before acting — `--no-verify` drops nothing and produces no `immaterial` list |
| `open_questions` non-empty, no findings | answer them — they are decisions, and the review found nothing to edit |
| nothing actionable | done, and `--profile final` if a merge is next |

Rows are tried in order and the first match wins, so a degraded run outranks whatever it did report.

Put it as a numbered list, the recommendation first and marked `(recommended)`, and wait for the pick.

**Never start fixing because the findings look clear.** A review produces findings; editing is a
separate decision, and this is where the user makes it.

**Offer the loop as a third way out of that question**, alongside stopping and fixing-then-one-more-round:
review, fix, commit, re-review, until a round comes back with nothing gating. It is for the user's own
branch, never a fetched PR, and it commits without pushing. `references/loop.md` is the whole procedure —
read it when he picks it, not before.

### Step 7: Fix and re-run, if asked

1. Agree which findings to act on. An `immaterial` verdict means revmux already dismissed it — it is in
   its own list, not in `findings`. A `rejected` one appears nowhere at all: the verifier judged it not
   real and dropped it, so only the archive's stage snapshots still hold it.
2. Make the fixes.
3. Open the next round on the same task and write its own `scope` — the fixes and the range they land
   in — then run it:

```bash
revmux new --task <id> --run 02-after-fix
revmux --task <id> --run 02-after-fix --profile <picked> --no-tui \
    > /tmp/revmux-<id>-02-after-fix.json 2> /tmp/revmux-<id>-02-after-fix.log
```

revmux injects the prior rounds itself. **Do not paste prior findings into the scope** — it
duplicates the injection and anchors agents on conclusions they should re-derive.

A pull request re-reviewed after the author pushed is fetched again first, and carries `--workdir`
again: `references/pr.md`, step 6.

**Pick this round's profile from what the fixes touched, not from the round number.** A re-review is
not automatically smaller: round 1's findings may have been fixed by a redesign, and a narrower roster
cannot catch a regression in an area its lenses do not cover.

| the fixes were | profile |
|---|---|
| contained edits inside what round 1 flagged | `final` — `bugs+impl` is the fix-confirmation shape, and it reports nothing below major |
| spilled into tests or structure | `comprehensive` — that surface has not been reviewed yet |
| documentation only | `final` — a doc fix is new prose, and `comprehensive` would review it |
| time-boxed, correctness only | `focused` |

The round-2 failure worth spending a roster on is a fix that does not address the finding, or addresses
it in the wrong place. That is the `impl` lens, which `focused` does not carry — so a re-review narrows
to `final` rather than to `focused`.

Documentation is the one surface a re-review does **not** widen to cover. Every other spill is code the
round before never looked at; a doc fix is a sentence this loop just wrote, and running the `docs` lens
over it produces a finding about that sentence round after round while the code stands still.

Put it as a numbered list, the matching row first and marked `(recommended)`, and wait for the pick.
Never narrow the roster silently: a user who does not notice gets a smaller review than the one he
thinks he asked for. Skip the question when he named a profile himself.

## Debugging a review that looks wrong

The archive is the round directory `revmux new` reported, beside the `input/` it was run against:

| question | where |
|---|---|
| why did this agent report nothing? | `agents/<name>.jsonl` |
| did an agent stall or get retried? | `events.jsonl`; a `<name>.retry.jsonl` means it did |
| did synthesis drop something? | `stages/1-found.json` vs `2-synthesized.json` |
| did verify reject wrongly? | `stages/2-synthesized.json` vs `3-verified.json` |
| what was this agent asked? | `prompts/agents/<name>.md` |
| which lens text, from which layer? | `manifest.json` |

## Self mode — tune the configuration from the record

Triggered by "revmux self", "self-improve revmux", "tune revmux". **It reviews nothing**: no round is
opened, no agent is spawned, and the code in the working directory is never read. It reads what past
rounds produced and proposes changes to the review configuration itself.

### Step S1: Read the two sources

```bash
revmux stats  > /tmp/revmux-stats.json     # the evidence: what each agent and lens actually produced
revmux config > /tmp/revmux-config.json    # what resolved, so a proposal edits the file in force
```

Both are read-only, run no pipeline and return immediately. `references/invocation.md` has each shape.

**Propose against `revmux config`, never against the shipped defaults.** A user with his own
`comprehensive.md` runs a roster this skill has never seen, and a suggestion phrased against the
embedded one edits a profile that is not the one running.

`revmux stats --task <id>` narrows to a single task. Use the whole corpus unless the user named one —
narrowing a thin sample makes it thinner.

### Step S2: Decide where a change would be written

**`./.revmux/` and nowhere else.** Never `~/.config/revmux/`, which is the user's cross-project layer
and not this project's to edit. Never the embedded tree, which is inside the binary.

Materialize the local tree first, every time — it is idempotent, and it is what prints the paths:

```bash
revmux init
```

It writes `./.revmux/` with every prompt file as it currently **resolves**, so a user-layer override is
what gets copied down and editing the result changes what already runs rather than reverting it to the
shipped text. A file already local is reported and left byte-identical, so running it over a tree that is
already there changes nothing. **Edit only the paths it printed** — never compose one.

When `.revmux/` is tracked in git, which is the usual arrangement since only `.revmux/tasks/` needs
ignoring, `git checkout` reverts an edit the user regrets. Say so if he hesitates over one.

### Step S3: Read the numbers honestly

**A counter that is zero across the whole corpus is nothing to say, not a finding.** `degraded_rounds`
and `retries` at zero everywhere means supervision never had to intervene — the timeouts are working,
not unvalidated. Do not manufacture advice from an empty set: on a healthy corpus those two are
uniformly zero and the knob candidate simply does not fire.

**Per-agent numbers are structurally sound; per-lens numbers are not.** revmux stamps `sources` from
the process that emitted the finding, so `raised`, `survived` and `corroborated` per agent are exact.
A finding's `lenses` is model-supplied and falls back to the agent's whole lens set when the model
named none — which is what `ambiguous` counts. **Quote `ambiguous` beside every per-lens number, in
the suggestion itself**: "bugs raised 14, 3 of them ambiguous". A lens whose `ambiguous` share is a
large fraction of its `raised` cannot carry a suggestion on its own; report the number and propose
nothing.

**`raised` is stage 1, the verdict map is survivors.** A rejected finding is counted in `raised` and
under no verdict, so it widens the gap between the two — but so does synthesis merging two findings that
carry the same lens, and nothing in the output tells them apart. Check the `synthesis` stage's own `in`
and `out` before reading a per-lens gap as rejections: a corpus that loses a quarter of its findings at
synthesis explains most of those gaps by itself. `immaterial` in that map is the separate signal — real,
and judged not worth fixing.

**`rounds` is the denominator and `skipped` is what is missing from it.** A task reporting rounds beside a
non-empty `skipped` was read from fewer rounds than it ran; quote both, and treat the sample as that much
thinner.

**Say the sample size.** Five rounds of one task on one codebase is a sample, not a trend, and the
user weighs it.

### Step S4: Build the candidates

| candidate | evidence | the change |
|---|---|---|
| drop or keep an agent | `raised` high, `survived` near zero, `corroborated` zero — it produces nothing that lasts | remove the entry from the `agents:` list in `.revmux/prompts/profiles/<name>.md`, or keep it and say why |
| split a lens pair | one agent carrying two lenses, both raising, its `corroborated` near zero | one agent per lens in that roster — two processes can corroborate, one cannot |
| create a profile | the roster or `--lenses` set actually used matches nothing in `.profiles[]` | a new `.revmux/prompts/profiles/<name>.md` with that roster and its own `description:` |
| retune a knob | `degraded_rounds` non-zero, or `retries` non-zero and the retry's own log says it stalled | `idle-timeout` or `hard-timeout` in `.revmux/config`, with the counter as the reason |
| rewrite a lens | `immaterial` dominating that lens's verdict map | an edit to `.revmux/lenses/<name>.md`, shown as a diff before it is applied |

**`retries` names a retry, not a timeout.** The find stage retries an agent on a stall, a rate limit, a
dead process, a transport error, **or a clean exit carrying no JSON** — the codex path's own failure mode,
since it has no `--json-schema` to enforce one. Only the first two are what a timeout knob moves. Read
`<round>/agents/<name>.retry.jsonl` for the reason before proposing one, and propose nothing when the
reason is not a stall.

**A candidate with no number behind it is not a candidate.** If nothing fires, say the corpus supports
no change and stop. That is a real answer, and inventing one from an empty set is worse than silence.

### Step S5: Present one at a time

**One suggestion, not a list and not a ranked table of five.** Each one is three things:

1. **what to change** — the file, at the path `revmux init` printed, and the edit in concrete terms
2. **the evidence** — the actual numbers from `revmux stats`, `ambiguous` beside any per-lens one
3. **why it follows** — the step from the number to the change, stated so the user can reject the step
   rather than only the conclusion

Put the choice as a numbered list — 1. apply it, 2. skip it, 3. stop here — and wait for the number.

Then act on the answer and move to the next candidate. Stop when the user says stop or the candidates
run out. Never batch the edits, and never apply one that was not asked about.

## Example sessions

```
User: "revmux this branch"
→ preflight.sh → all present
→ git: on tui-rework, 7 commits vs master, 22 files, +840/-310
→ revmux config → .paths.tasks has no url or branch match; derive `tui-rework`
→ task-state.sh tui-rework → exists: false
→ revmux new --task tui-rework --run 01-initial → paths, created all four
→ write task.md, scope, goal, profile at the reported paths
→ revmux --task tui-rework --run 01-initial --no-tui > /tmp/…json  (background)
→ tell user: ~9 min, tail -f /tmp/…log for live progress
→ exit 1, sources 4/4, degraded []
→ 6 findings: 1 major, 5 minor; 2 corroborated across bugs+impl and codex
```

```
User: "fix the major one and run it again"
→ fix applied
→ revmux config → branch matches task `tui-rework`, rounds ["01-initial"]
→ revmux new --task tui-rework --run 02-after-fix → write its own scope
→ revmux --task tui-rework --run 02-after-fix --no-tui
→ exit 0, nothing above threshold
```

```
User: "revmux the branch, I want to watch it"
→ revmux new --task tui-rework --run 01-initial → write its input
→ $SCRIPT_DIR/launch-revmux.sh --task tui-rework --run 01-initial > /tmp/…json  (background)
→ agterm: floating overlay at 80%, TUI live, self-closes 30s after the report
→ same JSON, same exit code, Step 5 onward identical
```

```
User: "revmux pr 123"
→ preflight.sh → all present
→ gh repo view → umputun/revmux; gh pr view 123 → head `feature/oauth`, base master, +410/-95, 12 files
→ revmux config → .paths.tasks has no entry with that url; derive `pr-123`
→ git fetch origin pull/123/head:revmux-pr-123; git worktree add /tmp/revmux-pr-123 revmux-pr-123
→ merge-base origin/master revmux-pr-123 → 4ed3259
→ revmux new --task pr-123 --run 01-initial → write task.md (url, branch, base), scope, goal, context
→ revmux --task pr-123 --run 01-initial --workdir /tmp/revmux-pr-123 --no-tui > /tmp/…json  (background,
  from the repo root — the archive belongs to it, not to the worktree)
→ exit 1, 4 findings; report them
→ git worktree remove /tmp/revmux-pr-123 --force; git branch -D revmux-pr-123
```

```
User: "what lenses does revmux have?"
→ no run; `revmux config`, report .lenses[] with descriptions
```

```
User: "revmux self"
→ revmux stats → 1 task, 5 rounds; revmux config → comprehensive, roster of 4
→ degraded_rounds 0 and retries 0 on every agent → no knob candidate; say so, invent nothing
→ candidate: quality raised 4, 2 of them ambiguous, verdicts 1 confirmed / 1 refined / 1 immaterial
   → half the attribution is a fallback; report the numbers, propose no rewrite
→ candidate: arch+quality raised 10, survived 9, corroborated 4 across 5 rounds → it earns its slot
→ revmux init first (idempotent), then edit only the paths it printed
→ one numbered list per candidate, apply / skip / stop
```
