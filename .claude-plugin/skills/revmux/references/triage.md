# Triaging a filed item

revmux reviews a diff by default. Triage points the same supervision at a **filed item** — an issue, a
defect report, a feature request, a proposal, a discussion — and returns a four-agent panel's arguments
about it. Entered from Step 1 when the subject is a filed item rather than a change.

**Nobody in this flow decides.** The panel produces the strongest grounded version of each part of the
argument and stops there; the maintainer picks. This file is the whole procedure — it **replaces Step 2**
and hands back at SKILL.md Step 5.

```
forge CLI      the item, its whole thread, the author's history — into input/context/
scope.md       what was filed, where its thread lives, what the panel is judging
revmux         --profile triage --no-synthesis --verify-group-by source
present        grouped by agent, never reconciled
the six        accept, narrow, ask, defer, decline, hold — the maintainer's call
```

## 1. Work out what the number is

**A bare number is not a kind.** `revmux 123` may mean a pull request, an issue or a discussion, and the
three are fetched differently. Probe in that order and take the first that resolves — a repository
numbers all three from one sequence, so a hit is unambiguous:

1. **pull request** — hand off to `pr.md` and stop reading here. A PR is a change, and a change is
   reviewed rather than triaged, even when the user asked "should we take this"
2. **issue** — triage it, below
3. **discussion** — triage it, below

Establish the repository first and qualify every call with it, exactly as `pr.md` does: a number is a
number in whatever checkout the shell is standing in.

| forge | probe | thread | author's history |
|---|---|---|---|
| GitHub | `gh pr view <n>`, then `gh issue view <n> --json number,title,body,url,state,author,labels,createdAt` | `gh issue view <n> --comments` | `gh search issues --author <login> --repo <owner/repo> --state all --limit 30` |
| GitLab | `glab mr view <n>`, then `glab issue view <n> -F json` | `glab api "projects/:id/issues/<n>/notes?sort=asc&per_page=100"` | `glab issue list --author <login> --state all` |
| Gitea | `tea pulls <n>`, then `tea issues <n>` | `tea comments list <n>` | `tea issues list --author <login> --state all` |

Two things about the thread column:

- **`glab issue view --comments` has been seen to omit the newest note.** Use the notes API with an
  explicit sort and page size, as above. A triage that misses the last comment triages a question that
  was already answered.
- a **GitHub discussion** has no `gh` subcommand. It is GraphQL:
  `gh api graphql -f query='query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){discussion(number:$n){title body url author{login} comments(first:100){nodes{author{login} body}}}}}' -F o=<owner> -F r=<repo> -F n=<n>`

**Do not sweep for prior art.** The `precedent` lens runs its own, and it reports the sweep it could not
run as a finding of its own. Doing it here duplicates the work, anchors the panel on what this session
happened to find, and hides a lens that came back empty because it was blocked.

## 2. Gather the item into a round

Same delegation as Step 2 and for the same reason: fetching a thread and a history is a dozen calls whose
output answers nothing the user asked. Spawn **one** subagent the way SKILL.md's Step 2 spawns its own —
that step says how in this harness — say one line first (`Gathering issue 123 and its thread…`), and hand
it this brief with every path already expanded:

> Gather a filed item for a revmux triage round. Write files; change no source, run no review, post
> nothing to the forge, commit nothing.
>
> The item is `<the kind, number and repository resolved above>`. Its metadata is `<the JSON from the
> probe>` — take it as given rather than fetching it again.
>
> 1. Match an existing task before minting one: `revmux config | jq '.paths.tasks'`, on the item's `url`
>    first and its `description` second. An item triaged before is a task with rounds under it, and a
>    second id for it runs as a first round with no history. Derive `issue-<number>` or
>    `discussion-<number>` only when nothing matches.
> 2. Run `<the absolute path this session resolved for scripts/task-state.sh> <task-id>` for the round
>    inventory, then `revmux new --task <id> --run <NN-label>` with `NN` one past the highest there.
>    Write only to the paths it prints.
> 3. `scope` — required, **under 1500 characters**. What was filed, by whom, when, and its current state;
>    the item's URL and the command that reads its thread; what the panel is being asked to weigh. Name
>    the code the item is about when it names any, as paths to read rather than quoted source. Do not
>    argue the item either way and do not summarize the thread's conclusion — four agents are about to
>    read it.
> 4. `goal` — the maintainer's framing when he gave one, **under 1000 characters**: what he wants out of
>    the triage, and anything he has already settled. Omit the file when he said nothing rather than
>    inventing a frame.
> 5. `profile` — the project's own conventions, as in Step 2. Copy the previous round's when a round has
>    one.
> 6. `context/` — one file each: the item's body in full, its whole thread in order with each comment's
>    author, the author's own history from the command above, and any item the thread links. Curate:
>    every file is a tool call an agent may spend.
> 7. `task_file` — `description` from the item's title, `url` from the item. No `branch` and no `base`:
>    a filed item is not a ref range, and inventing one makes the next session match on it.
> 8. Stop at the last write. No `ls`, no second `task-state.sh`, no `git status`.
>
> Return JSON only: `{"task": "", "run": "", "round_dir": "", "scope_path": "", "wrote": [],
> "kind": "", "number": 0, "url": "", "comments": 0, "notes": ""}`. Put anything that went wrong in
> `notes` and do not paper over it.

