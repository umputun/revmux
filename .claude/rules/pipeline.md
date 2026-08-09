---
paths:
  - "app/pipeline/**"
---

## Pipeline — the three-stage contract

Stage structure is **hardcoded**: find → synthesize → verify.
Everything that varies between review shapes is the roster and the severity bar, and both are configuration.
Do not turn this into a generic DAG engine — that was considered and rejected as over-engineering.

### Stage boundaries

- **find** — the profile's roster, all agents in parallel, launched staggered. Each returns structured findings.
- **synthesize** — one model call. Merges, dedups, boosts confidence, splits open questions and pre-existing issues.
- **verify** — parallel agents grouped by directory, or by the agent that raised the finding under
  `--verify-group-by source`. Each confirms, rejects, refines, or reclassifies its own group.

`--no-synthesis` passes findings through with their `sources` and `lenses` attribution intact.
`--no-verify` marks every finding unverified rather than silently claiming it was checked.

Each stage is its own unexported type (`finder`, `synthesizer`, `verifier`) owning its own methods and test file.
`Pipeline.Run` is a thin three-call orchestrator.
Do not let `Pipeline` accumulate stage logic — it becomes a god object holding three stages, event fan-out and I/O plumbing.

The I/O plumbing is held off it the same way: `app/pipeline/artifacts.go` owns the whole-artifact writers
(`save`, `saveStage`, the sticky `fail`).
`events.jsonl` is the deliberate exception and stays in `pipeline.go` beside `emit` — it is a stream held
open across the whole run under the mutex guarding it, not a whole-file write.

The paths those writers are handed are **not** this package's to name.
Every archive path is joined from an `app/task` constant — `EventsFile`, `AgentsDir`, `AgentPromptDir`,
`StagePromptDir`, `FoundFile`, `SynthesizedFile`, `VerifiedFile` — the same single source `app/archive`
and `package main` join the caller-facing layout from.
A literal spelled here is a second copy of the layout, and it drifts the next time the layout moves.

### No VCS. None.

This package must never import a git library, shell out to `git`, or walk a repo looking for `.git`.
Scope arrives as `{{SCOPE}}`, the **absolute path** of a `scope.md` the caller authored;
the pipeline passes that path through and never opens the file.
Agents read it and run their own diff commands.
A change that makes the pipeline read a repository — or read the scope file — is a change that belongs in the caller.
This is the hardest boundary in the project and it is what keeps revmux reusable and testable.

### Source counting

**A source is a process.**
The cross-source confidence boost counts distinct processes, never tags and never lenses.
An agent carrying two lenses that flags the same issue under both is still **one** source — it cannot corroborate itself.

The pipeline knows which process emitted which finding, so the count is structurally correct.
**That only holds if Go assigns the attribution, never the model.**

`finder.parse` overwrites `Finding.sources` on every parsed finding with exactly the executing
`AgentSpec.Name`, discarding whatever the model put there, and filters `Finding.lenses` down to the lenses
that agent actually carries — a model naming one it was never given is informational noise, and an empty
result falls back to the agent's full set, which raised the finding by definition.
**No schema exposes `sources`** — a field the model can fill is a field it will fill, and one agent naming
itself twice produces precisely the self-corroboration this rule exists to forbid.
`FinderSchema` omits `verdict` on the same grounds, but the omission is the finder's alone:
`VerifySchema` must carry one, since a verdict per finding is that stage's entire output.

**`parse` rewrites `Finding.id` in the same loop, to `<agent>-<n>`.**
The schema is one shape shared by every finder, so four agents on it each emit an id starting at `1`.
Synthesis derives each merged finding's sources union from the input ids it merged, so colliding ids do not
just look untidy — they make one agent's finding indistinguishable from another's and corrupt the source
count the whole confidence boost rests on.

Stamping happens in `find`, not in synthesis.
Deferring it to the synthesis prompt leaves `--no-synthesis` runs carrying model-invented attribution
straight into the report.

**Synthesis re-derives attribution rather than carrying it through, and `merged_ids` is how.**
`SynthesisSchema` exposes no `sources` either, for the same reason `FinderSchema` does not.
Each synthesized finding instead returns the input ids it merged, and `synthesizer.attribute` unions the
`sources` and `lenses` of those inputs, discarding whatever the model put in either field.
A merged id that is not an input is a **hard error**, never a skip: it means the model invented one, and
dropping it quietly yields a finding credited with fewer sources than it earned.

