# revmux issue triage — a profile, five lenses, and a verify mode for non-code claims

## Overview

revmux reviews code changes. This adds the ability to review a filed GitHub or GitLab item instead — an
issue, a discussion, a proposal — by running a four-agent adversarial panel over it and returning the
panel's grounded, verified arguments. `revmux 123` becomes one command that works out what 123 is,
gathers the context, runs the panel, and puts the recommendation to the maintainer.

**revmux does not decide.** The panel produces arguments; the skill weighs them and asks. That is how the
existing `rr` issue flow already works — it never auto-decides — and it is what makes this small. Every
expensive thing in the two superseded plans followed from assuming the binary had to return a verdict: a
new pipeline stage, a new schema, a new report field, a declared stage chain, archive and stats changes,
and a re-opened exit-code question. None of that is here.

**Supersedes two untracked plans, neither committed:** `20260809-revmux-triage.md` (16 tasks, bolting a
decide stage onto the fixed pipeline) and `20260809-revmux-pipelines.md` (17 tasks, rewriting the
pipeline as declared phases). Three independent reviews found the second not executable. Both external
reviewers, asked to compare this approach against that one, said ship this. The architecture question the
rewrite raised is real and is deliberately deferred — this plan changes no structure.

## Context (from discovery)

Everything here was verified against the code during review, and three claims in the previous draft of
this plan were **wrong** and are corrected below.

- `app/pipeline/verify.go:24` — `thinGroup = 2`. `groupByDir` merges every bucket holding fewer than two
  findings into one shared group **before** `capped` runs. Four agents with one argument each therefore
  produce **one** verifier group, not four. This defeated the previous draft's only Go change in exactly
  the case it was written for.
- `app/pipeline/find.go:214` — `parse` stamps `Sources = []string{spec.Name}` on every finding, and
  `synthesizer.attribute` rebuilds a non-empty union or errors. **No finding reaching verify has an empty
  `Sources`**, so a "key by source when there is no file" fallback is not inert for code review: a
  location-less code-review finding would move from the `"."` bucket to its agent bucket, changing
  `verifyGroup.dirs`, `label()`, the archived prompt filename and the displayed label.
- `app/finding/report.go:154` — `ExitCode()` returns 1 only when `len(Findings) > 0`, and `verifier.run`
  drops `rejected` findings and moves `immaterial` and `pre_existing` into separate slices. **"Exit code
  is always 1" is false.**
- `prompts/verify.md:35` defines `pre_existing` as "real, but present in code the change under review did
  not touch". In a triage there is no change, so a verifier following its instructions routes every
  grounded claim about existing code there — out of `Findings`. The materiality test asks "is the fix
  worth it" of arguments that have no fix, feeding `immaterial` the same way.
- `app/finding/finder-schema.json` — `file` is **required**, described as "path of the file the finding
  is in", and severity and confidence carry code-review rubrics. All three reach every triage prompt
  through `--json-schema` and the codex contract, pushing a model to invent a path for an argument that
  cites no code.
- `app/config.go:52,62,63` — `--min-confidence` defaults to **0**, `--max-parallel` to **4** (so a
  four-agent roster does not queue), `--verify-groups` to **6**.
- `app/prompt/defaults_test.go` — `TestDefaults_LensesAreSelfContained` requires `## Lens: <name>` and no
  `{{VAR}}`, and its count message enumerates all eight shipped lens names;
  `TestDefaults_LensesStayExecutorAgnostic` bans the substrings `json`, `schema`, `claude`, `codex`;
  `TestDefaults_NoShippedFileCarriesThePriorRoundBlock` iterates **lenses as well as profiles** and bans
  `prior round`, `previous round`, `earlier round`, `runs/`, `re-evaluate everything`;
  `TestDefaults_SeverityContract` indexes on the literal heading `## Severity bar` and already exempts
  `final`; `TestDefaults_WhatNotToReportContract` exempts nothing and has no `Greater` guard.
