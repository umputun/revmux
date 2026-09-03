---
paths:
  - "app/prompt/**"
---

## Prompts — profiles, lenses and composition

Everything that shapes a review lives in markdown; the INI config holds only runtime knobs.
The two must not overlap: a reviewer should be able to copy the prompt tree to another machine
and get the same review, and change a timeout without touching a prompt.

### Layout

```
prompts/profiles/comprehensive.md   focused.md   final.md   claude-only.md   codex-only.md   opencode-only.md   grill-me.md
prompts/profiles/expert.md
prompts/profiles/triage.md
prompts/synthesis.md   prompts/verify.md
lenses/bugs.md  impl.md  architecture.md  quality.md  docs.md  tests.md  comments.md  adversarial.md
lenses/grounding.md  precedent.md  thesis.md  antithesis.md  cost.md
```

The second lens line reads a filed item rather than a diff, and `triage` is the profile composing it.
Nothing else does: the two sets share the composer and nothing else, so a change to a code-review lens
cannot reach the panel and a change to a panel lens cannot reach a review.

Precedence, per file: `./.revmux/` > `~/.config/revmux/` > `go:embed` defaults.
Per-**file** fallback, not per-directory — overriding one lens must not orphan the rest.

There is no per-file CLI flag in that chain, unlike the runtime knobs in `config`.
`--profile` names a profile, not a path, and `--config-dir` relocates the whole tree;
neither points at an individual lens file.

`.md`, not `.txt`: these carry YAML front matter, so editors fold the metadata and highlight the body.

### Profiles declare the roster

A profile is roster front matter plus a body that is the shared preamble and severity bar.

```yaml
---
model: claude/opus:high
agents:
  - {name: bugs+impl,    lenses: [bugs, impl],            color: cyan}
  - {name: arch+quality, lenses: [architecture, quality], color: magenta}
  - {name: docs+tests,   lenses: [docs, tests, comments], color: green}
  - {name: adversarial, lenses: [adversarial], model: codex/gpt-5.6-sol:high, color: yellow}
---
```

The top-level `model` is the review's runner; a roster entry or a stage naming its own overrides it.

### One `model` string is the whole runner selection

`<binary>[/<model>][:<effort>]`, parsed by `app/prompt/runner.go` and nowhere else.
There is no `executor:` key and no `effort:` key in any prompt file.

```
claude                   the binary's own default model and effort
claude/opus:high         fully specified
codex/gpt-5.6-sol        effort falls back to the profile's, then the binary's
codex:high               the binary's default model at high effort
opencode/gpt-5.1         opencode CLI with a specific model
```

**The binary leads and is mandatory, which is what makes the value validate itself.**
Deriving it from the model name instead would need a catalog of vendor model names inside revmux, and the
day either vendor ships a name outside the pattern a valid profile stops loading until revmux cuts a
release — a hard external dependency traded for saving eight characters.
It also lets `model:` mean "that binary, whatever it defaults to", which two fields expressed as an
`executor:` with no `model:` beside it.

**The three are one field because they are not independent.**
A model is a model *of a binary* — `opus` means nothing to codex — so separate keys let a file state a
pairing that cannot run, and every layer that inherited one without the other recreated it.
That is not hypothetical: the `--lenses` override took the profile's `model` while forcing `executor` to
claude, and under `codex-only` built a claude agent asked for `gpt-5.6-sol`.
`Runner.or` therefore refuses to inherit a model across binaries, and carries only the effort, which
belongs to neither model.

Parsing splits on the **first** `/`, so a model whose own name carries one survives, and on the **last**
`:`, whose suffix must be a real effort rather than being folded into the model name — `:hgih` is a load
error, not a typo that silently becomes part of a model nobody checks.

### Agent color

`color` is optional per roster entry and is the one presentation key in an otherwise review-shaping file.
It lives here because the roster is the only place agent names exist, and it does not change the review —
copying the prompt tree to another machine still produces the same findings.

Two accepted forms, both normalized at load to a string a renderer hands straight to lipgloss —
an ANSI index `"0"`-`"15"` or the original `#RRGGBB`. `app/prompt` never imports lipgloss:

- a **name** from the ANSI-16 set — `black`, `red`, `green`, `yellow`, `blue`, `magenta`, `cyan`, `white`
  and a `bright-` variant of each. lipgloss has no name lookup, so revmux maps names to indices `0`-`15`
  itself, and that mapping is the point: an ANSI index is drawn from the user's own terminal theme, so
  `red` is the red he already reads everywhere else
