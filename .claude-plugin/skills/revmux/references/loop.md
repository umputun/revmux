# Loop mode — review, fix, re-review until clean

One round of revmux is a review. The loop is review → fix → commit → review again, until a review comes
back with nothing gating. revmux has no loop of its own: each iteration is a new round on the same task,
and revmux injects the prior ones itself.

Entered from Step 6, when the user picks the autonomous option — or straight away when he asked for a
loop up front, in which case round 1 is still reported before any fixing and Step 6 skips the question.

## Before starting

- **Local changes only.** A fetched PR branch is somebody else's; the loop commits, so it never runs
  against one. Offer a single re-review round instead.
- **The working tree must be clean.** Refuse to start otherwise — the loop's own commits have to be
  separable from whatever was already uncommitted.
- **Record the starting commit** and report it. Every round reviews the cumulative diff from it, and it
  is what undoes the whole loop: `git reset --hard <sha>`.

## What gates

Gating is `critical` and `major` in `findings`. Nothing else:

- `minor` never gates — it is what the loop is expected to leave behind.
- `immaterial`, `pre_existing` and `open_questions` are not in `findings` and never gate. An open
  question is a decision for the user, so the loop must not edit code to resolve one.
- **A finding restating a scope the round's own `scope.md` or `goal.md` puts outside the change does not
  gate.** The loop cannot overturn that decision by editing code, so the same finding returns unchanged
  on every round and no confirming round can ever clear it. Report it in full and treat the round as
  clean on it. **The exclusion has to be written in the round's input**, not recalled from the
  conversation: keyed on memory this becomes "he ruled that out" for any inconvenient major, which is
  silent suppression wearing the exemption's clothes. Written down, each round's brief carries it and
  the finding usually stops recurring at the source.
- A `degraded` run gates nothing either way: it is not evidence. Stop and report it.

**A round in which you fixed a GATING finding is never a clean exit.** Clean means a *review* came
back with zero gating findings. After fixing one, always run the confirming round. Sweeping only
minors is not an iteration and ends the loop — see Stopping.

## The iteration

1. Fix every gating finding, plus any minor co-discovered alongside them. A minor alone never starts an
   iteration.
2. **Enumerate what you touched before committing**: every value of an enum you branched on **and
   every transition between them where direction matters**, every field of a struct you folded or
   copied, every platform or error class the code distinguishes, and that a ratio's two sides come
   from one population. **A test has to tell the cases apart** — one pinning only the case that
   prompted the fix cannot catch what was left out, which is how the same enum defect shipped twice
   in one commit. Then re-read the finding and check the fix
   answers the mechanism it named rather than the example it used. This is where the loop's own
   convergence is won or lost — measured across four tasks, each round's fixes produced about two
   thirds of what the next round found. Run the tests and the linter after. A fix that breaks the
   build is not committed.
3. Commit locally. **Never push** — that stays the user's decision, and it is what makes the whole loop
   revertible.
4. Open the next round and run it. Its `scope` is the cumulative diff from the starting commit, not just
   this round's fixes, and it repeats every exclusion the earlier rounds carried — a round that drops one
   is a round the excluded finding gates again.

Profile per round follows Step 7: `final` when the fixes stayed inside what the last round flagged,
`comprehensive` when they spilled into tests or structure.

**A round whose fixes were documentation only uses `final`, never `comprehensive`.** A doc fix is new
prose, and `comprehensive` carries the `docs` lens over it — so the round finds a defect in the sentence
the last round just wrote, and the loop churns on prose while the code stands still. `final` drops that
lens and reports nothing below major, which is what a doc-only round is actually confirming.

## Stopping

Stop on the first of these:

| condition | what to report |
|---|---|
| a review returns zero gating findings | clean, plus whatever minors are left |
| the **code** gating count is not **strictly lower** than the previous round's **and** one of its gating findings repeats | the fix is not landing — name the repeated finding and stop |
| five rounds | cap reached, with what is still open |
| a run exits `2`, or `sources.degraded` is non-empty | the failure, not a verdict — `1` is findings and is the normal case |


**Count only findings in executable code toward that rule.** A gating finding in the skill text, a
prompt file or a schema description is real and worth fixing, but it must not drive
another round, because **fixing it writes new prose that the next round then reviews.** Measured over
one archived task: the production Go was clean of gating findings from round 2 onward while every
later major sat in the review system's own text — a rewritten rubric produced a new true major on each
of three rewrites. The severity bar is what makes this bite: prose caps at minor *except* a document a
machine executes against as a contract, which is exactly what those files are. So a branch editing the
reviewer's own contract text cannot produce a doc fix that is not itself gating material, and iterating
on it does not terminate.

**A shipped script is code, and a defect in its arithmetic counts.** The rule above turns on what the
fix writes, not on which directory the file sits in: correcting a computation deletes or replaces
logic and leaves no new prose behind, so it terminates the way a Go fix does. That is also how Step 6
tags it — `code` is executable logic, and a `.py` aggregation bug is that. Only the sentences such a
script *prints* are prose, and a finding about their wording batches with the rest.

Collect those instead: fix them in one batch at the end of the branch, then one confirming round under
`final` — whose roster carries no `docs` lens and reports nothing below major. If a round's gating
findings are *all* in that category, the loop is done even though the count did not fall.

That confirming round is the merge gate, so its `goal.md` says so and states the bar, the way Step 2
requires of any last-round goal. Without it the round runs as round N+1 of the same loop and returns
the same prose findings the batch just created — the roster narrows what is looked at, and only the
goal narrows what is worth reporting.

**Write down every round's gating count and each finding's mechanism and file before fixing anything.**
The non-converging row is a comparison against both, and it takes both to fire. A stalled count alone is
not a stall: round N's fixes produce about two thirds of what round N+1 finds, so one new defect
replacing one that was fixed is what a productive loop looks like from the outside. What stops the loop
is a finding from the previous round arriving again — the same mechanism in the same place, however it is
worded — because the fix did not answer it and the next one will not either. Name that finding and stop.

**A count that stalls or rises on findings that are all new is progress, and the loop continues to the
cap.** Say so in the round report — "round 3: 2 gating again, both new, continuing" — or the user reads a
flat number as a stall. The five-round cap is the bound on that case and it does not need a second one.

On a clean exit with findings left, ask **once** what to do with them, as a single question offering
three courses: fix the shortlist, fix every minor, or leave them all. **The shortlist goes inside the
question**, not only in the report above it — at most five, ranked, each with its mechanism and what
leaving it costs, since that is what he decides against. Fix what he picks and stop either way; no
review round runs after that question.

The shortlist is what the option buys. Offered only a blanket sweep, an unranked pile of minors gets
taken or dropped wholesale, and the two or three that were worth the round go either way with the rest.

## While it runs

Autonomous between rounds: no questions until it stops. The commits are the exception to the usual
draft-and-confirm, because opting into the loop is the authorization and nothing is pushed.

Report each round in plain language, carrying the combined surface/severity tags Step 6 puts on a
finding — "round 2: 3 findings, fixing 1 [code, major] and 1 [docs, major]; leaving 1
[comments, minor]", "round 3: clean". The user should be able to follow it without knowing any of the
vocabulary on this page.

Those tags are the convergence signal a gating count alone hides. Several rounds whose gating findings
are all `[docs, major]` or `[comments, major]` means the prose is churning while the code stands still —
say that outright rather than leaving him to infer it from the file names.
