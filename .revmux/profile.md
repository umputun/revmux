# Project profile — revmux

## What this is

A standalone Go CLI, personal tooling, one maintainer. It spawns and supervises parallel `claude
--print` and `codex exec` subprocesses, merges what they report, and returns findings as JSON on
stdout. No server, no network service, no daemon, no user data, no persistence beyond the run
archive under `.revmux/tasks/`.

It is driven by a caller model rather than typed by a person, so its output shape and its
configuration are machine-readable by design.

## What a real failure looks like

Concrete, in rough order of how much it costs:

- **A review that silently reviews the wrong thing** — the wrong scope, a prompt composed from the
  wrong precedence layer, a stage resolved to a runner the manifest does not record.
- **An archive that cannot reconstruct the run** — a composed prompt not written, a stage snapshot
  missing, a manifest naming a model that did not run. The archive exists so a later reflection
  agent can ask which lens text raised a finding; one that cannot answer that is broken even when
  the review it recorded was fine.
- **Attribution that inflates confidence** — `sources` carrying anything but distinct agent names,
  or one agent counted twice. The cross-source boost is the whole reason two executors run.
- **Supervision that does not supervise** — a stalled agent the watchdog misses, a retry that lands
  in the same rate-limit window, a kill that leaves a child alive.
- **Anything but the report on stdout** — a status line, a warning or a banner there makes
  `revmux > findings.json` unparseable, which is the primary way it is used.
- **Documentation that states a flag, exit code, JSON field or archive path the binary does not
  have** — `site/`, `README.md` and both skill trees are executed by agents without checking.

## Blast radius

One maintainer, one machine. A crash costs one review, which is re-run. Nothing here is a security
boundary in the usual sense and no untrusted input reaches it, except a `.revmux/` checked into a
repository under review — which is executable configuration and is treated as such in the skill,
not in the binary.

What actually costs is a wrong review believed, or an archive that cannot be audited afterwards.
Weigh findings against those two, not against uptime.

## Where the project's rules live

`CLAUDE.md` at the repo root is the authority, and `.claude/rules/*.md` carry the per-subsystem
detail — `executor.md`, `pipeline.md`, `prompts.md`, `tui.md`, `config.md`, `testing.md`, each
scoped by `paths:` front matter. `CLAUDE.md` also holds a long keep-in-sync list naming every place
a flag, profile, lens, roster key or schema change must land.

**A deviation from a documented rule there is always worth reporting**, including a change that
lands in one of the keep-in-sync sites and not the others.

## The reporting bar

Findings must be material, not merely true.

Noise here:

- **Anything the linter or the tests catch.** `golangci-lint` runs 45+ linters including `revive`,
  `gocritic`, `gosec`, `wrapcheck` and `exhaustive`; tests run under `-race` at ~94% coverage. Both
  ran and passed before this review.
- **Style, naming and idiom preferences**, and "consider extracting this".
- **Duplication listed as deliberate below.**
- **"Add more tests"** as a bare suggestion. A new branch with no case pinning it is a finding; a
  coverage number is not.

Real here:

- A contradiction or a stale fact in `.claude/rules/`, `CLAUDE.md`, `site/` or either skill tree.
  Those are contracts a machine executes against, so a wrong statement is a defect rather than a
  wording nit. A wording *preference* in the same files is still noise.
- A change that makes a round un-auditable, or that widens the one carve-out in the
  archive-write-fails-the-run rule.

## Conventions that are deliberate — do not file these as defects

- **Zero VCS dependency.** No git library, no `git` subprocess, no repo walking anywhere in the
  binary. Agents run diff commands themselves; revmux substitutes a path. "Why not use go-git" is
  the rule, not an oversight.
- **Context variables expand to paths, never to file content.** Prompt composition stats the
  caller's files and never opens one.
- **The two skill trees carry byte-identical `references/` and `scripts/`.** `.claude-plugin/` and
  `plugins/codex/` duplicate on purpose — a plugin must be self-contained once installed. Only
  `SKILL.md` differs. A `diff -r` of the other two directories coming back empty is the invariant.
- **The severity bar and the "what not to report" block are duplicated byte-identical across
  shipped profiles.** Nothing composes them from one place, deliberately; three profiles carry
  their own variant because their review shape needs a different bar.
- **`app/archive` spells stage names and event kinds as string literals** rather than importing
  `app/pipeline`, to keep the artifact package free of the orchestrator. Same for `manifest.json`'s
  `finished_at`, decoded through a local partial struct.
- **`--permission-mode auto` with no Bash denylist.** Measured: `dontAsk` denies the `git diff` the
  whole design depends on. A `Bash(sed:*)` denial was tried and reverted. The read-only guarantee
  rests on the prompt, not on flags.
- **Everything private by default** — functions, methods, types and fields alike. Exported only for
  an out-of-package caller.
- **Mocks are moq-generated under `mocks/` and never hand-edited.**
- **One test file per source file**, `foo.go` → `foo_test.go`, never a third.

## Languages in play

Go is the binary. Markdown under `.claude/rules/`, `app/prompt/defaults/` and both skill trees is
instructions a model executes, so judge it as a contract rather than as prose. Shell scripts are
shellcheck-gated in CI where any output at all fails the job, including info-level findings. One
Python script under each skill tree aggregates the archive and has its own tests.
