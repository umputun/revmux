# The review context — what to write

All review context reaches revmux as files the caller wrote. revmux does no scope detection.

## Where the files go

`revmux new --task <id> --run <name>` prints the absolute path of every file to write:

```console
$ revmux new --task pr-123 --run 01-initial
{
  "task_dir": "/abs/.revmux/tasks/pr-123",
  "task_file": "/abs/.revmux/tasks/pr-123/task.md",
  "round_dir": "/abs/.revmux/tasks/pr-123/01-initial",
  "input_dir": "/abs/.revmux/tasks/pr-123/01-initial/input",
  "scope": "/abs/.revmux/tasks/pr-123/01-initial/input/scope.md",
  "goal": "/abs/.revmux/tasks/pr-123/01-initial/input/goal.md",
  "profile": "/abs/.revmux/tasks/pr-123/01-initial/input/profile.md",
  "context": "/abs/.revmux/tasks/pr-123/01-initial/input/context",
  "created": ["task_dir", "task_file", "round_dir", "input_dir"]
}
```

**Write to those paths and no others.** Never join a path and never create a directory the output did
not name. The layout is revmux's own detail.

`created` names what this call made, so a second round on an existing task reports `round_dir` and
`input_dir` alone. `context` is reported but never created — create it only when filling it.

**Each round carries its own scope, goal and context.** A non-empty round profile is an optional override
of the project profile. Round 2 reviews the fixes for what round 1 found, so it gets its own `scope.md`
at its own `input/` path; round 1's is left as the record of what round 1 reviewed.

## Variables expand to paths, never contents

`{{SCOPE}}` is the absolute path of `scope.md`. The agent reads the file itself; revmux only stats it.

- a large `scope.md` costs nothing at launch, and that is the only place it costs nothing: every agent
  reads it in full, and whoever composed the round paid for the prose in wall-clock before the review
  started. Keep it under ~1500 characters, bullets and command blocks rather than paragraphs
- prefer text in `context/`; an agent still has to read it
- an absent optional file expands to the literal `none provided`, not a broken path

## task.md — how the next session finds this task

At `task_file`, about the task rather than any one round. `revmux new` writes it commented out; fill it
when the task is new:

```yaml
---
description: OAuth token exchange rework
url: https://github.com/umputun/revmux/pull/123
branch: feature/oauth
base: 4ed3259
---

Reviewing the token exchange path after the provider swap.
```

Every key is optional. Set the ones that identify the subject: `url` and `branch` are what a later
session matches on exactly.

Leave an existing `task.md` alone. revmux stores these strings and resolves none of them — no git runs
against `branch` or `base`, nothing is fetched from `url`.

## scope.md — required

What is being reviewed, and how to get at it. Not intent — that is `goal.md`.

1. **What changed** — branch, commits, or "uncommitted changes"
2. **The commands to see it** — revmux runs no git; agents run these themselves
3. **Scale** — so the reviewer can trade breadth against depth
4. **Which files to read in full**, and why each matters
5. **Explicit exclusions** — vendored code, generated files

````markdown
# Scope

Review the two most recent commits on the `tui-rework` branch:

```
ce8a9e9 feat(ui): break the header's findings count down by severity
995f637 feat: put JSON on stdout by default, markdown behind --markdown
```

Fifteen files, +115/-36. Small, so review it thoroughly rather than broadly.

Read the change:

```
git diff c06c558..HEAD
git log --format='%H%n%s%n%n%b' c06c558..HEAD
```

Then read the files it touches in full:

```
app/ui/model.go          the tally type and Model.count
app/ui/status.go         the header
app/main.go              runOpts.write, which picks the stdout renderer
CLAUDE.md                the stdout rule this rewrote
```

Ignore `vendor/` entirely.
````

### Write the plainest form of every command

The agent subprocess runs under a permission layer matching command **prefixes**. A leading option
changes the prefix and can turn an allowed command into a denied one.

- write `git diff master...HEAD`
- not `git -c core.pager=cat diff master...HEAD`

When an agent reports a denied command and falls back to reading files directly, check this first —
the review completes, but blind to the diff.

Put any pipe at the end (`git log ... | cat`) where it does not disturb the prefix.

## goal.md — optional, high value

What the change is meant to achieve, and what would make it wrong. Without it agents review for
internal consistency only; with it they can review for fitness. The `impl` lens and verify both use it.

1. **Intent** — what the change accomplishes, in the author's terms
2. **Success criteria** — a "this is correct only if…" list. The most useful lines in the file:
   they give a verifier something falsifiable.
3. **The severity bar for this change** — what is serious here versus noise

Under ~1000 characters, and the criteria are what the budget is for: an agent that has read the diff
learns nothing from a retelling of it, and every falsifiable line is one it can check.

```markdown
# Goal

<two or three sentences on what the change is for and why it was made this way>

So this change is correct only if:

- nothing that consumed the old output shape is silently broken, and its replacement is discoverable
- a degraded run is as obvious to a machine consumer as it was to a human reader
- the tally's parts can never contradict its total

Weigh findings by whether they would mislead a caller or a watcher. Do not inflate style preferences
into defects.
```