**An input id claimed by two outputs is a hard error too, for the mirror-image reason.**
The schema binds each input finding to **at most** one output, across all three lists taken together —
at most rather than exactly, because the drop rule below legitimately leaves a weak singleton in none —
so `synthesizer.parse` threads one claimed set through `findings`, `open_questions` and `pre_existing`,
not one per list.
An unclaimed input is therefore never an error; only a twice-claimed one is.
A reuse means one finder's work became two report entries, possibly a finding and a pre-existing issue
at once, contradicting each other about the same code.
Renaming the duplicate would keep both and invent an id for the second that no finder ever emitted,
leaving the archive unable to say which input it came from.

Rejecting a reuse is also what makes the **output** ids unique.
Each merged finding leads with the first id it merged, so distinct claims cannot collide, and verify —
which keys its verdicts by id — can never have one verdict reject or rewrite two findings.
Do not reintroduce a suffixing fallback: it would silently accept exactly the malformed attribution this
check exists to catch.

Pass the real roster into the synthesis prompt as data rather than letting the model infer it
from the findings themselves.
`{{SOURCES}}` must state which agents ran, which degraded, and which emitted each finding.

### Confidence and drop rules

Implemented in `prompts/synthesis.md`, not in Go.
Synthesis is a full model stage: deciding that two differently-worded findings describe one issue is semantic work,
and matching on file and line in Go would both merge unrelated findings and split identical ones.

- dedupe on `(file, line ±2)` with similar descriptions
- boost: `min(99, max_conf + 10*(N-1))` over distinct sources
- severity: max across sources
- drop: single-source, confidence below 80, no corroboration — **never a critical or a major**
- open questions and pre-existing issues are split out **first** and are never boosted, dropped, or verified for fixing

**What the drop rule removes is announced, because nothing else can see it.** `synthesizer.unclaimed`
returns every input id no output claimed — `attribute` already errors on a second claim, so the set is
exact — and `run` emits it as `EventDropped` carrying the findings themselves. Measured over one archived
corpus synthesis removed 58 findings where verify removed 2, three of them critical, and the only way to
learn that was to hand-diff two stage snapshots. `parse` returns the list rather than emitting it, so the
stage's parsing stays testable without an event channel.

**Severity exempts a finding from the drop rule, and only the drop rule.**
A single source is not evidence against a serious defect: one reviewer looking in the right place is
the normal case for the worst bugs, and corroboration is a boost rather than a gate.
Measured over the first seven rounds, four majors met all three drop conditions and one met them
exactly — single source, confidence 75, nothing corroborating — and survived only because the model
declined to apply the rule it was given.
A critical or major that would be dropped is kept and routed to the verifier instead, the same escape
hatch a degraded run uses.

**Degraded runs do not drop.**
With a source dead, corroboration is rarer, so the drop rule starts eating findings the missing source would have confirmed.
The gate is prompt text like every other rule in this section: `{{SOURCES}}` states which agents degraded,
and `prompts/synthesis.md` instructs the model to keep every would-be-drop and route it to the verifier
when that list is non-empty. It is not a Go branch, and `SourceStatus.Degraded()` is not what enforces it.
The verifier is the authority anyway; dropping is only a cost optimization.

### Degrade policy

Stall or crash → kill → retry **once** → second failure marks the source `degraded` and the pipeline **continues**.
Never abort the whole run because one agent died — one flaky agent would waste every other agent's work and tokens.

**Every source degrading is the one exception.** A run with zero reporting sources has no review to report,
so it is a tool error and exits `2`, not a clean report with an empty findings list.
See `.claude/rules/config.md` on exit codes.

**Synthesis retries once and then fails the run — it is the one stage with no degrade path, deliberately.**
It is a single call standing between every finder's completed work and the report, so `synthesizer.dispatch`
gives it the same one-launch-plus-one-retry the find stage gives an agent: a transient `529` must not
discard a find stage that already ran for minutes.
An attempt that returns an error, a non-zero exit or no structured output did not deliver and is retried;
one carrying output despite a stall or a rate limit did, exactly as `finder.fault` reads it.
A second failure exits `2` rather than passing the unmerged findings through.
Degrading to passthrough here would emit a `--no-synthesis`-shaped report to a caller who did not ask for one —
no dedupe, no confidence boost, no open-question split — and a quietly unsynthesized run that reads like a
merged one is the failure mode this whole section exists to prevent.

