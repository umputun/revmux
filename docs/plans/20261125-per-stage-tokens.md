# Per-stage token counts on stats.stages[]

## Overview
- Add a `tokens` field to every `finding.StageRun`. It is what that stage's CLI reported, failed attempts included.
  This answers issue #62: `manifest.json` reports tokens per find agent and per run, but synthesis and verify
  are not attributed to anything.
- The only workaround today is subtracting the cumulative `stats.tokens` in consecutive `stages/*.json` snapshots.
  That works by accident: nothing promises those totals are cumulative, and the maintainer chose a field over
  documenting that accident (option 2 in the issue).
- find carries the sum of its agents, so the stage entries add up to `stats.tokens` exactly.
- No new plumbing past the pipeline: `manifest.json` (top-level `stages`) and the stage snapshots
  (`stats.stages`) both copy `rep.Stats.Stages` already.

## Context (from discovery)
- `app/finding/report.go:78`: `StageRun` and its godoc. The godoc says "The find stage leaves all three empty",
  which is stale once find carries tokens. `SourceStat.Tokens` (`:49-54`) carries the per-executor caveat.
- `app/pipeline/pipeline.go`:
  - `runFind` builds the find entry inline (`:189`). At that point `rep.Stats.Tokens` is exactly the agents' sum
    (`find.go:301`).
  - `runSynthesis` (`:212`): `out.Stats.Tokens` is synthesis-only. `synthesizer.dispatch` sums across attempts
    (`synthesize.go:101-106`).
  - `runVerify` (`:232`): `out.Stats.Tokens` is the CUMULATIVE total. The stage-only count is `v.tokens.Load()`
    (atomic, added before the error is judged, `verify.go:198`).
  - `stageRun` (`:241`) builds the synthesis and verify entries.
    Its `took` argument is already per-stage wall clock, not cumulative.
- `app/revmux/artifacts.go:33,113`: the manifest copies `rep.Stats.Stages` to top-level `stages`.
- Tests:
  - `app/pipeline/pipeline_test.go`: `TestPipeline_Run_stats` and `TestPipeline_Run_recordsTheStageRunner`.
  - `app/pipeline/synthesize_test.go`: `TestPipeline_Run_synthesis`.
  - `app/pipeline/verify_test.go`: `TestPipeline_Run_verify`.
    Its first subtest runs ONE verify process: one finding in each of two directories, and `groupByDir` merges
    directories below `thinGroup = 2` (`verify.go:25,328`) into one group.
    `VerifyGroupBy = groupBySource` skips that merge and gives one group per agent.
  - `h.runner` (`pipeline_test.go:422`) gives every verify group the same fixture, keyed only on
    `verifyMarker`, and counts no calls. Per-group answers and attempt counting need a custom runner.
  - No test compares a `StageRun` or a JSON golden exactly, so the new field breaks nothing existing.
  - No `package main` test decodes the manifest's `stages`, so none is added for the free copy.
- Maintainer's constraints (issue #62 comment):
  - no `omitempty`;
  - amend `.claude/rules/pipeline.md` and the `StageRun` godoc;
  - docs in `site/docs.html`, `site/reference.html`, and `references/output.md` in both skill trees, naming both
    locations (`stats.stages` and the manifest's top-level `stages`);
  - keep `--no-verify` with no verify entry, and a verify with nothing to check with an entry that has no executor.

## Development Approach
- **testing approach**: Regular (code first, then tests). Add the field and plumbing, then the stage-token
  assertions in the same task.
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- mocked `CommandRunner`/runner only, never a real model (`.claude/rules/testing.md`)
- maintain backward compatibility: this is an additive JSON field, and no existing key changes

## Testing Strategy
- **unit tests**: at the `Pipeline.Run` level, because `stageRun` lives in `Pipeline`.
  The component tests (`synthesizer.run`, `verifier.run`) never see a `StageRun`.
- **invariant**: on every run shape, the sum of `Stats.Stages[].Tokens` == `Stats.Tokens`.
- **e2e**: none in this project.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope

## Solution Overview
- Approach A (chosen over a before/after delta and over per-stage `stat()` methods):
  an explicit `tokens int` parameter on `Pipeline.stageRun`. Each stage's count is taken from the stage that
  spent it, never derived by subtracting one cumulative total from another. Subtracting is exactly the accident
  the issue is about.
- Durations are not expected to sum to `stats.duration_ms`: setup and the gaps between stages fall outside every
  stage. Tokens do sum, because nothing spends tokens outside a stage.

## Technical Details
```go
type StageRun struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
	Tokens     int    `json:"tokens"`
	Executor   string `json:"executor,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
}