- Empirically, across all 54 archived stage snapshots in the local corpus there are **zero** findings
  with an empty `file`. The compatibility risk is real in contract and absent in practice.
- `scripts/analyze-corpus.py` and `app/archive/collect.go` need **no change** — `roundReader.reports()`
  tolerates a missing synthesis snapshot. It is the aggregate *readings* that degrade, not the mechanics.

## The design change this review forced

The previous draft keyed verification by source **when a finding had no file**. Three reviewers
independently showed that does not work: `thinGroup` folds the resulting singleton buckets back together,
`grounding` and `cost` cite code so they group by directory anyway, directory and source keys share one
namespace, and the fallback is not inert for code review.

**It becomes a flag instead of an inference.** `--verify-group-by=dir|source`, default `dir`. In `source`
mode the verifier buckets by `Sources[0]` regardless of file **and skips the thin merge**, because
thin-merging is justified by directory locality — one verifier reads an area once — and that argument
does not transfer to a per-agent bucket, where the whole point is that one panelist's case is judged
apart from another's. Code review is then untouched **by construction** rather than by an argument about
whether findings always carry files.

## Development Approach

- **testing approach**: Regular (code first, then tests).
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility

**Backward compatibility here means a code review is unchanged**, which the default `--verify-group-by=dir`
guarantees without relying on any property of the findings.

**Project-specific: no test may spawn a real model process** (`.claude/rules/testing.md`).

## Code-Quality Rules (HARD — verify against every task before marking complete)

These rules supplement project CLAUDE.md and are NOT optional. They are the gate for marking any task complete. If a rule is violated, the task is not done — refactor, re-test, then mark complete.

**Signatures (hard limits):**
- No function or method has 4+ parameters. `ctx context.Context` does not count toward the budget. If you need 4+, use an option struct (e.g., `type fooOpts struct { ... }`).
- No function or method has 4+ return values. Split the function into two single-purpose ones, or return a struct.
- Multiple adjacent same-type parameters (`oldLine, newLine int`) are a swap hazard — review whether they belong on a struct.

**Methods vs standalone helpers (project rule, hard):**
- If a function is called only from methods of a single struct, it MUST be a method on that struct. Calling pattern decides, not field access.
- Standalone helpers are reserved for: (a) constructors and entry points (`Parse...`, `New...`, `Decorate...`), (b) utilities shared by multiple unrelated types or by both standalone functions AND methods, (c) tiny cross-cutting helpers.
- Before adding any standalone helper, mentally walk its callers. If every caller is a method of one type, make the helper a method on that type.

**Visibility (private by default, hard):**
- Lowercase identifiers by default. Only export when an out-of-package caller exists.
- Exception (per CLAUDE.md): methods called by other structs in the same package CAN be exported for inter-component API clarity. This is the only exception. It does not extend to types, functions, constants, or variables.
- Before exporting any new identifier, grep for cross-package callers. If none, lowercase it.

**Comments (default: none, hard):**
- Default to writing no comments. Add one only when the WHY is non-obvious (a hidden invariant, a workaround, behavior that would surprise a reader).
- Exported items get godoc comments starting with the name. Unexported items get lowercase non-godoc comments — or no comment at all.
- Never describe WHAT the code does when the code itself is self-evident. Never write multi-paragraph comments on routine helpers.

**Per-task gate (before marking ANY checkbox complete):**
1. Formatter runs clean (`~/.claude/format.sh` or `gofmt -s -w` + `goimports -w`).
2. `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` reports zero issues.
3. `go test ./... -race` passes.
4. Scan the new code for the four rule classes above. Specifically:
   - Grep new function signatures: `grep -nE '^func.*\(.*,.*,.*,.*\)' app/<path>/*.go` — any hit with 4+ comma-separated params (excluding `ctx`) is a violation. Same for the return-value side.
   - For every new standalone helper, `grep -rn 'helperName(' --include='*.go'` and confirm at least one caller is NOT a method of a single type. If all callers are methods of one type, convert.
   - For every new exported identifier, grep cross-package. If no out-of-package hit, lowercase it.
