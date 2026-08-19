# Add agy (Antigravity) as a third peer executor, with runner-combination selection

## Overview

- Add the `agy` CLI (Google Antigravity, verified against 1.1.14) as a third peer executor
  alongside `claude` and `codex`: a third entry in the `model:` binary vocabulary, a new
  executor in `app/executor/`, and two new shipped profiles — `agy-only` (whole review on agy)
  and `trio` (one full-coverage finder per binary: claude, codex, agy).
- Add one new CLI flag, `--runners <binary>[,<binary>...]`, which filters the resolved roster
  by binary so a caller can pick a combination at the CLI: `--profile trio --runners claude,agy`
  runs the claude+agy pair. The flag selects among runners the profiles resolved; it never
  builds one, so the "one `model:` string is the whole runner selection" rule stands.
- agy is a peer source: it runs in parallel with the other finders and its findings go through
  the same synthesis and verification. No agy-specific prompt files; lens text stays
  executor-agnostic.

## Context (from discovery)

Verified agy 1.1.14 behavior, measured on this host (captures referenced below must be
re-recorded into `app/executor/testdata/` during Task 1):

- Non-interactive form: `agy --print "<prompt>" --output-format stream-json --sandbox
  [--model <id>] [--effort low|medium|high] [--json-schema <schema>] [--disable-slash-commands]`.
  The prompt is the **value of `--print`** (argv). Piping the prompt on stdin while passing
  bare `--print` made the flag consume the next argv token as the prompt (measured: it answered
  a question about `--output-format`). `--input-format stream-json` exists and may allow
  stdin-fed turns — verify in Task 1 whether the prompt can reach agy on stdin, because claude
  uses stdin specifically for the Windows 8191-char argv cap; if argv is the only way, record
  that as an accepted agy limitation in `.claude/rules/executor.md`.
- Output is agy's **own NDJSON dialect**, not claude stream-json. Three event kinds observed:
  - `{"event":"init","conversation_id":...,"init":{cwd, tools[], permission_mode}}` —
    print mode showed `permission_mode: "always-proceed"` without any flag.
  - `{"event":"step_update","step_update":{conversation_id, step_index, state, step_type,
    text_delta?, duration_seconds?, usage?}}` — `step_type` observed: `user_input`,
    `checkpoint`, `agent_response`, `finish`. Tool-using runs will carry more; capture them.
  - `{"event":"result","result":{conversation_id, status:"SUCCESS", response, num_turns,
    duration_seconds, structured_output?, json_schema?, usage:{input_tokens, output_tokens,
    thinking_tokens, cache_read_tokens, total_tokens}}}` — terminal event.
- `--json-schema` works like claude's: the result carries a **pre-parsed `structured_output`
  object**. Read it; never scrape the `response` string. Unlike claude, agy accepts a schema
  file path as well as inline JSON — pass inline anyway for symmetry with claude.
- The result carries **no actual-model field** (claude has `modelUsage`); the manifest records
  requested model only for agy. Say so in godoc where the claude executor reads the model back.
- `agy models` lists ids like `gemini-3.7-flash-{high,medium,low}`, `gemini-3.1-pro-{high,low}`,
  `claude-sonnet-4-6`; effort exists both as `--effort` and baked into some model-id suffixes.
  revmux passes `--effort` only when the `model:` string carried one, same as the other binaries.
- `--print-timeout` defaults to **5m0s** — an agy-internal wait that would kill a long review
  under revmux's own 20m hard timeout. The executor must pass a `--print-timeout` derived from
  (or safely above) the hard timeout, or the maximum the flag accepts; verify in Task 1.
- No `--disallowedTools`, `--no-session-persistence`, or `--include-partial-messages`
  equivalents in `--help`. `--sandbox` (terminal restrictions) is passed always — revmux never
  lets an agent write; the prompt states the constraint too, as with the other executors.
- Files/components involved: `app/executor/` (new `agy.go`, `agystream.go` or equivalent,
  tests, fixtures), `app/prompt/runner.go` (binary vocabulary), `app/config.go` (`--runners`),
  composition root in `app/main.go`/`app/config.go` (executor construction), `app/pipeline/`
  (roster filter application point), `app/prompt/defaults/prompts/profiles/` (two new profiles),
  plus the documentation and skill trees named per task below.