**A failing verifier degrades its own group, never the stage.**
Find already paid for the findings and synthesis already merged them, so a dead, unparseable or empty
verifier returns its group `unverified` — the same honest value `--no-verify` produces — and emits
`EventAgentDegraded` naming the group. A verdict outside the enum is treated as unverified too, since the
codex path has no `--json-schema` to enforce the vocabulary.
The one thing that does fail the stage is a prompt tree that will not compose: every group would hit it
identically, and `.claude/rules/prompts.md` requires an unresolved variable to be loud.
`verifyGroup` therefore carries its composed prompt, so every group is composed before any is dispatched
and that config error surfaces in one place.

Every degraded source must be loud in three places or it is effectively hidden:

1. `SourceStatus.degraded` in the JSON output
2. the banner at the top of the markdown report
3. `{{SOURCES}}` in the synthesis prompt

A quietly degraded run that reads like a complete one is the worst failure mode this tool has.

### Stagger

Agent 1 launches immediately; the rest are released once it produces its first output,
or after `stagger-delay` if it never does.

The release signal needs an explicit path, or `stagger-delay` silently becomes the only one.
The leader's `sink` carries a first-activity callback guarded by `sync.Once`, invoked on the first
`EventActivity` it receives and **before** that event is offered to the lossy channel — watching `Events()`
instead would be wrong twice over, since it drops events and has a single reader already.
First activity means an assistant turn for claude and the first raw stdout write for codex,
so a codex leader still releases the rest without waiting out the full delay.

**It must be model output alone — `EventActivity` or `EventProgress` — never any event the sink happens
to receive.** Both of those mean the process produced an assistant turn: prose in one case, a tool call
in the other. An agent that opens by reading twenty files says nothing for its first minute, so gating
on prose alone would leave `stagger_delay` as the only release path for the whole roster.

Everything else is excluded, and `proc` therefore emits **nothing** when the fork succeeds. It once
announced the binary there, and that one event was enough to latch the gate before a byte had been
read: nothing was staggered, the delay was dead, and the codex first-chunk signal that exists solely to
release the roster never mattered. The gate is there to prove the leader can actually reach a model
before three more processes try — a started process proves only that the binary exists.

A test that stubs a kind nothing produces guards nothing. Pin this with an event the executors really
emit, `EventInfo` being the earliest of them.
`EventRateLimit` and `EventFinished` are excluded for the same reason: a throttled leader releasing the
roster hands the limit to every follower, and by the time one exits the delay is the honest signal.

**A resolved-configuration line is not activity either, which is why `EventInfo` exists.**
Codex prints its `model:` / `sandbox:` / `reasoning effort:` banner on stderr before it has contacted a
model, and stderr drains concurrently with the stdout parse, so forwarding those as `EventActivity` opens
the gate milliseconds after a codex leader forks — the same dead stagger `EventStarted` would cause.
Both kinds render identically downstream; only the release path distinguishes them.

**The gate latches open and never re-arms, and `runFind` latches it on completion.**
One `stagger` instance serves both find and verify, taken from `Pipeline` rather than constructed per stage:
a fresh instance would charge verify another `stagger-delay` to re-prove the auth find already proved.
That reuse only works because the gate latches, and it needs the third release path as well as the first two.
A single-agent roster never waits on the gate — its only agent is index 0, the leader — so nothing calls
`leaderStarted` and the gate would stay shut until the delay elapsed, leaving verify's groups blocked on a
leader that had already finished. Find running to completion is the stronger proof anyway: at least one
process finished, or the run had already failed with `errNoSources`.

**The stagger must never influence which agents run, on which models, or in what order.**
Roster composition is a review-quality decision.
It does not group agents, does not reorder them, and does not constrain per-entry `model`.

`max-parallel` caps concurrency independently of the stagger.
Result ordering must stay deterministic regardless of completion order, or reports become diff-noisy between runs.

