# Reviewing a pull request

revmux fetches nothing, runs no git and checks nothing out. Getting the PR onto disk and pointing the
review at it is this skill's half, and this file is that procedure. Entered from Step 1 when the subject
is a pull request — "revmux pr 123", "review PR 123 with revmux", a pull-request URL.

```
gh pr view       metadata: head branch, base, url, size — and the task.md anchors, for free
git worktree     the PR head checked out somewhere else, the user's checkout untouched
--workdir        the agents run there; revmux itself still runs from the main checkout
cleanup          worktree and temp branch removed, the archive left behind
```

## 1. Resolve the PR before anything else

**A number alone is not a PR.** `123` is a number in whatever repository the shell happens to be
standing in, which may not be the one the user meant. Establish the repository first and qualify every
`gh` call with it:

```bash
origin=$(gh repo view --json nameWithOwner --jq .nameWithOwner)     # the current checkout
gh pr view <n> --repo <owner/repo> --json \
   number,title,body,url,state,isDraft,headRefName,baseRefName,additions,deletions,changedFiles
```

Record `url`, `headRefName` and `baseRefName`. Step 2 of SKILL.md writes all three into `task.md`, and a
pull request is the one review subject where they need no guessing.

A draft PR is a review the author did not ask for yet. Say so and let the user decide rather than
reviewing it silently.

Whether the PR is in this repository or another decides how it is fetched, and nothing else.

## 2. Fetch it into a worktree

**Same repository** — fetch the head into a temp branch and add a worktree for it. Both are temporary
and both are removed in step 5:

```bash
wt="${TMPDIR:-/tmp}/revmux-pr-<n>"
git fetch origin pull/<n>/head:revmux-pr-<n>
git worktree add "$wt" revmux-pr-<n>
workdir=$(git -C "$wt" rev-parse --show-toplevel)
```

Outside the repository on purpose: nothing is added to the user's tree, so no `.gitignore` entry has to
exist for the review to leave no trace.

**A different repository** — never fetch a foreign PR into the user's own repository. Clone it to
scratch and check the head out there under the same branch name, so every step below is unchanged:

```bash
clone="${TMPDIR:-/tmp}/revmux-pr-<n>"
git clone --filter=blob:none https://github.com/<owner/repo>.git "$clone"
git -C "$clone" fetch origin pull/<n>/head:revmux-pr-<n>
git -C "$clone" checkout revmux-pr-<n>
workdir="$clone"
```

Blobless rather than `--depth=1`: the merge base needs history, but not every blob.

Then compute the merge base inside whichever one exists, and diff against that:

```bash
merge_base=$(git -C "$workdir" merge-base origin/<baseRefName> revmux-pr-<n>)
```

**Diff against the merge base, never against the base branch head.** `origin/master..` picks up
everything master gained after the PR forked and reports another author's code as this PR's. If
`merge_base` comes back empty, stop and say so — a fallback to `HEAD~N` reviews a different range and
looks like it worked.

## 3. Run revmux from the main checkout, never from the worktree

Two roots, and a PR review is where confusing them costs the most:

| what | governed by |
|---|---|
| where the agents run their commands and read files | `--workdir` |
| the project config layer `./.revmux/`, and the `--tasks-dir` default | the process working directory |

So cd nowhere, and hand the worktree over as a flag:

```bash
revmux --task pr-<n> --run 01-initial --workdir "$workdir" --no-tui \
       > /tmp/revmux-pr-<n>-01-initial.json 2> /tmp/revmux-pr-<n>-01-initial.log
```

Two things follow from that, and together they are the whole reason for the rule:

- **the archive outlives the checkout.** `--tasks-dir` resolves against the process working directory,
  so the round lands in the main repository's `.revmux/tasks/pr-<n>/` and step 5 cannot take it with it.
  Run from inside the worktree instead and the entire record — prompts, stage snapshots, `events.jsonl`
  — sits inside the directory the cleanup deletes.
- **the PR's own `.revmux/` never loads.** The project layer is read from the process working directory
  and never from `--workdir`, so a branch that adds or edits `.revmux/lenses/*.md` does not get to
  rewrite the instructions its own review runs under. That text is what a headless agent with a shell
  executes — the trust section of `invocation.md` — and a pull request from someone else is exactly the
  case it exists for.

`scripts/launch-revmux.sh` keeps the launcher's own working directory, so the overlay form takes
`--workdir` the same way and both properties hold there too.

## 4. Write the round's context

Task and round naming is `task-dir.md` and a PR changes none of it. What a PR changes is that the
identifiers are already in hand:

- **match before deriving.** `revmux config | jq '.paths.tasks'`, on the `url` from step 1. A PR
  reviewed before is a task with rounds under it; deriving `pr-<n>` when the recorded id was something
  else forks the history and the new round runs with none.
- **`task.md`** — `description` from the PR title, `url`, `branch` from `headRefName`, `base` from
  `merge_base`. revmux stores these and resolves none of them.
- **`scope.md`** — the agents run in the worktree, so write the plain form and no `-C`:
  `git diff <merge_base>...HEAD`. `git -C <path> diff` breaks the child's permission prefix match for
  the reason `task-dir.md` gives, and `--workdir` has already put the agent in the right tree. Say the
  scale from `additions`/`deletions`/`changedFiles`, and name the files worth reading in full.
- **`goal.md`** — the PR title and body are the author's own statement of intent, which is what this
  file wants and what the `impl` lens reviews against. Add the linked issue's requirement when there is
  one. A PR body that says nothing is a reason to omit the file, not to paraphrase the diff into it.
- **`context/`** — the PR description in full, the discussion, and any earlier review comments, each as
  its own file: `gh pr view <n> --json comments,reviews` and
  `gh api repos/<owner/repo>/pulls/<n>/comments` for the inline ones. Curate — every file is a tool call
  an agent may spend.

## 5. Read the result, then clean up

Reading and presenting are Steps 5 and 6 of SKILL.md. Then remove the worktree and the temp branch, on
every exit path — a clean run, an exit `2`, or the user stopping partway:

```bash
git worktree remove "$wt" --force
git branch -D revmux-pr-<n>
```

The cross-repo form has no branch to delete in the user's repository; remove the scratch clone instead.

**The report and the archive are both outside the worktree**, so cleanup runs as soon as revmux returns
and nothing has to be read out of the checkout first. `git worktree remove` refuses a dirty tree, and
nothing in this flow writes to it — look at what is there before forcing past that.

## 6. Re-reviewing after the author pushes

Step 5 removed both the branch and the worktree, so a second round repeats step 2 verbatim: fetch the
head again, add the worktree again, recompute `merge_base` — the base moves too. Then a new round on the
**same** task, with its own `scope.md` naming the new commits and the range they land in.

**The loop is not available for a pull request.** It commits, and a fetched PR branch is somebody
else's — `loop.md` refuses it and offers a single re-review round instead. That is the right answer
here.

## What this half does not do

It reports findings to the user. It posts no review comment, approves nothing, merges nothing and
profiles no contributor. Nothing in this file writes to GitHub.