- Patterns to follow: `Codex`/`Claude` executors embed the unexported `proc` struct and supply
  only `args()` and stream parsing; model/effort live on the per-run `Request`; raw bytes tee to
  `Request.RawOutput` **before** parsing; both timers come from the injected clock; process-group
  teardown is shared. `.claude/rules/executor.md` and `.claude/rules/testing.md` govern all of it.

## Development Approach

- **Testing approach**: this repo's own convention overrides the no-unit-test default —
  `.claude/rules/testing.md` requires fixture-driven tests with mocked runners and an injected
  clock, and **no test may spawn a real model**. Follow the existing executor test patterns
  (recorded fixtures in `app/executor/testdata/`, fake runner, clock advanced by the test).
- Complete each task fully before moving to the next.
- Make small, focused changes; commit per task on the feature branch.
- **CRITICAL: `make lint` and `make test` must pass before starting the next task** —
  `make lint`, never bare `golangci-lint` (the shellcheck half is part of the gate).
- **CRITICAL: update this plan file when scope changes during implementation.**
- Zero VCS dependency, no new Go dependencies, no changes to the three finding schemas.

## Testing Strategy

- Executor: recorded agy fixtures + fake runner + injected clock, mirroring the existing
  claude/codex executor tests (this is the repo's established pattern, not new infrastructure).
- Runner vocabulary and `--runners` filtering: table tests beside the existing
  `parseRunner`/roster tests.
- Profile contracts: the existing derived contract tests (`TestDefaults_SeverityContract`,
  `TestDefaults_WhatNotToReportContract`, prior-round block test) must pass for the two new
  profiles **without adding exemptions** — both new profiles carry the shared bar byte-identically.
- Cross-platform: `GOOS=windows GOARCH=amd64 go build ./...` must pass (executor package rule).

## Progress Tracking

- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Update plan if implementation deviates from original scope

## Implementation Steps

### Task 1: Capture real agy fixtures and pin the open CLI questions

- [x] capture real agy runs into `app/executor/testdata/` (launch agy directly from a shell —
      it is not the host tool, so the nested-launch block that applies to `claude` does not
      apply; verified working in this environment): a trivial text run, a `--json-schema` run
      whose result carries `structured_output`, a tool-using run (a prompt that makes agy run
      `ls` or read a file, to record the tool-call `step_update` shapes), and an error run
      (e.g. `--model no-such-model`) recording both stdout and stderr and the exit code
      (recorded against agy 1.1.15: `agy-clean.jsonl`, `agy-stdin-clean.jsonl`,
      `agy-schema.jsonl`, `agy-tools.jsonl`, `agy-toolerror.jsonl`, `agy-error.jsonl` +
      `agy-error.err.txt`, `agy-timeout.jsonl`)
- [x] verify and record: can the prompt reach agy on stdin (`--input-format stream-json` or
      otherwise), or is argv the only path; what `--print-timeout` accepts as a maximum and
      how it interacts with a run longer than 5m; whether agy sets a nested-session guard
      env var in its children (run `agy -p "print your environment variables that mention
      antigravity or agy" --sandbox` or inspect `env` from a tool-using run) that the executor
      must strip the way `CLAUDECODE` is stripped
      (stdin works: `--print "" --input-format stream-json` +
      `{"event":"user","message":{"role":"user","content":"<prompt>"}}`; `--print-timeout`
      accepts `100000h`, expiry is exit 1 + result ERROR "timeout waiting for response";
      no nested-session guard — agy launches fine with all five `ANTIGRAVITY_*` vars set)
- [x] verify what a rate-limit / quota error looks like if cheaply reproducible (a bad model id
      error at minimum); note the `status` value and stderr shape for a failed run
      (bad model: exit 1, single `result` event on stdout with status ERROR and the model list
      in `error`; stderr empty in every capture — rate-limit not cheaply reproducible, noted)
- [x] write the findings as a "Verified agy CLI behavior" section in
      `.claude/rules/executor.md`, same register as the claude and codex sections: measured
      facts only, each one stated with what it costs to rediscover
- ➕ measured fact Task 3 must honor (contradicts the Technical Details bullet below):
  `result.status:"ERROR"` can coexist with exit 0 and a complete `response` — a mid-run tool
  error propagates into the terminal result while the run still answers correctly
  (`agy-toolerror.jsonl`). Outcome mapping must weigh exit code and answer presence, not the
  status string alone; see the rules section for the capture.

### Task 2: agy stream parser

- [x] add the agy NDJSON decoder in `app/executor/` (own file beside `stream.go`), parsing
      `init` / `step_update` / `result` events; unknown `event` values are ignored, not errors
      (`app/executor/agystream.go`, `agyStream` fed line-by-line the way `readLines` will)
- [x] map events to the existing emit vocabulary the way the claude parser does: agent text
      from `agent_response` deltas/steps, tool activity from tool-call step types (a step whose
      command cannot be recovered follows the codex rule — dropped, not reported by name),
      result text/`structured_output`/`usage` from the terminal `result` event
      (deltas accumulate per step and emit once at completion; thinking-only steps emit nothing;
      a schema-forced JSON answer is suppressed from activity the way claude's StructuredOutput
      tool never reaches progress; tool dispatch only, completions not doubled)
- [x] flatten multi-line text blocks rather than truncating (decoder emits text; width belongs
      to the renderers)
- [x] read token counts only from the result `usage`; map agy's fields onto the existing usage
      shape (`thinking_tokens` and `cache_read_tokens` fold in wherever claude's equivalents go)
      (input + output + cache_read; thinking already inside output_tokens, not added again)
- [x] tests against the recorded fixtures: event mapping, structured_output extraction, usage,
      a stream that fails to parse is a degraded source, not a crash
      (`agystream_test.go`, internal package test over all seven `agy-*.jsonl` captures plus
      derived truncated / garbage / command-stripped cases)

### Task 3: agy executor

- [x] add `Agy` executor in `app/executor/agy.go` embedding `proc`, supplying `args()`
      (`--print <prompt>` or the stdin path Task 1 verified, `--output-format stream-json`,
      `--sandbox`, `--disable-slash-commands`, `--model`/`--effort` from the per-run `Request`
      only when set, `--json-schema` with the running stage's schema inline, `--print-timeout`
      per Task 1) and the agy parser
      (stdin path: `--print ""` + the stream-json user turn built by `agyStdinMessage`;
      `--print-timeout` is hard timeout + 1m, or `24h` with the hard timeout disabled)
- [x] tee raw bytes to `Request.RawOutput` before parsing; the archive tee stays `<agent>.jsonl`
      (agy output is NDJSON)
      (shared `proc` tee; `finder.rawName` already defaults every non-codex executor to `.jsonl`)
- [x] idle watchdog: touch on every stdout line and stderr line; from the Task 1 captures,
      determine whether a long single answer streams `step_update` deltas throughout or goes
      silent while composing (the claude blind-window problem) — if blind, document the exposure
      in `.claude/rules/executor.md` and rely on the hard timeout; do not invent a third
      liveness source that does not exist
      (schema-forced answers are blind — documented in the rules' agy section during Task 1;
      no third source added)
- [x] outcome from the result `status` (only `SUCCESS` observed; treat any other status as a
      failed attempt carrying the response as the diagnostic); error/limit patterns tiered
      retry → limit → error, checked only against the tail and only on non-zero exit, per the
      existing codex rules; skip pattern checks on a canceled context
      (per the ➕ fact below the mapping weighs the exit code, never the status string: exit 0
      succeeds even under `status:"ERROR"`; the tiering is the codex logic hoisted into the
      shared `proc.classifyFailure`, with the result `error` string as agy's diagnostic)
- [x] child environment: strip `CLAUDECODE` (revmux is often launched from a claude session and
      agy can proxy claude models) plus whatever nested-session guard Task 1 found for agy;
      respect the existing `--preserve-anthropic-api-key` behavior consistently
      (all shared `proc.childEnv`; Task 1 found no agy guard to strip)
- [x] structured output: read the result's `structured_output` object; never scrape `response`;
      absence of structured output where a schema was sent is a degraded source
- [x] tests: fake runner + fixtures + injected clock — happy path, schema path, error path,
      idle timeout fires when the fixture ends in a block (per the testing rule: a fixture that
      simply ends is EOF, not a stall), process-group teardown reuses the shared tests
      (`agy_test.go`: args, stdin message shape, clean, schema, schema-without-output,
      tool-error-exit-0, bad model, pattern tiers, truncated, raw tee, idle timeout)
- [x] `GOOS=windows GOARCH=amd64 go build ./...` passes

### Task 4: Runner vocabulary and executor wiring

- [x] add `agy` to the binary vocabulary in `app/prompt/runner.go` (`parseRunner` is the only
      place a `Runner` is built; `Runner.or` continues to refuse carrying a model across
      binaries); effort vocabulary is shared — confirm agy's `low|medium|high` matches the
      existing effort constants and extend nothing unless it does not
      (the vocabulary lives in `app/prompt/roster.go`'s `executors` slice, which `parseRunner`
      checks; agy's efforts are a subset of the shared set, nothing extended)
- [x] runner tests: `agy`, `agy/gemini-3.1-pro-low`, `agy/gemini-3.7-flash-high:high`,
      cross-binary inheritance refusal to/from agy
- [x] composition root: construct the agy executor where claude and codex are constructed and
      register it in the injected factory under the `agy` binary name; reuse the existing
      binary-path/timeout knob pattern exactly — add an agy knob only where claude/codex each
      already have one, with matching INI template entries in `app/defaults/config` and the
      runtime-knob list in `.claude/rules/config.md` only if such knobs exist for the other two
      (claude/codex have no per-binary path or timeout knobs — names are hardcoded in each
      executor, timeouts are the shared `idle-timeout`/`hard-timeout` — so no knob, no INI
      entry, no rules edit)
- [x] verify `revmux config` reports the `executor` vocabulary from the same constants
      `parseRunner` validates against, so `agy` appears automatically; wire it if it does not
      (already wired: `introspect.go` reads `prompt.Executors()`; verified live —
      `["claude","codex","agy"]`)

### Task 5: `--runners` combination flag

- [x] add `Runners []string` to the options struct in `app/config.go`
      (`long:"runners" no-ini:"true"`, description in the existing flag style), validated
      against the executor vocabulary at load — an unknown binary is a load error naming the
      valid set
      (validation lives in `prompt.Profile.Restrict`, beside the `executors` slice it checks)
- [x] apply the filter where the roster is resolved: drop roster agents whose resolved binary
      is not in the set; a filter that empties the roster is a load-time error naming the
      profile and the surviving set (never a silent zero-agent run)
      (`Restrict` stores the filter on the profile copy; `Roster` drops excluded agents and
      errors naming the profile, the filter, and the binaries the roster runs)
- [x] stages follow the filter: a stage whose resolved binary is excluded falls back to a bare
      `Runner` on the first listed binary (its own default model and effort), and
      `finding.StageRun` records the resolution as always, so the archive says what actually ran
      (post-resolution check in `Profile.Stage`, after the three layers apply)
- [x] the `--lenses` synthesized agent resolves through the same filter (its inherited profile
      `model:` may name an excluded binary — same fallback)
- [x] record the applied filter in `manifest.json` (a new prompt input needs a matching record,
      per the keep-in-sync rule for reflection agents)
      (`runners` field, `omitempty` so a no-flag manifest is byte-identical to before)
- [x] tests: filter on a mixed roster, empty-roster error, stage fallback, no-flag behavior
      byte-identical to today
      (`TestProfile_Restrict` in `roster_test.go`, `TestRun_runnersFilter` in `main_test.go`,
      `TestParseArgs_runnersAcceptCommasAndRepetition` in `config_test.go`)

### Task 6: `agy-only` and `trio` profiles

- [x] add `app/prompt/defaults/prompts/profiles/agy-only.md`, mirroring `codex-only.md`:
      profile-level `model: agy`, same roster shape, the shared `## Severity bar` and
      `## What not to report` blocks byte-identical to the other five/six copies
      (body copied from `codex-only.md` verbatim; profile-level model is
      `agy/gemini-3.1-pro-high:high` rather than bare `agy` — the shipped-roster test
      requires every spec to resolve a model and an effort)
- [x] add `app/prompt/defaults/prompts/profiles/trio.md`: three finder agents with identical
      lens sets, one per binary (`claude`, `codex`, `agy` in `model:`), named for the runner
      per the documented grill-me/expert exception (identical lens sets make the runner the
      only distinguisher); distinct `color:` per agent; a `stages:` block only if the default
      profile-level resolution is not what a mixed run should do — prefer the one-line
      profile-level `model:` for stages (claude) and no `stages:` block
      (profile-level `claude/opus:high`, no `stages:` block; codex and agy entries carry
      full runner strings per the same shipped-roster rule; colors cyan/magenta/green)
- [x] both new profiles pass the derived contract tests with **no new exemptions**
      (severity, what-not-to-report and prior-round contracts all pass; `agy` also added to
      the lens executor-agnostic ban list)
- [x] update the literal test inventories: profile name sets in `prompt_test.go` (twice),
      `initcmd_test.go`, `main_test.go`; count and enumerating message in `defaults_test.go`

### Task 7: Documentation sweep (README, site, rules)

- [x] README: shipped-profile table (+2 rows), install paragraph naming which binaries each
      profile needs (agy install path included), the "all five print JSON"-adjacent text only
      where the README already states what changed, flag mention only where README states flags
      (intro sentence also gains `agy --print`; README states no flag list, so no `--runners`)
- [x] `site/index.html`: profile table, the lens-names-in-prose profiles section if it
      enumerates profiles, install section if it names binaries
      (+2 table rows, "eight ship" → "ten ship"; prose and install name no binaries per
      profile; the meta/OG/JSON-LD descriptions enumerating "claude and codex" gained agy)
- [x] `site/docs.html`: profile table, prompt-tree diagram (+2 profile files), model-string
      section (binary vocabulary gains `agy`), `--runners` in the runtime-knob table with the
      reason attached, `revmux config` sample payload if the executor vocabulary appears in it
      (`--runners` is `no-ini` and the knob table is config-backed flags only, so it is
      documented in prose beside `--lenses` in the profiles section instead, reason attached;
      Model CLIs install list gains agy-only and trio)
- [x] `site/reference.html`: profile table (binaries column), flag table row for `--runners`,
      model-string / executor vocabulary section
      (the needs column's "both" rewritten as "claude + codex" — ambiguous with three binaries)
- [x] `site/llms.txt`: only if it states executor names or profile counts — check and update
      (it did both: summary lines, "eight shipped profiles", the model-string feature bullet)
- [x] `.claude/rules/prompts.md`: layout block (+2 profiles), model-string grammar section's
      binary vocabulary, profile examples if they enumerate binaries
      (also the orthogonality note: claude *and agy* have `--json-schema`)
- [x] `.claude/rules/executor.md`: confirm the Task 1 section landed and reads like the
      claude/codex sections (landed in Task 1, measured facts with rediscovery costs — verified)
- [x] CLAUDE.md: project-structure and keep-in-sync counts that name the profile set literally
      (e.g. "eight shipped profiles" in the model-grammar bullet becomes ten; the severity-bar
      bullet's five identical copies becomes seven; the what-not-to-report six becomes eight)
      (also: intro and `app/executor/` line gain agy, the adversarial-composing six becomes
      eight, the orthogonality `--json-schema` note gains agy)

### Task 8: Skill trees (both, kept identical)

- [x] `SKILL.md` in both trees: profile table (+2), the "the user says" mapping (a user asking
      for "agy", "antigravity", "gemini review" or a combo like "claude and agy" maps to
      `agy-only` / `trio` / `--runners`), executor enumeration anywhere it appears
      (three mapping rows; the intro — and the claude tree's frontmatter description — gain
      `agy --print`; a `--runners` paragraph lands beside the `--lenses` one in Step 3)
- [x] `references/invocation.md` in both trees: profile table (+2), the executor **count** in
      its Environment bullet (the count is the sync site the CLAUDE.md bullet exists for),
      `--runners` wherever flags are enumerated
      (Environment rewritten: an `agy` bullet plus per-profile needs — three single-binary
      profiles, trio needs all three, the other six still claude+codex; `--runners` in the flag
      table and a paragraph under Choosing a profile; the stale "twelve prompt files" under
      `revmux init` corrected to twenty-five while touching the file)
- [x] any other `references/` file enumerating binaries or profiles — grep both trees for
      `codex` and update the enumerations that are about executors
      (`output.md`'s agent-tee line notes agy's NDJSON shares the `.jsonl` extension;
      `preflight.sh` and `launch-revmux.sh` header comments name agy — the scripts themselves
      are data-driven off `revmux config` and needed no logic change)
- [x] `diff -r` of the two trees' `references/` and `scripts/` comes back empty; only
      `SKILL.md` differs (verified after the sync)

### Task 9: Verify acceptance criteria

- [x] `--profile agy-only`, `--profile trio`, and `--profile trio --runners claude,agy` all
      load without error (config/load-path check; no real model run)
      (verified via a throwaway `prompt` package test — Load + Profile + Restrict + Roster
      over the shipped defaults; the claude,agy filter on trio survives as exactly that pair)
- [x] `revmux config` reports `agy` in the executor vocabulary and both new profiles with
      their rosters
      (live run: `vocabulary.executors` is `["claude","codex","agy"]`; `agy-only` lists the
      four-lens roster, `trio` lists `claude`/`codex`/`agy`)
- [x] `make lint` passes (the full gate, including shellcheck) — exit 0
- [x] `make test` passes (race + coverage) — exit 0, 93.7% statements
- [x] `GOOS=windows GOARCH=amd64 go build ./...` passes
- [x] `diff -r` skill-tree check passes (`references/` and `scripts/` identical between
      `.claude-plugin/skills/revmux/` and `plugins/codex/skills/revmux/`; only `SKILL.md` differs)
- [x] all commits on the feature branch, none on master; do not push
      (8 commits ahead of master, 0 behind, worktree clean, nothing pushed)

## Technical Details

- **agy result → executor result mapping**: `status != "SUCCESS"` or non-zero exit → failed
  attempt; `response` string is the prose answer; `structured_output` is the stage payload;
  `usage.input_tokens`/`usage.output_tokens` are the counts (fold `thinking_tokens` into
  whatever bucket claude's thinking lands in; if claude's usage has no such bucket, report
  input/output only and leave the rest to the raw tee).
- **`--runners` semantics, precisely**: parse each entry with the same binary-vocabulary check
  `parseRunner` uses (bare binary names only — `--runners claude/opus` is a load error; the
  flag filters, `model:` strings select). Filter applies after profile resolution and before
  the roster reaches the pipeline; order of surviving agents is unchanged; the first listed
  binary is the stage fallback.
- **No new Go dependencies.** The agy decoder is `encoding/json` over lines, like the others.
- **Fixture hygiene**: strip nothing from captures except genuinely secret values; the
  fixtures are the executor tests' ground truth, per `.claude/rules/testing.md`.

## Post-Completion

*No checkboxes — informational.*

**Manual verification:**
- One real end-to-end run per new profile against a small diff, watched in the TUI, plus
  `--profile trio --runners claude,agy` — confirms stagger, watchdog and archive behavior
  against the live CLI, which no fixture can.
- Check archive: `<agent>.jsonl` tee for the agy agent replays through the decoder;
  `manifest.json` carries the runners filter and requested model.

**External follow-ups:**
- Push and open the PR when the user is ready (delivery decision was local-branch-only).
- Consider proposing `agy` support upstream to umputun/revmux once proven here.
- A future round may add an agy agent to `comprehensive` once real-run quality is judged
  (deliberately out of scope now).