## 3. Run the panel

```bash
revmux --task <id> --run <name> --profile triage --no-synthesis --verify-group-by source --no-tui \
       > /tmp/revmux-<id>-<run>.json 2> /tmp/revmux-<id>-<run>.log
```

Background it and take the progress feed from Step 4 of SKILL.md unchanged. Four agents, so it runs at
the short end of the usual range.

**Every flag on that line is required, and two of them are silent when dropped:**

- **`--no-synthesis`** — every triage argument is single-source by construction, and synthesis drops weak
  singletons. Left on, it eats the panel's minor-weight arguments wholesale and boosts confidence where
  two agents *told to disagree* happen to agree. The report still looks like a report.
- **`--verify-group-by source`** — verification buckets by directory by default and merges thin buckets
  into one, so a four-argument panel reaches a single verifier holding a case and its rebuttal together.
  In `source` mode each panelist's case is judged on its own.
- **never pass `--min-confidence`.** It filters before anything renders, and an argument's confidence is
  the panelist's honesty about his own evidence, not a ranking of what matters.

`--profile triage` alone is the failure mode to watch for, because it produces a plausible short report.

## 4. Read it, then present the arguments

Exit code first, `sources.degraded` second — SKILL.md Step 5, unchanged in both. Then two differences
that matter more here than anywhere else:

- **Do not branch on the exit code.** `1` means findings were reported and `0` means none survived, but a
  triage run reaches `0` through routes a code review does not, so read `findings` and say what is in it.
- **Reconcile nothing.** With synthesis off, nobody has resolved `facts` contradicting `thesis` or `cost`
  contradicting `antithesis`. That contradiction is the product: it is what tells the maintainer the
  question is close. Presenting one side as settled hides the thing he is being asked to decide.

Present **grouped by agent**, not by severity — `facts`, `thesis`, `antithesis`, `cost` — using the
`sources` array, which carries the agent name. Within a group, order by severity.

Severity here is **weight on the decision**, not damage: `critical` is decisive on its own, `major` bears
strongly, `minor` is worth knowing and does not move the answer. Say so once, in a half-sentence, or the
counts read as a defect report.

Each argument gets its title, the body's reasoning, and what it cites — a comment and its author, a
comparable item and how it was answered, a file and line where the point is about code. An argument that
cites nothing is a hunch and should be presented as one. `open_questions` are what the panel could not
settle and go in their own short section; `immaterial` is what verification judged does not bear on the
decision, reported separately and kept out of the counts.

Cross-source corroboration means much less than it does in a code review: four agents given four
different parts of one argument are not expected to overlap, so `sources` holding two names is a
curiosity rather than the strongest signal in the report.

## 5. Put the six to the user

The panel produced the case; this is where the maintainer answers it. Six options, the one the arguments
point at first and marked as recommended, **with its reason in the option itself** rather than only in
the text above it:

| answer | when the arguments point here |
|---|---|
| accept it | `thesis` carries a critical, `cost` is proportionate, `precedent` supports or does not bear |
| accept something smaller | `antithesis` names a simpler design reaching the same goal, or `cost` is the objection |
| ask the author | `grounding` could not tell what is being claimed, or the panel splits on what the item means |
| defer it | the case holds and the cost does not fit now — real, and not this quarter's |
| decline it | `antithesis` or `precedent` carries a critical, and nothing rebuts it |
| decide nothing yet | the panel split, or `facts` reports it could not check the thing the case rests on |

Use whatever the harness's own way of asking is — SKILL.md Step 6 names it for this one.

**Never act on the answer before it is given, and never post because the arguments look one-sided.** The
panel was built to argue; the maintainer decides, and this question is where he does it.

## 6. Draft the reply, then post it

Only after he picks, and only through the approval path the harness already uses for forge writes:
draft the comment, show it, post it on approval and not before.

The comment says the decision and the reason it turned on. It does **not** relay the panel: an author
reading four agents' arguments about their issue is reading a machine deliberate about them, and the two
points that decided it are worth more than the twelve that did not. Never say a review tool produced it.

```bash
gh issue comment <n> --repo <owner/repo> --body-file <file>
glab issue note <n> -m "$(cat <file>)"
tea comments add <n> --description "$(cat <file>)"
```

Closing, labelling and assigning are the maintainer's, in his own words, and are not part of triage.

## 7. A second round

A thread that moved — the author answered, someone else weighed in, the maintainer stated a constraint —
is a new round on the **same** task, with its own `scope.md` naming what changed and its own `context/`
carrying the new comments. revmux injects the prior rounds itself, so do not paste the first panel's
arguments into the scope: the whole point of the injected block is that the panel re-derives them.

Same three flags, every time. A re-review that drops `--no-synthesis` is the same silent failure as the
first round dropping it.

## What this half does not do

It hands the maintainer arguments and posts what he decides. It closes nothing, labels nothing, assigns
nothing, opens no pull request against the item and profiles no author — what someone has filed before is
precedent about items, never evidence about them.