5. Only after 1–4 pass: mark the task complete.

If a previous task shipped a violation (spotted later by user, reviewer, or yourself): fix it in the next commit BEFORE starting the next task. Do not let violations accumulate.

## Testing Strategy

- **unit tests**: required for every code task
- **e2e tests**: not applicable
- **every acceptance criterion must be deterministic and local.** Criteria needing a real model run or a
  real filed item belong in Post-Completion, not in a checkbox an autonomous executor will stall on or
  burn a real review against
- **`diff -rq` of both skill trees' `references/` and `scripts/` must come back empty**

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

**One profile, five lenses, one flag, one verify-prompt branch, and the skill.**

The `triage` profile runs four agents — `facts` carrying grounding and precedent, `thesis`, `antithesis`,
and `cost` on codex. Its body redefines the bar: severity means how much a point bears on the decision.
It carries its own `## What not to report` and its own `## Reporting`, because the shipped one says
*"Point at a specific file and line. A finding with no location cannot be verified"* — the exact
instruction triage must contradict.

The skill runs it `--profile triage --no-synthesis --verify-group-by source`, and never passes
`--min-confidence`.

**Key decisions and why:**

- **revmux returns arguments, not a decision.** The maintainer decides, which is what `rr` already does.
  Both external reviewers judged this a real simplification rather than a hiding place, on the grounds
  that the weighing already lived in the skill — what moves is *evidence production*, which is the part
  with no timeout, no retry and no archive today.
- **Grouping is a flag, not an inference.** See above.
- **`verify.md` gets a branch for claims that cite no code**, because its verdict vocabulary is
  code-shaped and would otherwise route the whole panel out of `Findings`.
- **`cost` takes the codex slot, not `antithesis`.** It reads code only, so a sandbox with no network
  cannot silently empty it, and losing codex degrades one input rather than the panel's opposition.
- **Severity is reinterpreted rather than replaced.** The heading stays the literal `## Severity bar`,
  because `TestDefaults_SeverityContract` indexes on it.

**What this knowingly gives up:**

- `revmux stats` mixes triage and code-review severities, and the damage is broader than totals:
  `collect.go` accumulates **per-lens** verdict stats, so `analyze-corpus.py`'s "which lens rates
  hardest" reading compares `thesis` against `bugs`, and a `--no-synthesis` round contributes a two-stage
  chain to a corpus of three-stage ones, skewing "which stage is filtering". `revmux self` proposes
  prompt changes off exactly those numbers.
- The prior-round inventory line reports a panel's arguments as "N findings (1 critical…)".
- **`--no-synthesis` is unenforced, and both reviewers rank this the top risk.** A bare `--profile triage`
  runs the drop rule, and every triage argument is single-source by construction, so minor-weight
  arguments are eaten wholesale and the boost fires on agreement between agents told to disagree. Silent
  and plausible-looking. Mitigated by saying so in the profile description, in `references/output.md` and
  in the skill's re-review path — three places, because the exposure is every future caller.
- The exit code is **unreliable**, not always 1. Nothing should branch on it for a triage run.
- `report.md` still groups by severity, so the archived report interleaves thesis and antithesis. The
  skill regroups from the JSON; the archive does not.

## Technical Details

### The flag and the grouping

```go
// key buckets a finding for verification: by the agent that raised it in source mode, and by directory
// otherwise. A panel's arguments are judged one panelist at a time, so one verifier never holds a case
// and its rebuttal.
func (v *verifier) key(f finding.Finding) string
```

`key` **absorbs `dir`**, which has exactly one caller and would otherwise trip `unused`. In `source` mode
`groupByDir` skips the thin merge; `capped` still applies as a resource limit. `name` and `freeName` are
untouched.