- a `#RRGGBB` hex string, for a specific shade. lipgloss downsamples it on terminals that cannot show it,
  and it ignores the terminal theme by definition — that is the trade the author is making by writing one

A raw numeric index is deliberately not accepted: `color: 12` says nothing to the person editing the file,
and the name it corresponds to always exists. Anything outside the two forms is a load-time error like
every other bad front-matter value.

Omitted, the color is assigned from a fixed palette of those names **by roster position**, not by hashing it:
a reviewer watching two runs of one profile should see the same agent in the same color, and a hash makes
that depend on a name he might edit.

The resolved color travels on `AgentSpec`, so the TUI and the plain `--no-tui` renderer prefix an agent
identically. A color chosen inside `app/ui` would exist in one renderer only.
`--lenses` synthesizes a single agent with no front matter, so it takes the first palette entry.

`revmux config` reports the authored form where one was given, so a caller reads back `cyan` rather than
`"6"`. Keep the authored value alongside the normalized one if a single field cannot serve both.

`synthesis.md` and `verify.md` name **no runner at all**: they are text, and which binary reads it is the
profile's to say. A `model:` in a stage file is still honored if someone writes one, but the shipped pair
carry only a `description:`, so a profile's own `model` reaches them with nothing to override.

### A profile's runner covers the whole review

The stage files are one per tree, so what they declare cannot be a per-profile answer.
When they declared one, a `codex-only` profile was honest about the find stage and silently false about the
other two: the round ran four codex finders and then synthesized on claude, and the only way to change that
was to override `prompts/synthesis.md` for **every** profile at once, which stops `--profile` switching the
review.

So the stage files name no runner and the profile's `model:` reaches everything — the roster, the agent
`--lenses` synthesizes, and both stages. `codex-only` is one line as a result, which is the point: the
version of this that kept `executor:` on the stages made the shipped profile state one fact three times,
and the repetition was load-bearing for a reason nothing in the file explained.

The optional `stages:` block is for a **deliberately mixed** run, and each stage is named on its own, so
two stages can take different models:

```yaml
stages:
  synthesis: claude/opus:high
  verify:    claude/sonnet:low
```

The body always stays the shared file: there is one synthesis job, lens and stage text is
executor-agnostic by the rule above, and the codex output contract is injected by the executor rather than
authored into a variant file.
A profile pointing at a *different* stage prompt would duplicate sixty lines of body to change one word,
which is the "shared text belongs in the profile body" rule inverted.

Resolution order per stage, highest first: the profile's `stages:` entry, then the stage file's own
`model:` if it has one, then the profile's top-level `model:`.
A `stages:` entry naming only a binary still falls back through `Runner.or` for the rest, and the three
layers are applied in turn rather than collapsed first: fold the stage file into the profile before
applying the override and an override switching back to the profile's binary finds its model already
replaced by an incompatible one, and runs with none at all.
Resolution is `Profile.Stage(set, name)`, and `app/pipeline` reads it rather than `Set.Stage` — the latter
answers what the file says, which is a different question from what this run will do.
Validation happens at load like everything else, and it is against a **closed set** — `overridableStages`,
the stages the pipeline dispatches — never against what happened to load.
The tree glob turns any `prompts/*.md` into a stage, so a project file beside `synthesis.md` and `verify.md`
would otherwise be nameable as an override that then silently never runs, which is the ignored-key failure
this package rejects everywhere else.
`prompt.Stages()` is that set, and `revmux config` reads its `catalogStages` from it rather than declaring a
second copy, so the catalog cannot advertise a stage the loader would refuse.

`Profile.Runner()` is exported for the same class of reason: the profile's own `model:` is what the
`--lenses` replacement runs on, and nothing outside the package could otherwise tell which binary that
needs — the reported roster and stages may all name another one. `revmux config` carries it as
`profiles[].runner`, and `preflight.sh` reads it **only** for a `--lenses` invocation. Checking it on an
ordinary run turns a review revmux can run into a preflight failure, which is worse than the gap it
closes.
Its **value** needs no separate check: `parseRunner` validated the binary and the effort when the string
was read, which is why `Stage.validate` and the vocabulary checks inside `AgentSpec.validate` are gone
rather than kept as defence — a second check that cannot fire is unreachable code claiming to guard.
What `parseProfile` does add is a rejection of an authored-but-**empty** override: the key is present, so
an empty value states nothing while still counting as an override, and a silent no-op is the failure this
package refuses everywhere else.