func (p *Pipeline) stageRun(name string, st *prompt.Stage, took time.Duration, tokens int) finding.StageRun
```

| stage | value passed | why |
|---|---|---|
| find | `rep.Stats.Tokens` after `f.report` | exactly the agents' sum, retries included |
| synthesis | `out.Stats.Tokens` | `synthesizer.run` sets it to `dispatch`'s sum across attempts |
| verify | `int(v.tokens.Load())` | `out.Stats.Tokens` is cumulative; `v.tokens` sums every group, failed ones included |

Unchanged edge cases:
- `--no-synthesis` / `--no-verify`: the stage has no entry.
- Verify with no groups: entry present, `tokens: 0`, no executor.

## What Goes Where
- **Implementation Steps**: code, tests, site, skill references, rules.
- **Post-Completion**: the PR and the maintainer's plugin version bump.

## Implementation Steps

### Task 1: Add StageRun.Tokens and plumb it through the pipeline

**Files:**
- Modify: `app/finding/report.go`
- Modify: `app/pipeline/pipeline.go`
- Modify: `app/pipeline/pipeline_test.go`
- Modify: `app/pipeline/synthesize_test.go`
- Modify: `app/pipeline/verify_test.go`

- [x] add `Tokens int \`json:"tokens"\`` to `StageRun` after `DurationMS` (no `omitempty`)
- [x] `StageRun` godoc:
  - "how long it took, what it spent, and which runner it was resolved to";
  - "The find stage leaves the runner fields empty";
  - a `Tokens` comment: what the stage's CLI reported, failed attempts included; find is its agents' sum so the
    entries add up to `Stats.Tokens`; the per-executor caveat by reference to `SourceStat.Tokens`
- [x] `stageRun` gains `tokens int`, and its godoc gets one line about it
- [x] find literal: `Tokens: rep.Stats.Tokens`, with a comment that it is exactly the agents' sum, so the entries
      add up to the run total
- [x] synthesis: pass `out.Stats.Tokens`
- [x] verify: pass `int(v.tokens.Load())`, with a comment that `out.Stats.Tokens` is the running total
- [x] add a test helper `stageTokens(rep) map[string]int` plus a sum check against `rep.Stats.Tokens`, if it
      keeps the assertions short
- [x] extend existing tests:
  - `TestPipeline_Run_stats`: `Stages[0].Tokens == 10` (7 + 3), and it equals `rep.Stats.Tokens`;
  - `TestPipeline_Run_recordsTheStageRunner`: give each mocked result distinct tokens (bugs 100, impl 40,
    synthesis 200, verify 25), then assert per entry and that the sum equals `rep.Stats.Tokens`;
  - `TestPipeline_Run_synthesis` merge subtest: the synthesis entry is 200, find is 140;
  - `TestPipeline_Run_verify` first subtest (one merged group): give the find agents nonzero tokens, so a
    cumulative value would fail the assertion, and expect the verify entry to be 25
- [x] run `go test ./app/...`: all pass

### Task 2: Edge-case tests for per-stage tokens

**Files:**
- Modify: `app/pipeline/synthesize_test.go`
- Modify: `app/pipeline/verify_test.go`

- [x] new `TestPipeline_Run_synthesis` subtest, synthesis retry:
  - the first synthesis attempt fails with 100 tokens and the second succeeds with 150, so the entry is 250,
    find is 140, and the sum holds;
  - the runner counts ONLY requests containing `synthesisMarker` and answers find agents via `"lens <name>"`,
    as `h.runner` does. Under `Pipeline.Run` the find agents use the same factory, so a global call counter
    would treat a find agent as the first synthesis attempt.
- [x] both custom runners (synthesis retry and per-group verify) follow `h.runner`'s order: they check the stage
      marker (`synthesisMarker` / `verifyMarker`) BEFORE the `"lens <name>"` match, since a stage prompt can
      contain an agent's lens name. Any state a runner keeps is goroutine-safe (atomic or mutex): find agents
      run concurrently, and so do the verify groups.
- [x] new `TestPipeline_Run_verify` subtest, with several groups and one failing:
  - set `h.cfg.NoSynthesis, h.cfg.NoVerify, h.cfg.VerifyGroupBy = true, false, groupBySource` explicitly
    rather than relying on the harness default. With synthesis on, the findings and their `sources` would come
    from the synthesis fixture, and the groups would not come out as `bugs-1` / `impl-1`;
  - one finding per agent, so there are two groups (`groupBySource` skips the thin-group merge);
  - a custom runner answers find agents via `"lens <name>"`;
  - for `verifyMarker` prompts it branches on the finding id in the prompt (`bugs-1` vs `impl-1`):
    one returns `{Tokens: 7}` with an error, the other valid verdicts with `Tokens: 10`;
  - find agents spend nonzero tokens; expect the verify entry to be 17 and the sum to hold.