`--verify-group-by` is INI-backed like `--verify-groups`, so it needs a commented-out entry in
`app/defaults/config` and a row in the README flag table. `revmux config` reports it by reflection.

### The verify branch

`prompts/verify.md` gains a scoped section for a finding that cites no file:

- `pre_existing` does not apply — there is no change under review for something to pre-date
- `immaterial` means the point does not bear on the decision, not that a defect is not worth fixing
- the materiality test's third question, the fix's blast radius, is skipped when there is no fix
- check the cited issue, pull request or prior decision instead of opening a file

One shared file, no structure change, and code review is unaffected because the section is scoped to
findings with no location.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): tasks achievable within this codebase - code changes, tests, documentation updates
- **Post-Completion** (no checkboxes): items requiring external action - manual testing, changes in consuming projects, deployment configs, third-party verifications

## Implementation Steps

### Task 1: Write the five lens files

**Files:**
- Create: `app/prompt/defaults/lenses/{grounding,precedent,thesis,antithesis,cost}.md`
- Modify: `app/prompt/prompt_test.go`
- Modify: `app/prompt/defaults_test.go`

- [x] `grounding` — does the code do what the item claims, does the capability already exist, is it a
      duplicate
- [x] `precedent` — how comparable asks were decided before, read off the maintainer's closing comment
      rather than the open/closed state. It runs its own prior-art sweep. Answers supports, cuts against,
      or does not bear. **It must report an inability to search as a finding of its own** — an empty
      answer is indistinguishable from "no precedent exists", and `sourceResult.ok()` counts
      `{"findings": []}` as a successful source
- [x] `thesis` — the strongest honest case the item should be done or the report is real
- [x] `antithesis` — the strongest case against, including whether a simpler design reaches the same goal
      when the item proposes an approach
- [x] `cost` — what implementing it reaches into, and whether the work is proportionate to the value
- [x] **four authoring constraints the shipped-file tests enforce**: every body opens with
      `## Lens: <name>`; no `{{VAR}}` anywhere; none of the substrings `json`, `schema`, `claude`,
      `codex` — which constrains how `precedent` describes its sweep and is a trap for `cost` discussing
      what a change reaches into; and none of `prior round`, `previous round`, `earlier round`, `runs/`,
      `re-evaluate everything`, since `TestDefaults_NoShippedFileCarriesThePriorRoundBlock` iterates
      lenses too and `precedent` is the likeliest file in the repo to trip it
- [x] each carries a `description:` one-liner
- [x] update both lens inventories: the literal name set in `prompt_test.go`, and in `defaults_test.go`
      the count **and** the message enumerating all eight shipped names
- [x] reword `TestDefaults_ComprehensiveRoster`'s "carries every shipped lens exactly once" message — the
      assertion still passes, but the message stops being true
- [x] run tests - must pass before next task

### Task 2: Write the triage profile

**Files:**
- Create: `app/prompt/defaults/prompts/profiles/triage.md`
- Modify: `app/prompt/defaults_test.go`, `app/prompt/prompt_test.go`
- Modify: `app/initcmd_test.go`, `app/main_test.go`

- [ ] roster: `facts` (grounding + precedent), `thesis`, `antithesis`, `cost` on codex, with colours
- [ ] body: the shared read-only rules and context-paths block, then a bar under the **literal heading
      `## Severity bar`** — the contract test indexes on that string — where critical is decisive on its
      own, major bears strongly, minor is worth knowing but does not move the answer
- [ ] its own `## What not to report`, since the shipped one is about diffs
- [ ] **its own `## Reporting`**, which every shipped profile has and which triage must contradict:
      `comprehensive` says "Point at a specific file and line. A finding with no location cannot be
      verified". Triage's says what a location-less argument carries instead — the thread, the prior
      decision, the comparable item — and **instructs leaving `file` empty when the point cites no code**,
      as the counterweight to the finder schema requiring `file` and describing it as a path