**A profile does not colour a stage.** Both renderers resolve a name the roster does not carry through the
package-level `prompt.DerivedSpec`, a pure function of the name, and that purity is what keeps them from
disagreeing about a colour neither chose. Verify is not one process either — it fans out per group into
rows the profile never names — so one authored colour has no well-defined set of rows to paint.

`--lenses bugs,impl` overrides a profile's roster while keeping its body.
It produces **one agent carrying every named lens**, not one agent per lens —
the alternative would change the source count, and a caller asking for two lenses is asking for a viewpoint,
not for two corroborating votes.
The synthesized entry inherits the profile's top-level `model` **whole, binary included**, so
`--profile codex-only --lenses bugs` finds on codex; a roster's own per-entry model does not survive the
override, since the caller named the lens set explicitly.
Taking the model while forcing the binary to claude is what built a claude agent asked for `gpt-5.6-sol`,
and on a codex-only host left the run with no finder that could launch.
The profile's `stages:` block survives it too, because the flag replaces the roster and a stage is not in
the roster.
It is named `lenses`, and that name is not cosmetic: it reaches `Finding.sources` and becomes
`agents/lenses.jsonl` and `prompts/agents/lenses.md`, so it can never be empty.
It validates through `AgentSpec.validate` like any authored entry, which is what gives the override the
name and lens checks for free.

Profiles and stage prompts share a parsed shape but not an interface:
a stage prompt has no roster, so it must not expose a roster method,
and composing a stage prompt must not require fabricating a fake roster entry.
Runner selection is the one thing they genuinely share, so it travels as its own small value —
a `Runner` inside `app/prompt`, a `RunnerSpec` across the boundary — that both can produce.
See `.claude/rules/pipeline.md`.

### Executor and lens are orthogonal

A `model:` names `claude` or `codex` and nothing else.
Anything else is a **load-time** error with a clear message, never a runtime surprise.

There is no codex-specific prompt file and no per-entry prompt-path override.
Codex is an entry whose `model:` names it, composing `lenses/adversarial.md`.
Consequences worth preserving: the adversarial lens can run on claude by changing one word,
and the `bugs` lens can run on codex.

Lens text must stay executor-agnostic.
The output-contract difference — claude has `--json-schema`, codex does not — is injected by the executor.
Never write "return JSON shaped like…" into a lens file.

**The contract is appended after the stage has already archived the prompt, so the stage appends it to what
it stores too**, through the exported `executor.CodexOutputContract`.
The text stays in the executor — that is what the rule above is about — but an archived codex prompt missing
the one instruction that asks for JSON at all is not the bytes the model saw, and describes a run that did
not happen.

Keep the vocabulary singular. The authored surface is one `model:` string; inside `app/prompt` the parsed
form is a `Runner`, and the value the pipeline hands an executor factory is a `RunnerSpec`.
`executor` names the binary and the package that supervises it, never the front-matter key — there is no
`executor:` key to confuse it with any more.

### Composition

One agent's prompt = profile body + each of its lens files, concatenated, with `{{VAR}}` substituted.

Variables: `{{SCOPE}}`, `{{GOAL}}`, `{{PROFILE}}`, `{{CONTEXT}}`, `{{WORKDIR}}`,
plus `{{FINDINGS}}` and `{{SOURCES}}` for the synthesis and verify stages.

**Context variables expand to absolute paths, never to file contents.**
`{{SCOPE}}` and `{{GOAL}}` become paths to files in the round's `input/`, and so does `{{PROFILE}}` when
the round carries a non-empty one — otherwise it names the round's own copy of `./.revmux/profile.md`, written to
`prompts/input-profile.md`, which is the one context variable with a layer under it.
`.claude/rules/config.md` owns that resolution.
`{{CONTEXT}}` becomes the path to its `context/` directory, and the profile body instructs agents to read them.
Prompt composition therefore only stats those files and never opens one, so there is no way for a large
scope to bloat a prompt. The TUI separately opens a bounded startup snapshot with size and encoding
guards; headless mode does not.

### Prior rounds are injected, not a variable

revmux already wrote the task's earlier rounds, so it hands them to every process rather than making each
caller copy them forward. This does not weaken "revmux never derives context":
it surfaces what revmux itself produced, under a path it owns, and never reads a repository to do it.