Name the primitive for what it does: `acquire` takes a slot, `release` gives it back.

### Token accounting

The claude `result` event carries per-model `usage`, so per-agent totals cost nothing to collect.
Record tokens per agent and summed per run, and stop there —
revmux reports what was spent, it does not model or optimize it.

### Event channel

The pipeline is headless and knows nothing about terminals.
`Events()` returns one buffered channel with **exactly one reader**: the active renderer,
either `app/progress.go` or `app/ui`, never both.

- A blocked renderer must never stall the pipeline. Buffer, and drop or coalesce rather than block.
- Every new `EventKind` needs a case in **both** renderers, or it is invisible in one of them.
- Events carry the agent name so the TUI can route them without inferring anything.
- Executor activity reaches the channel through a small unexported adapter satisfying `executor.EventSink`.
  Do not make `Pipeline` itself satisfy that interface — it forces an exported method whose only
  purpose is interface satisfaction, and it collides with the pipeline's own emit path.

**The archive is not a second subscriber on that channel.**
A Go channel distributes, it does not broadcast: two readers would each receive an arbitrary subset,
and the drop-rather-than-block policy makes the split nondeterministic.
Adding a reader to `Events()` would therefore corrupt both the display and `events.jsonl` at once.

`emit` writes to the archive **first and synchronously**, then offers the event to the channel.
That ordering is what makes `events.jsonl` a complete decision record while the display stays droppable:
a dropped event is a cosmetic gap, a missing archive line is a permanently unauditable run.
An archive write that fails is a run failure, not a warning — see `.claude/rules/config.md` on exit codes.

### Executor construction

`find` selects an executor per roster entry, because entries differ in the runner their `model:` names.
The stages select one too, resolved by `Profile.Stage` from three layers, highest first: the profile's
optional `stages:` override, the stage file's own `model:`, then the profile's `model:`. The shipped stage files
name none, so under `codex-only` both stages run on codex without either file mentioning it.
The pipeline must not import concrete executor types to do that.

Inject a factory on `Config` from `package main`.
**Both the field and the factory's return type must be exported**, or `package main` cannot supply it:
a lowercase field is unsettable from another package, and a function returning an unexported interface
is unnamable and therefore unassignable there.

```go
type Runner interface {                                  // exported: package main names it
    Run(context.Context, executor.Request, executor.EventSink) (executor.Result, error)
}
type RunnerSpec struct{ Executor, Model, Effort string } // shared by AgentSpec and Stage
type Config struct {
    NewRunner func(RunnerSpec) Runner
    ...
}
```

Consumer-side and exported are not in tension — the interface is still declared here, by the consumer;
exporting it only lets the supplier name it.
`RunnerSpec` exists so a stage can select a runner without fabricating a fake roster entry,
which `.claude/rules/prompts.md` forbids.

The archive is the exception to `Config` injection being enough — see the event channel rule above.

### Verifier grouping

One agent per directory, thin directories merged, group count capped from config.

**Never hand the whole findings list to one verifier.**
Materiality is a per-claim judgment, and a verifier holding the full list anchors on the first few,
then rubber-stamps or batch-rejects the rest.
Serial verification is also the review's bottleneck — N parallel verifiers finish in the time of the slowest, not the sum.

Grouping by directory rather than per-finding is deliberate: directory approximates code locality,
so one verifier reads that area once and judges several findings against it.
Per-finding would re-read the same file N times for N findings in it.

**`--verify-group-by source` keys by the agent that raised the finding instead, and skips the thin merge.**
It is a flag rather than an inference off an empty `File`, so a code review is untouched by construction:
`find.parse` stamps `Sources` on every finding, so a source key is always available and a directory-mode
run never reaches this branch.
The thin merge does not carry over, because what justifies it is directory locality — one verifier reading
an area once — and a per-agent bucket has no locality to amortize.
Merging there would defeat the mode outright: a panel of four agents holding one argument each is four
buckets under `thinGroup = 2`, and every one of them folds into a single group.
That is the never-hand-one-verifier-everything rule above, reached the long way — one verifier would hold a
case and the case answering it, and anchor on whichever it read first.
`capped` still applies as a resource limit, and it merges the *smallest* groups first, so a panel larger
than `--verify-groups` can still put two opposed agents in front of one verifier.