- [ ] the `description:` names the panel and says the profile wants `--no-synthesis`. **Nothing about
      `--max-parallel`**: the default is 4 and the roster is four agents, so nothing queues
- [ ] exempt triage in `TestDefaults_SeverityContract` as `final` already is, asserting triage's own bar
      separately so it stays pinned
- [ ] exempt triage in `TestDefaults_WhatNotToReportContract`, which exempts nothing today — assert its
      block separately, **do not weaken the equality check for the others**, and add the
      `require.Greater(compared, 1)` guard the severity contract has and this one lacks
- [ ] update the four literal profile inventories: `prompt_test.go` (two sites), `initcmd_test.go`,
      `main_test.go`, and the count in `defaults_test.go`
- [ ] run tests - must pass before next task

### Task 3: Add `--verify-group-by` and group by source

**Design Contract:**

Type: none

Methods (full signatures):
- `(v *verifier) key(f finding.Finding) string` — replaces the `v.dir(f)` call in `groupByDir` and
  **absorbs `dir`, which is deleted**: one caller, and it would otherwise trip `unused`. A method because
  its only caller is a `verifier` method

Standalone helpers planned (justification why NOT a method): none

Exports (justification per item: who outside the package calls this?):
- `pipeline.Config.VerifyGroupBy` — set by `package main` from the flag, like `VerifyGroups`

**Files:**
- Modify: `app/config.go`, `app/defaults/config`, `app/main.go`
- Modify: `app/pipeline/pipeline.go`, `app/pipeline/verify.go`, `app/pipeline/verify_test.go`
- Modify: `app/config_test.go`

- [ ] add `--verify-group-by` with values `dir` (default) and `source`, INI-backed like
      `--verify-groups`, with its commented-out entry in `app/defaults/config`; reject any other value at
      load
- [ ] add `key`: `Sources[0]` in source mode, `path.Dir(File)` otherwise, and today's `"."` when a
      directory-mode finding has no file. Delete `dir`
- [ ] **in source mode, skip the thin merge.** `thinGroup` is justified by directory locality; a
      one-argument agent bucket must survive as its own group, or four singleton buckets fold into one
      and the change achieves nothing
- [ ] leave `capped`, `name` and `freeName` alone. Note in the task that `capped` merges the *smallest*
      groups first, so a panel larger than `--verify-groups` can still put thesis and antithesis in front
      of one verifier — acceptable at four agents against a default of six
- [ ] update the comments the change falsifies: `groupByDir`'s godoc, the `verifyGroup.dirs` field
      comment, `shortLabel`'s godoc, and `finding.StageRun`'s "verify fans out into one process per
      directory"
- [ ] **do not rename `groupByDir` or `verifyGroup.dirs`** — both become imprecise and the blast radius
      exceeds this change. Raise separately
- [ ] write tests: in source mode four agents with one argument each produce **four** groups; a mixed set
      of located and location-less findings groups by agent; the cap still applies; in dir mode
      everything groups exactly as today, including the existing root-bucket subtest — **and give that
      subtest's fixture a source**, since production findings always have one and it currently pins a
      state that cannot occur
- [ ] run tests - must pass before next task

### Task 4: Teach `verify.md` about claims that cite no code

**Files:**
- Modify: `app/prompt/defaults/prompts/verify.md`

- [ ] add a scoped section for a finding with no file: `pre_existing` does not apply, since there is no
      change under review for anything to pre-date; `immaterial` means the point does not bear on the
      decision rather than that a defect is not worth fixing; skip the materiality test's third question,
      the fix's blast radius, when there is no fix; and check the cited issue, pull request or prior
      decision instead of opening a file
- [ ] keep it scoped to location-less findings so code review is untouched — this file is shared by every
      profile and cannot be overridden per profile