- [x] `--no-synthesis` and `--no-verify` subtests: the skipped stage has no entry, and the remaining entries still
      sum to `rep.Stats.Tokens`
- [x] new `TestPipeline_Run_verify` subtest for verify with nothing to check:
  - `NoVerify=false`, and both agents return `findingsJSON()`;
  - the verify entry is present with `Tokens == 0` and an empty `Executor`;
  - no existing `Pipeline.Run` test covers this; the only one is component-level, at `verify_test.go:451`.
- [x] run `go test ./app/pipeline/...`: all pass

### Task 3: Rules and documentation

**Files:**
- Modify: `.claude/rules/pipeline.md`
- Modify: `site/docs.html`
- Modify: `site/reference.html`
- Modify: `.claude-plugin/skills/revmux/references/output.md`
- Modify: `plugins/codex/skills/revmux/references/output.md`

- [x] `.claude/rules/pipeline.md` Token accounting, with semantic line breaks:
  - per agent, per stage and summed per run, and stop there;
  - a stage's count comes from the stage that spent it (`dispatch` across attempts, `verifier.tokens` across
    groups), never from subtracting cumulative totals;
  - find's count is its agents' sum
- [x] `site/docs.html` sample (~:868):
  - `"tokens"` after `duration_ms`: find 141200, synthesis 25270, verify 18450, summing to the sample's 184920
    (revised in review: the sample carries a `confirmed` verdict, so it gained a verify entry, and synthesis
    dropped from the planned 43720 to keep the run total that README and `site/index.html` also state);
  - the agents list shows all four roster rows, summing to find's 141200 (added in review);
  - one or two sentences beside it:
    - the semantics;
    - that the list is `stats.stages` in the report, and top-level `stages` in `manifest.json`, while each
      stage snapshot lists only the stages finished when it was written;
    - a clause that counts differ by executor, and find's adds up its agents' counts, which may mix
      claude, codex and agy, so stage counts compare only within one executor
- [x] both `references/output.md` (~:35): the same sample and the same prose, byte-identical between the two trees
- [x] `site/reference.html:443` stats row: "per-stage duration, tokens and the runner each resolved to".
      Name both locations (`stats.stages` in the report and stage snapshots, top-level `stages` in
      `manifest.json`): in the row if a short clause fits, otherwise in the `<p class="small">` note right below
      the table (~:447). The maintainer asked for both to be named, so it must land in one of the two.
- [x] `diff -r .claude-plugin/skills/revmux/references plugins/codex/skills/revmux/references` is empty
      (and the same for `scripts/`)

### Task 4: Verify acceptance criteria
- [x] every `StageRun` carries `tokens` with no `omitempty`; find equals its agents' sum; the sum invariant holds
      (verified: `report.go` tag has no `omitempty`; `TestPipeline_Run_stats` asserts find == 7 + 3 and
      `assertStageTokensSum` runs across the stats, synthesis-retry, failed-group, skipped and empty-verify subtests)
- [x] `--no-verify` has no verify entry; an empty verify has an entry with no executor (unchanged)
      (verified by the `--no-verify` and nothing-to-check subtests in `verify_test.go`)
- [x] `make test` (script tests + race + coverage): all packages ok, both script tests pass, total 93.7%
- [x] `make lint` — PARTIAL, golangci-lint half NOT run: neither `golangci-lint` nor `shellcheck` is installed on
      this machine. Ran instead: `go vet ./app/...` (clean), `gofmt -l app/` (clean), and
      `.github/scripts/check-sentinel-helpers.sh` (passes). No `.sh` file changed on this branch. CI must gate it
- [x] `make build` and a quick look at the JSON shape (`stats.stages[].tokens` present) with no real model call
      (build ok; a throwaway marshal of `Stats` gave
      `"stages":[{"name":"find","duration_ms":5,"tokens":10},{"name":"verify","duration_ms":1,"tokens":0}]`)

### Task 5: [Final] Update documentation
- [x] README: no change (it shows only `"stages": [...]`); confirm
      (confirmed: the report sample shows `"stages": []` with no per-entry fields, and no prose describes a stage entry)
- [x] CLAUDE.md: no new pattern expected; confirm
      (confirmed: its one `StageRun` statement, that it records the runner resolution, still holds; the stage-schemas
      keep-in-sync bullet already names the sites a `Report.JSON` shape change touches, and those were updated in Task 3)
- [x] move this plan to `docs/plans/completed/` (skipped - the harness moves the plan after all phases finish)

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**External**:
- open the PR against `umputun/revmux` referencing #62
- the plugin version bump is maintainer work (CLAUDE.md), so the PR does not touch it
- optional: re-run a real round and confirm `stages[].tokens` matches the snapshot subtraction the issue describes