The rounds are the task directory's own children, and a round is a directory whose `manifest.json` carries
a run's record of itself — `task.HasRun`. `archive.New` creates that file empty as it claims the round and
the finished run fills it in, so an empty one is a claim rather than a round. Anything else under the task,
`task.md` included, is not one either, and neither is the round being written, which is why the inventory is
resolved before `archive.New` rather than after it — and why gating on the file's mere presence would put a
re-run of an interrupted round into its own inventory.

**This is an injection, not a `{{VAR}}`.**
A variable is opt-in per file — any lens or profile omitting it silently loses the history, including
user-overridden files written before the feature existed.
The composer appends the block to every composed prompt instead, the same way the codex executor appends its
own output contract rather than trusting a lens to carry it.
The vocabulary therefore stays closed at the variables listed above.

A bare directory path tells an agent nothing about whether the contents are worth opening, so the block is
the path plus a generated one-line inventory per round — name, when it ran, finding counts by severity, and
which sources degraded:

```
Prior rounds for this task: /abs/.revmux/tasks/pr-123/
  01-initial    2026-07-26T14:30Z  8 findings (1 critical, 3 major, 4 minor)  sources 4/4
  02-after-fix  2026-07-26T16:02Z  2 findings (0 critical, 1 major, 1 minor)  sources 3/4, docs+tests degraded
Each round holds report.md (rendered) and findings.json (machine shape). Read the rounds you judge relevant.

Re-evaluate everything independently. A prior round reporting an issue is not evidence that it is real,
and a prior round missing one is not evidence that it is absent.
```

The inventory is metadata revmux computes, not review content lifted off disk — findings stay in the files.
On a first round the block is omitted entirely rather than injected empty.

Finders, synthesis and verify all receive it.
**The independence instruction is part of the injected block, never left to the profile body.**
An agent told a prior round flagged something at a location tends to confirm it rather than judge it,
which is the same anchoring failure that makes codex a peer rather than a second pass.
Injecting the data without the guard, or letting an overridden profile drop the guard, reintroduces exactly
the dependence the cross-source boost assumes is absent.

- An unresolved `{{VAR}}` left in a composed prompt is a bug, not a warning. Fail loudly.
- A missing variable resolves to an explicit placeholder ("none provided"), never to a path that does not exist —
  an agent whose `Read` fails cannot tell absence from a broken run.
- The vocabulary is closed: `SCOPE`, `GOAL`, `PROFILE`, `CONTEXT`, `WORKDIR` and the two stage variables.
  A lens naming anything else is a load-time error, which is what makes a typo'd variable loud rather than silent.
  Arbitrary extra material goes in `context/`, which is why that one is a directory.
- Shared text belongs in the profile body, never duplicated across lenses.
  If two lenses say the same thing, it is preamble.
- Composition needs the profile body, so it hangs off the profile, not off a bare roster entry.

### Never embed content

Prompts carry paths, refs and instructions. The agent fetches the diff and reads the files itself.
Embedding a diff makes prompts enormous and slows every launch.
Because every context variable is a path, this rule needs no judgment call —
there is no per-variable decision about what is small enough to inline.

### Shipping defaults

The `config` file installs fully commented out, so a file containing only comments can be safely
overwritten on upgrade while any uncommented line marks it customized and preserves it.

Prompt and lens markdown is content, not settings, so it ships live.
Overriding means copying the file and editing it; the embedded version stays the fallback for every file not copied.
Deleting a lens file on disk does not disable it — the embedded one is used.
To actually drop a lens, remove it from the profile roster.

`revmux init` is what turns that resolution into files a user can edit: every path `Provenance` reports,
written to the same relative path under `./.revmux/`, and one already there is reported and left
byte-identical rather than overwritten.
It writes what **resolved**, so a user with `~/.config/revmux/` overrides gets his own text copied down and
a tree with nothing left under it to fall back to.
`--dump-defaults` is the only way to the embedded bytes, and pointing either of them at the other's layer
removes the one thing it is for.

### The winning file's bytes are retained, not re-read

`Set` keeps the raw bytes of whichever file won the chain, in a `files` map of one `fileRef` per relative
path carrying the layer, the source and the hash alongside them.
`Provenance` is **derived** from that map rather than accumulated beside it: two per-path records holding
the same layer and source are two records to keep in step, and the pair kept in step by one line is the one
that eventually is not — so `Content` and `Provenance` cannot disagree about which layer won a file.