- [ ] **this is the task that decides whether triage returns arguments or returns `findings: []` with
      exit 0.** Without it, a verifier following the shipped text routes every grounded claim about
      existing code to `pre_existing` and every unactionable argument to `immaterial`, and
      `verifier.run` moves both out of `Findings`
- [ ] no Go change; the gate is the mocked end-to-end in Task 7 asserting arguments survive verification

### Task 5: The skill flow, in both trees

**Files:**
- Create: `.claude-plugin/skills/revmux/references/triage.md` and the same under `plugins/codex/`
- Modify: both `SKILL.md`, both `references/invocation.md`, both `references/output.md`
- Modify: `plugins/codex/README.md`, `.claude-plugin/plugin.json`, `.claude-plugin/marketplace.json`

- [ ] **routing**: a bare `revmux 123` probes pull request, then issue, then discussion. Today the only
      number-shaped trigger is the PR line, so an issue number would fetch a worktree
- [ ] add triage to the `description:` frontmatter, the `argument-hint`, the activation triggers, the
      Step 1 routing table, and the `references/` pointer list in the "Answering questions" section
- [ ] **the four per-tree sites CLAUDE.md names, enumerated literally**: `SKILL.md`'s profile table,
      `SKILL.md`'s **separate "the user says" mapping table** ("triage this", "is this worth doing",
      "should we accept this", "should I close this"), `references/invocation.md`'s profile table, and its
      `Environment` executor count — "the other four shipped profiles need both" becomes five
- [ ] `references/triage.md`: fetch the item, its full thread and the author's history into
      `input/context/`, write `scope.md` and any framing into `goal.md`, run
      `--profile triage --no-synthesis --verify-group-by source`, **never pass `--min-confidence`**,
      present the arguments grouped by agent, and put the six answers to the user with the recommendation
      first and its reason in the option. Then draft and post through the existing approval path. Cover
      gh, glab and tea
- [ ] **the presenting agent reconciles nothing** — with synthesis off, nobody has resolved `facts`
      contradicting `thesis`, and the skill groups rather than adjudicates
- [ ] **do not fetch prior art** — the `precedent` lens sweeps for itself
- [ ] triage needs its own preparation path: Step 2's brief reads a diff and returns a shortstat. Say in
      `SKILL.md` that `triage.md` replaces Step 2 and hands back at Step 5
- [ ] add the five lens rows to both `references/invocation.md` lens tables
- [ ] `references/output.md`: triage severities mean weight; **never branch on a triage exit code**, which
      is unreliable rather than always 1; and repeat the `--no-synthesis` requirement here and in the
      re-review path, not only in the profile description
- [ ] the five places stating verification groups by directory, which Task 3 makes conditional:
      `README.md:134`, `README.md:273`, both `references/invocation.md:259`, and `.claude/rules/pipeline.md:16`
- [ ] add `triage.md` to the `references/` listing in `plugins/codex/README.md`
- [ ] ask the user about the plugin version bump and apply it to **both** `plugin.json` and
      `marketplace.json`, which carry it separately with nothing testing that they agree
- [ ] verify `diff -rq` of both `references/` and both `scripts/` comes back empty

### Task 6: Documentation

**Files:**
- Modify: `README.md`, `CLAUDE.md`
- Modify: `.claude/rules/prompts.md`, `.claude/rules/pipeline.md`

- [ ] README: the shipped-profile table and the CLI-requirements bullet above it, the five new lens rows,
      the prompt-tree diagram, and the flag table gaining `--verify-group-by`
- [ ] `.claude/rules/prompts.md`: the layout block gains triage and the five lenses
- [ ] `.claude/rules/pipeline.md`: the verify-grouping rule gains source mode and says why the thin merge
      is skipped there — the never-hand-one-verifier-everything rule is what it exists to preserve