With no real goal — a mechanical cleanup, a dependency bump — omit the file rather than writing a
placeholder.

## profile.md — optional, a per-round override of the project's own

What kind of software this is and what counts as a real failure. Without it the implicit bar is
"production service with real traffic", wrong for a personal tool or a UI surface in both directions.

- **What it is** — "standalone Go CLI, personal tooling, one maintainer"
- **What a real failure looks like** — concrete. "A review that hangs with no output" is useful;
  "bugs" is not.
- **Blast radius** — who is affected when this area breaks
- **The reporting bar** — findings must be material, not merely true. Say what is noise here.
- **Where the project's rules live** — `CLAUDE.md`, `AGENTS.md`, `CONTRIBUTING.md`, `docs/`. A
  deviation from a documented rule is always worth reporting; an agent that never finds the rules
  cannot report one.
- **Conventions that are deliberate** — so an agent does not file them as defects
- **Which languages the change actually touches** — a Go repo's conventions are not the bar for a
  commit of shell and markdown

Do not write this file as a matter of course. What it describes — what the software is, what a real
failure looks like, where the rules live — belongs to the repository rather than to one round, so it
lives in `./.revmux/profile.md` and every round without a non-empty override inherits it with nothing
copied forward.
`revmux config` reports the resolved path as `paths.profile_fallback`.

Write a round's own `input/profile.md` only when this subject genuinely needs a different bar than the
repo's — a vendored or generated tree, a prototype, a subsystem the project profile does not describe.
A non-empty round file wins outright, and the project file is then not read at all.

A round that inherits gets a copy of the bytes at `prompts/input-profile.md` inside the round, and
`{{PROFILE}}` names that copy. The archive therefore records what actually calibrated the round even
after the project file changes.

## context/ — optional directory

Ticket text, design notes, spec excerpts, a commit list. `{{CONTEXT}}` expands to the directory path.

The escape hatch that keeps the variable vocabulary closed: there is no `--context-file` flag and no
way to add a `{{VAR}}`.

Keep it curated — every file is a tool call an agent may spend.

## Task and run names

Both become path components: no separators, no `..`, not absolute, no leading dot.

### Match a task before minting an id

```bash
revmux config | jq '.paths.tasks'
```

Each entry carries `id`, `description`, `url`, `branch`, `base` and the `rounds` already reviewed under
it. Match on `url` or `branch` exactly; failing that, on `description` against the subject in hand.
Reuse the matched `id` verbatim — a second id for one subject forks the history and the next round runs
with none.

Name a new task only when nothing matches:

| reviewing | task id |
|---|---|
| a pull request | `pr-<number>` |
| an issue | `issue-<number>` |
| a branch | branch name, `/` replaced by `-` |
| a commit range | `since-<short-sha>` |
| working-tree changes | `wip-<branch>` |

Prefer the most stable identifier: a branch name outlives a sha, a PR number outlives a rename. Then
write that task's `task.md`, so the next session matches instead of deriving.

`scripts/task-state.sh <task-id>` validates an id and reports what the task holds — its `task.md`
anchors, every round, and each round's `input/` state. Each round comes back `prepared`, `claimed` or
`ran`; `revmux config` lists only the `ran` ones, so the script is how a round that is still open —
never reviewed, or reviewed by a run that never finished — becomes visible. A `claimed` round is only
re-runnable while that run left nothing in it; revmux refuses the name and says so if it did not.

### Run names: `NN-label`

`01-initial`, `02-after-fix`, `03-final`. Sorts lexically, so rounds read in order. `NN` is one past
the highest round already there.

Do not mix vocabularies inside one task — `round-1` next to `after-fix` shares no ordering axis.

`--run` is required and has no default. A round that has already run is an error, not an overwrite. A
round whose review never finished is not one of those: re-run it under the same name, with the same
`input/`, as long as that review had not already written artifacts into the round — if it had, revmux
refuses the name and names what it found, and the answer is a new round with the `input/` copied
across. `task.md` is reserved and cannot name a round.

## Rounds

```bash
revmux new --task pr-123 --run 01-initial      # then write its input/
revmux --task pr-123 --run 01-initial
<fix findings>
revmux new --task pr-123 --run 02-after-fix    # then write its own input/
revmux --task pr-123 --run 02-after-fix
```

revmux injects the prior rounds into every composed prompt: one line per round with its name, when it
ran, counts by severity and which sources degraded, carrying its own re-evaluate-independently
instruction.

**Never paste prior findings into `scope.md`** — it duplicates the injection and anchors the agents.

Round 2's `scope.md` describes what round 2 reviews: the fixes, and the range they land in.

## Removing a task

```bash
revmux cleanup --task <id>
```

Rounds accumulate and nothing removes them as a side effect of a review. `revmux cleanup` is the one
command that does, and it takes a whole task: a task's rounds are one review's history and are read
together, so removing part of one leaves `revmux stats` reporting the remainder as the whole record.
Nothing links tasks together, so removing one loses that task's own record and affects nothing else.