`Content(relPath)` returns those bytes **with the front matter still on them**, and that is the whole
constraint on it: `revmux init` writes exactly what it hands back, so a stripped write produces a lens with
no `description:` and a profile with no `agents:` — a tree the next `Load` rejects, which would have init
break the project it just initialized.
The parsed `doc.Body` is therefore not a substitute for the retained bytes, and neither is re-reading the
file at materialization time: that is a TOCTOU against `Provenance`, and it cannot reach the embedded layer
at all.
An unknown path is an error naming it, never empty bytes — a caller writing zero bytes it took for content
gets the same broken tree by a quieter route.

### `description:` front matter

Every profile, stage and lens file may carry a `description:` one-liner.
`revmux config` reports it, and that catalog is the only view a caller model has of the lens set —
composing `--lenses bugs,quality` means knowing what `quality` covers without reading its body.

It is optional at load, so overriding a lens does not require re-authoring metadata,
but every **shipped** file has one and a test asserts it.
A description is never inherited from the embedded default when an override wins:
an override is different text, and the default's summary would describe something else.

### `task.md` is a fourth front-matter kind, and it does not live here

Profiles, stages and lenses are the three kinds `app/prompt` parses. `task.md` is the fourth, and it is
parsed by `app/task` instead: it describes the task being reviewed rather than shaping a review, so
importing `app/prompt` for it would widen that package's contract to non-prompt files.
What it shares is the convention — a leading `---` block, `yaml` with `dec.KnownFields(true)` so an unknown
key is rejected, and a body below it that is prose nothing parses.
It shares the **scanner** too, `app/frontmatter`: the CRLF pre-pass and the empty-block case are subtle
enough that a second copy is a second thing to get wrong, and `app/task` importing a scanner is not
`app/task` importing the prompt tree.

Where the two kinds differ is what a rejected key costs. A bad prompt file fails the load and the run
stops. `task.md` is read by `revmux config`, which lists the task either way and reports the parse failure
as `meta_error` on that entry — a task dropped from the list is one a caller mints a second id for, and
anchors that are silently empty are the same shape as a task nobody described.

```yaml
---
description: OAuth token exchange rework
url: https://github.com/umputun/revmux/pull/123
branch: feature/oauth
base: 4ed3259
---
```

Every key is optional, the body is optional, and the file itself is optional — an absent one is a task with
no metadata, not an error.
The keys exist so a caller can match an existing task exactly instead of deriving an id and forking the
history on a near-miss, and `revmux config` reports them back under `paths.tasks`.

**revmux stores and reports these and never resolves one.**
No `git` runs against `branch` or `base`, nothing is fetched from `url`, and nothing is checked out.
They are strings the caller wrote and strings the caller reads back.
This is where the zero-VCS-dependency rule would erode first, because resolving a ref looks like a small
convenience right up to the point revmux needs a repository.

`Meta` therefore carries **both** `yaml` and `json` tags: it is parsed from front matter and marshaled into
the `revmux config` payload, where an untagged field would emit `URL` rather than `url`.

### Validation at load

- every lens named by a roster entry exists
- every stage named by a profile's `stages:` block is one the pipeline dispatches — `synthesis` or
  `verify`, not merely a `prompts/*.md` that loaded
- every `model:` parses: the binary is `claude`, `codex` or `opencode`, and an effort suffix is one of `low`,
  `medium`, `high`, `xhigh`, `max`. `parseRunner` is the only way a runner is built, so it is the only
  place either vocabulary is checked — a second check elsewhere is unreachable code pretending to guard
- `color`, when present, is an ANSI-16 name (`red`, `bright-blue`, …) or `#RRGGBB`
- no duplicate agent names in one roster
- front matter parses, and a profile with no `agents` is an error rather than an empty run
- front matter carries only the keys its own kind of file defines, so each kind declares its own shape
  rather than sharing one: `model` belongs to a profile, a roster entry and a stage, `agents` and
  `stages` to a profile, and a lens takes `description` alone.
  One shared shape accepts every key in every file, so a key that belongs to another kind would parse
  and then be ignored — the silent no-op this package rejects everywhere

Invalid values are rejected, never silently defaulted.
A typo'd model quietly changing which model reviews your code is worse than a startup error.