- [ ] `CLAUDE.md`, four amendments: the severity-bar rule counts six profiles today and becomes seven,
      triage's deliberately different; the `## What not to report` invariant gains triage's copy and its
      new exemption; **the sentence praising `TestDefaults_SeverityContract` for deriving from
      `ProfileNames()` rather than listing profiles** now has two named exemptions, `final` and `triage`,
      and must say why each is one; and the profile and lens counts in the keep-in-sync bullets
- [ ] re-read every table and diagram for column alignment

### Task 7: Verify acceptance criteria

Every criterion here is deterministic and local. Anything needing a real model or a real filed item is in
Post-Completion.

- [ ] **code review is unchanged**: in dir mode an all-located findings set groups exactly as today, and
      the existing `groupByDir` subtests pass with fixtures that carry sources
- [ ] in source mode, four agents with one argument each produce four verifier groups
- [ ] a mocked end-to-end in `app/main_test.go` under `triage` with `--no-synthesis` and
      `--verify-group-by source`: the arguments reach stdout with `sources` intact, **and survive
      verification rather than landing in `pre_existing` or `immaterial`** — the Task 4 gate
- [ ] a mocked run asserting a second round sees the first through prior-round injection
- [ ] `--verify-group-by` rejects an unknown value at load and appears in `revmux config`
- [ ] `revmux config` reports the triage profile and the five lenses with their descriptions
- [ ] `preflight.sh triage` reports both binaries needed
- [ ] `diff -rq` of both skill trees comes back empty
- [ ] run `make test` and `make lint`; coverage meets the project standard of 80%

### Task 8: [Final] Update documentation

- [ ] update README.md and CLAUDE.md for anything that drifted during implementation
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Gate before the feature is called done — this one is not optional, it validates a design decision:**

- Prove the `precedent` lens's `gh` sweep actually executes from a claude agent under
  `--permission-mode auto` in headless `claude --print`. The decision that the lens sweeps and the caller
  does not rests entirely on it. If the classifier denies the call, the lens returns a thin sweep and
  nothing distinguishes that from "no precedent exists" — at which point the sweep moves back to the
  caller and `references/triage.md` changes.

**Manual verification:**

- Run triage against three real items of different kinds — a bug report, a feature request and an
  open-ended discussion — and read whether the recommendation you reach from the arguments is the one you
  would have reached alone. Previously-resolved issues make the best fixtures, since you already know the
  answer.
- Run a real second round on one of them to confirm prior-round injection reads well for a panel.
- Watch one run in the TUI to confirm the four agents are distinguishable.

**Deferred, deliberately:**

- The pipeline architecture question stands. Three independent reviews agreed the fixed pipeline is why a
  second review shape is awkward, and that the phase-based rewrite is real but was not executable as
  planned. Both external reviewers, asked to choose, said ship this first — and one noted it improves the
  rewrite's odds, since revisiting it later generalizes from two live archived review shapes rather than
  one shape plus a hypothesis.
- The known compromises are all consequences of not answering it, and all reversible. Nothing here paints
  the rewrite into a corner.
- The `rr` skill's issue and discussion flow becomes a candidate for replacement once triage proves itself.

---

Review record. Smells pre-check: 7 items applied. Three-way review (codex, fable, plan-review): all three
found the previous draft's grouping fix a no-op, because `thinGroup` folds singleton source buckets back
together — replaced with `--verify-group-by`, which also removes the located-argument hybrid, the
key-namespace collision and the false byte-identical claim. Two reviewers found `verify.md`'s verdict
vocabulary routes triage arguments out of `Findings` entirely, now Task 4. Also corrected: `find.parse`
stamps `Sources` on every finding so the `"."` fallback is unreachable; the exit code is unreliable, not
always 1; the finder schema's required `file` pushes models to invent paths, countered in the profile's
own `## Reporting`; `--min-confidence` must never be passed; four lens-authoring constraints, including
the prior-round-phrase ban that applies to lenses and most endangers `precedent`; the "the user says"
mapping table and five "grouped by directory" statements; and the corpus contamination is per-lens and
per-stage, not only severity totals.
