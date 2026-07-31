# Glamour Markdown Rendering in the TUI

## Overview

The TUI's markdown rendering is an inline highlighter, not a renderer. `app/ui/inputs.go` handles four
block constructs (fenced code, ATX headings, list markers, blockquote) and `app/ui/markdown.go` handles two
inline spans (backticks, `**bold**`). Everything else falls through as a paragraph, so a caller's `scope.md`
shows GFM tables as raw pipes and a `|---|---|` separator row, `---` rules, `*italics*`, `[links](url)` and
strikethrough as literal punctuation, and 4-space indented code as prose.

The findings browser is thinner still: `findings.go:196` runs each line of a finding's `Body` and `Fix`
through the inline-only `markdown()`, so a list or a fenced snippet a model writes into a finding shows raw
markers.

This replaces both with `github.com/charmbracelet/glamour` v1.0.0. The input viewer renders the caller's
markdown files as documents, and the findings browser renders a finding's body and fix as documents. The
combined log and per-agent scrollback keep the inline path: they are one line per event with a timestamp and
agent prefix, not documents.

**Heading hashes are kept, by overriding one style key.** `.claude/rules/tui.md:94-95` and the `heading`
godoc (`markdown.go:29-31`) make keeping the hashes a deliberate rule: the pane is showing a markdown
document and a reader with the report open beside it should see the same thing. glamour's `dark` style keeps
`## ` and `### ` as prefixes but replaces h1's hash with a padded band (`h1.prefix: " "`, background 63), so
the rule breaks at exactly one heading level. Rather than trade the rule away, the renderer is built with
`WithStyles` over a copy of `styles.DarkStyleConfig` with `h1.Prefix` restored to `"# "` and the background
band dropped — which also removes a purple band that has nothing to do with this palette.

**Also in this plan, unrelated to rendering** (Task 5): `esc` and `q` currently quit at any moment, because
`keys.quit` binds `q`, `ctrl+c` and `esc` together. Nothing but `ctrl+c` should end a review that is still
running, and `q` should wait for the report. It rides along because it is the same package, the same file
and the same test file.

## Context (from discovery)

- files involved: `app/ui/inputs.go`, `app/ui/markdown.go`, `app/ui/findings.go`, `app/ui/model.go`,
  `app/ui/style.go`, `app/ui/view.go`, `app/ui/handlers.go`, plus their `_test.go` files; `go.mod`,
  `go.sum`, `vendor/`
- unchanged by design: `app/ui/combined.go`, `app/ui/agentpane.go`, `app/ui/wrap.go`, `app/progress.go`
- verified glamour facts, read from `$GOMODCACHE/github.com/charmbracelet/glamour@v1.0.0/glamour.go`:
  `getDefaultStyle` reads `os.Stdout` and `termenv.HasDarkBackground` **only** when the style is
  `AutoStyle` (lines 306-313); an explicit style name skips that path entirely. `WithStandardStyle(string)`
  (line 118) and `WithColorProfile(termenv.Profile)` (line 112) both exist. `RenderBytes` is pure.
  `WithWordWrap(n)` bakes the width into the renderer. Default `ColorProfile` is `termenv.TrueColor`.
- both glamour v0.10.0 and v1.0.0 require lipgloss v1, so no lipgloss v2 migration
- prior art in this developer's own projects: `skiltas/transport/ssh/render.go` uses
  `NewTermRenderer(WithStylePath(...), WithWordWrap(width))`; `ralphex` is already on glamour v1.0.0
- existing precedent for the color-profile hazard: `newStyles` in `app/ui/style.go:44-60` builds its
  lipgloss renderer against `cfg.Output` (the tty) rather than the default renderer, precisely because
  lipgloss's default profiles `os.Stdout`, which is a pipe under `revmux > findings.json`
- pane model: `viewState.navState{tab, scroll}`, `paneLines() []string`, `maxScroll()`, `clipAll()`. Every
  pane is a `[]string` addressed by line
- **there is no per-finding folding in the code.** `findingsState` holds `rows`, `matches`, `query`,
  `typing` and nothing else, and its godoc (`findings.go:24-27`) records that folding, a cursor and
  per-row expansion were all tried and removed. `.claude/rules/tui.md` and the `detail` godoc
  (`findings.go:152`) still describe folding as a feature. That drift predates this plan and is fixed in
  Task 5

## Development Approach

- **testing approach**: Regular (code first, then tests). The exact ANSI glamour emits has to be observed
  once before it can be asserted, so tests-first would be writing assertions against a guess.
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
- **CRITICAL: existing app/ui tests assert on the current rendered output. They are rewritten to the new
  expected output, never weakened, narrowed or deleted.** A test that asserted a table renders as raw pipes
  becomes a test that asserts it renders as a table. If a test can no longer express its intent, stop and ask.
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility

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

- **unit tests**: required for every task (see Development Approach above)
- **e2e tests**: none in this project. `app/ui` is tested by driving `Update` with synthetic messages and
  asserting on `View()` output, per `.claude/rules/tui.md`. No test may spawn a process or require a tty.
- **structural assertions, not byte-exact ANSI**: assert that a table's box-drawing characters are present
  and the raw `|---|---|` row is absent, that heading text survives, that a list renders as a list. Pinning
  byte-exact escape sequences would pin the suite to a glamour patch version.
- **one invariant test above all others**: every line any renderer returns satisfies
  `lipgloss.Width(line) <= width`. `Wrap` guarantees this today and glamour does not for every construct.
  Tables are **not** the gap — `WithTableWrap` defaults to true and `ansi/blockstack.go` already deducts
  indent and margin from the wrap width. **Code-block content is**: a long line inside a fence is not
  hard-wrapped, and today it is, by `verbatimLine` (`inputs.go:68,100`). Without the check, overflow
  reaches `clipAll` (`view.go:25`) and the tail of exactly the lines worth reading is cut, which is what the
  `Wrap` godoc (`wrap.go:26-28`) and `.claude/rules/tui.md` both exist to prevent.

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

One private renderer type in `app/ui`, owned by `Model` as a pointer field beside `combined` and `findings`,
so a value-receiver `View()` can fill its cache. It wraps `glamour.TermRenderer`, held **one per width**
because `WithWordWrap` bakes the width in and two panes ask for two different widths — the input viewer at
`m.view.width()`, the findings browser at that less its indent. A single renderer rebuilt on every width
change would thrash on every `i` keypress.

Two callers:

- **input viewer** — `markdownInput` stops walking lines and hands the whole document to the renderer
- **findings browser** — `detail` renders the finding's body and its fix as two separate documents.
  Separate, not one composed document: an unbalanced fence in a model-written body would otherwise swallow
  the `fix:` and attribution sections below it, and today each part renders independently so a stray fence
  cannot reach past its own paragraph

The combined log, agent scrollback and `app/progress.go` keep `Wrap` + `markdown()`. That split is
deliberate and is the plan's main documentation burden: `.claude/rules/tui.md` currently states "there is
one `Wrap`" and "every color in this package is raw SGR" as absolutes, and both become conditional.

**Color profile and light terminals.** glamour is given an explicit style and an explicit
`WithColorProfile`, so it never runs its `AutoStyle` branch and never reads stdout. Both facts come from the
same lipgloss renderer `newStyles` already builds against `cfg.Output` — which today discards that renderer
and returns only `styles`, so `styles` gains `profile` and `dark` fields rather than a second
`lipgloss.NewRenderer(cfg.Output)` being built in `New`. Two constructions of the same thing are two answers
to "what can this terminal do" that can disagree, which is the divergence this project keeps writing rules
against.

The `dark` flag matters as much as the profile. Every color in `style.go:12-19` is a
`lipgloss.AdaptiveColor`, documented as deliberate: "a reviewer running a light terminal gets legible muted
text instead of the near-invisible grey a dark-only palette produces". A fixed dark glamour style would make
the chrome adapt while the document panes do not, which is the same divergence one paragraph up. The base
style config is therefore `styles.DarkStyleConfig` or `styles.LightStyleConfig` chosen from the renderer's
own background, with the h1 override applied to whichever is picked.

## Technical Details

**New type** (`app/ui/mdrender.go` — named for the concern, not the library, so a later swap does not
rename the file):

```
type mdKey string

type mdRenderer struct {
    style     ansi.StyleConfig              // dark or light base, h1 prefix restored
    profile   termenv.Profile
    renderers map[int]*glamour.TermRenderer // by width
    cache     map[mdCacheKey][]string
    cached    int                           // bytes held, bounded by mdMaxCache
}

type mdCacheKey struct {
    key   mdKey
    width int
}

type mdDoc struct { // what a caller asks for: key, source, pane width, left pad
    key    mdKey
    src    string
    width  int
    indent int
}
```

`mdKey` is a named type rather than a bare `string` on purpose: `lines(key, src string, width int)` has two
adjacent same-type parameters, and swapped they cache under the document text and render the key — no
error, wrong pane. The named type makes the compiler catch it.

- `newMDRenderer(profile termenv.Profile, dark bool) *mdRenderer` — constructor, no I/O. Two bare
  parameters rather than the `mdOpts` struct an earlier draft carried: CLAUDE.md sets the option-struct
  bar at four, and two fields of different types carry no swap hazard (review pass)
- `(r *mdRenderer) lines(d mdDoc) []string` — cached render to pane lines, padded by `d.indent` **before**
  it is cached, since the findings browser re-lays the whole report every frame and only the pane slices
  to the visible window (review pass)
- `(r *mdRenderer) render(src string, width int) []string` — uncached path and the fallback
- `(r *mdRenderer) build(width int) *glamour.TermRenderer` — get-or-create for that width. Returns no error:
  `wrapcheck` is enabled (`.golangci.yml:50`) and every caller treats a construction failure as "fall back"
  rather than propagating it, so the error is handled here and a nil renderer means fall back
- `(r *mdRenderer) trim(out string, width int) []string` — split, strip leading and trailing blank lines,
  and hard-wrap any line still wider than `width`
- `(r *mdRenderer) reset()` — drop renderers and cache, so memory stays bounded by the widths currently in
  use rather than by every width the terminal has ever been. At a stable width the bound is `mdMaxCache`
  instead: the cache is dropped whole once the rendered bytes it holds would exceed it (review pass).
  **This is a memory bound, not a correctness
  mechanism**: both maps are keyed by width, so a resize already renders at the new width without it. Do not
  "simplify" by dropping the width key and relying on reset — that is the stale-cache bug

**The document margin is not stripped.** `ansi/blockstack.go` already deducts `Margin*2` from the wrap
width, so output fits `WithWordWrap(width)` and the margin is left padding. A naive `TrimPrefix("  ")` on a
code-block row cuts into a line that opens with a background SGR and leaves the block's background ragged.
The margin stands, and nothing tries to remove it.

**As shipped, the findings browser pads by `mdIndent = 2` and renders at `width - mdIndent`.** Glamour's own
two-column margin sits inside that, so a body lines up with the four-space pad `indent` gives the
single-line rows beside it, and the pad plus the document still fit the pane. Rendering at the full pane
width instead would make both panes share one renderer — Task 4's "both widths stay resident" case would
have nothing to assert — and would leave a two-column body beside a four-column location, which reads as
misalignment.

**As shipped, the document path has a size ceiling, `mdMaxDoc = 64 KiB`.** glamour's cost is structural, not
content-dependent: its `Document` style carries a color, so every emitted row is padded to the wrap width
with one escape pair per trailing space and the output runs many times the source. Measured at width 100,
fenced code and tables are the worst class — 64 KiB renders in ~110ms into ~3.4 MB, 1 MiB into ~2.8s and
~84 MB. That render is synchronous on the bubbletea goroutine and the result is then held by the `lines`
cache, so a large input stalls `Update` (during which the event channel drops) and is retained afterwards.
The snapshotter's 1 MiB-per-file and 8 MiB-total limits were sized for a line-at-a-time renderer and bound
neither cost, so the ceiling is here. Above it `render` falls through to the same inline path an error
falls through to.

**The fallback must be per line.** If the source is over `mdMaxDoc`, or `NewTermRenderer` or `Render`
returns an error, fall back to the old path — but `Wrap("", src, width)` on a whole document returns a **single** slice element containing embedded
newlines whenever the longest line already fits (`wrap.go:36-37`). The pane model is one slice element per
line: `maxScroll` (`view.go:57`) would report 0 and `detailPane` would pad to `paneHeight` (`view.go:26-28`),
producing a frame taller than the terminal. The fallback therefore iterates:
`for line := range strings.SplitSeq(src, "\n") { out = append(out, Wrap("", markdown(line), width)...) }`.

**Tabs must still be expanded, and per line.** `markdownInput` calls `expandTabs` on every line today
(`inputs.go:56`) because a tab is one cell to lipgloss and up to eight to the terminal, so an unexpanded row
escapes the frame — the reason is in the `verbatimLine` godoc (`inputs.go:93-95`) and
`inputs_test.go:97-100` pins it. Handing the raw document to glamour drops that call. **Not on the whole
document in one call**: `expandTabs` (`inputs.go:108-122`) never resets `column` on a newline, so a single
call over a multi-line string computes every line's tab stops from a column that has accumulated all
preceding lines. Split, `expandTabs(line)` each, rejoin, then render.

**As shipped, the oversized check runs before anything is split or expanded**, so an input the ceiling
exists to keep off the costly path does not pay the preparation for it, and the fallback expands lazily as
it wraps (review pass).

**As shipped, that expansion lives in `mdRenderer.render`, not in either caller.** Put in `markdownInput`
alone, the findings browser's `document` renders a model's tab-indented snippet unexpanded and it escapes
the pane — the common case, since that is how a model writes Go. In `render` it is behind the `lines`
cache, so a large document pays for it on a miss rather than on every frame, and `expandTabs` becomes a
package-level helper shared by the document renderer and `verbatimLine`, in its own `app/ui/tabs.go` with
both tab stops — `mdTabStop` has no consumer in `inputs.go` (review pass). It measures whole segments
between tabs rather than one rune at a time: per rune it cost a display-width parse per character and
counted a grapheme cluster once per component, putting the stop after a ZWJ sequence in the wrong place.

**As shipped, `expandTabs` takes its stop from the caller, because the two callers want different ones.**
`verbatimLine` passes `termTabStop = 8`, reproducing what the terminal draws. `mdRenderer.render` passes
`mdTabStop = 4`: it expands ahead of the parser, and CommonMark computes block indentation on four-column
stops, so at eight a list nested under a tab lands past the code-block threshold and glamour renders its
`-` marker as literal text — the raw-markdown defect this plan exists to remove, reintroduced by the
expansion that fixes the width one. Four satisfies both: no tab reaches the terminal, and the block
structure is the one the file has.

**As shipped, `render` entity-escapes the spans goldmark reads as raw HTML before handing the document
over** (review pass). glamour's `ansi.NewRenderContext` hardcodes a bluemonday `StrictPolicy` and pushes
every raw-HTML node through it, so CommonMark reading `<task>`, `<T>` or `<binary>` as raw HTML deleted them
from the pane with no marker — a path template or a type parameter gone mid-sentence, while the inline path
kept the same text verbatim. glamour exposes no way to disable the stripper, so it is handled on the source
side. The spans come from a goldmark parse configured exactly as glamour's, never from a scan for angle
brackets: escaping every `<` reaches into fenced and indented code where `&lt;` is what a reader would see,
and escaping `>` with it turns a blockquote marker into text.

**Cache keys are namespaced, and spelled out.** One `cache` map serves both callers, so a bare index would
collide across them and one key for a finding would serve its body where its fix belongs. The three keys are
`input:<tab>`, `finding:<row>:body` and `finding:<row>:fix`.

`<row>` is `f.matches[i]`, never `i`. The composition site is `for i, v := range vis` (`findings.go:92`),
where `i` indexes the **filtered** slice: keyed on `i`, typing a filter makes index 0 a different finding at
the same width and the cache serves the previous finding's body. `f.matches[i]` is a stable index into
`f.rows`, which never mutates after `newFindings`.

**What stays.** `markdown.go` keeps `markdown()`, `markdownWithin()` and `heading()` for the log panes and
`app/progress.go`. `Wrap` keeps all its current callers except the two that move.

**`indent` stays, with two callers instead of four.** It has four call sites, all inside `detail`
(`findings.go:155-166`), but only two of them move: `Body` and `Fix` become rendered documents, while
`Location()` and the attribution line stay single-line and keep their 4-space pad and their wrap. Deleting
`indent` would silently drop both from those two lines and break `findings_test.go:90,153,157` and the
exact-`[]string` subtests at `:102-105,167`.

**What goes.** `inputs.go`'s `markdownFence`, `markdownHeading` and `markdownList` become dead once
`markdownInput` delegates, and are deleted rather than left behind, per CLAUDE.md: no keeping old functions
for imaginary compatibility. Their removal takes the only `verbatimLine("    ", …)` call with it
(`inputs.go:68`), leaving `verbatimLine` with a head that is always `""` and `expandTabs` with a column that
is always 0 — `unparam` is enabled (`.golangci.yml:45`) and will fail the task gate on both unless they are
collapsed to their remaining shape.

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): code, tests and documentation inside this repo
- **Post-Completion** (no checkboxes): visual verification on a real terminal, which no test can do

## Implementation Steps

### Task 1: Add glamour dependency and the mdRenderer wrapper

**Design Contract:**

Type:
- `mdRenderer` (private — no caller outside `app/ui`)
- `mdKey` (private — named string key, swap-hazard guard)
- `mdCacheKey` (private — cache composite key)
- `mdOpts` (private — constructor options, `{profile termenv.Profile; dark bool}`)

Methods (full signatures):
- `(r *mdRenderer) lines(key mdKey, src string, width int) []string`
- `(r *mdRenderer) render(src string, width int) []string`
- `(r *mdRenderer) build(width int) *glamour.TermRenderer`
- `(r *mdRenderer) trim(out string, width int) []string`
- `(r *mdRenderer) reset()`

Standalone helpers planned (justification why NOT a method):
- `newMDRenderer(opts mdOpts) *mdRenderer` — constructor, per the standalone-helper carve-out
- `mdStyle(dark bool) ansi.StyleConfig` — picks the base style config and applies the h1 override. Called
  by the constructor **and** by `mdrender_test.go` to assert the override independently of a renderer;
  a method on `mdRenderer` would have to exist before the value it configures

Exports (justification per item: who outside the package calls this?):
- none

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `vendor/` (via `go mod vendor`)
- Create: `app/ui/mdrender.go`
- Create: `app/ui/mdrender_test.go`

- [x] `go get github.com/charmbracelet/glamour@v1.0.0` — **`go get` only at this point.** Running `go mod
      tidy` before any code imports glamour drops it straight back out of `go.mod`, which is the
      "no imports without code" rule applied to the module graph
- [x] create `app/ui/mdrender.go` with `mdRenderer`, built with `WithStyles(mdStyle(dark))`,
      `WithWordWrap(width)` and `WithColorProfile(profile)` — never `WithAutoStyle` or
      `WithEnvironmentConfig`, which read stdout
- [x] implement `mdStyle`: `styles.DarkStyleConfig` or `styles.LightStyleConfig` by the flag, then restore
      `h1.Prefix` to `"# "` and drop h1's background band, so the heading-hash rule in
      `.claude/rules/tui.md:94-95` survives
- [x] implement `build` as get-or-create per width, returning nil on a construction failure, and `reset` to
      drop both maps
- [x] implement `trim`: strip leading and trailing blank lines and hard-wrap any line wider than `width`.
      Do not strip the document margin — see Technical Details
- [x] implement the per-line error fallback in `render` (never `Wrap` on a whole document — see Technical
      Details)
- [x] `go mod tidy && go mod vendor` — now that `app/ui` imports glamour and `termenv` directly, `tidy`
      moves `termenv` out of the indirect block and `vendor` materializes the tree
- [x] write tests for `lines`: cache hit returns equal output, a second width does not evict the first,
      `reset` clears both
- [x] write tests for `render`: GFM table renders as a table and the raw `|---|---|` row is gone, inline
      code renders, horizontal rule renders, link and italics render
- [x] write a test that h1 keeps its `# ` and that h2/h3 keep `## ` and `### `
- [x] write a test that `mdStyle(false)` differs from `mdStyle(true)`, so the light path is not dead
- [x] write the width-invariant test: every returned line satisfies `lipgloss.Width(line) <= width`, with a
      long line inside a fence as the case that actually overflows, and a wide table as a regression guard
- [x] write a test for the error fallback returning one slice element per source line
- [x] verify `go build ./...` and that the vendor tree is consistent
- [x] run tests - must pass before task 2

### Task 2: Expose the color profile and route the input viewer through the renderer

**Files:**
- Modify: `app/ui/style.go` (add `profile termenv.Profile` to `styles`, set from `r.ColorProfile()`)
- Modify: `app/ui/style_test.go`
- Modify: `app/ui/model.go` (add the `md *mdRenderer` field, initialize in `New` from `style.profile`)
- Modify: `app/ui/inputs.go`
- Modify: `app/ui/inputs_test.go`
- Modify: `app/ui/model_test.go`

- [x] add `profile` and `dark` to `styles`, both from the same renderer `newStyles` already builds against
      `cfg.Output` — `ColorProfile()` and `HasDarkBackground()`
- [x] add the `md *mdRenderer` pointer field to `Model` and initialize it in `New` beside `combined` and
      `findings`, from `style.profile` and `style.dark`
- [x] rewrite `markdownInput` to expand tabs **per line** (see Technical Details), rejoin, then hand the
      document to `md.lines` keyed `input:<tab>`
      — the expansion moved into `mdRenderer.render` in the review pass afterwards, so the findings
      browser gets it too and the cache sits in front of it rather than behind
- [x] leave `verbatimInput` and `visibleMetadata` untouched — non-markdown inputs and the control-character
      guard are unchanged
- [x] delete the now-dead `markdownFence`, `markdownHeading` and `markdownList`
- [x] collapse `verbatimLine` and `expandTabs` to their remaining shape: with the fence branch gone, the
      only `verbatimLine` call passes `head == ""` and `expandTabs` always gets `column == 0`, and
      `unparam` (`.golangci.yml:45`) fails the task gate on both
- [x] rewrite the existing `inputs_test.go` cases to the new expected output, case by case, keeping every
      behavior each one pins: fenced code, headings **including h1's `# `**, lists, blockquotes, empty
      file, notices, **tab expansion** (`inputs_test.go:97-100`), **`TestModel_inputLines_wraps`**, and
      **`TestModel_inputLines_markdownFenceMatchesDelimiterAndLength`** (`inputs_test.go:34-50`), which
      pins that a `~~~` and a shorter fence nested inside a longer one stay literal — that behavior moves
      to glamour and still needs an assertion
- [x] add test cases for what was previously unrenderable: GFM table, horizontal rule, italics, link
- [x] add a test that a non-markdown input still renders verbatim
- [x] write a test that `newStyles` reports a usable profile and background for a nil writer (the test path)
- [x] run tests - must pass before task 3

### Task 3: Block-render finding bodies in the findings browser

**Design Contract:**

Type:
- `findingsState` (existing, private) gains one field: `md *mdRenderer`

Methods (full signatures — changed only):
- `newFindings(rep finding.Report, md *mdRenderer) *findingsState` — the renderer arrives at construction
  rather than being threaded through `render`/`rowLines`/`detail`, which would push two of them to three
  parameters for no gain. `findingsState` holds no `Model` reference and must not gain one
- `(f *findingsState) detail(v finding.Finding, row findingRow, width int) []string` — takes the stable row
  its caller read out of `f.matches` and names the two entries through `findingRow.key`, which is the one
  place `finding:<row>:body` and `finding:<row>:fix` are spelled. A named `findingRow` rather than a bare
  `int`, since beside `width` that would be two adjacent same-type parameters a caller can swap silently —
  the hazard `mdKey` exists to close (review pass: the key was assembled in fragments across three
  functions before this)

Standalone helpers planned (justification why NOT a method):
- none

Exports (justification per item: who outside the package calls this?):
- none

Nil behavior: **none, deliberately.** `newFindings` has exactly one caller, `m.complete`
(`model.go:210`), and every test reaches it through `New(ModelConfig{...})` plus a `CompletedMsg`. Once
Task 2 sets `Model.md` in `New`, `md` is never nil in the real binary, so a nil branch here would be
production code existing only to make a test pass, plus a second render path nothing exercises. If a guard
is ever wanted for a bare `Model{}` literal it belongs once at the `New`/`complete` boundary, not as a
second path inside `findingsState`.

**Files:**
- Modify: `app/ui/findings.go`
- Modify: `app/ui/model.go` (the `newFindings` call site in `m.complete`)
- Modify: `app/ui/findings_test.go`

- [x] add the `md` field and the `newFindings` parameter, updating the call site in `m.complete`
- [x] render `Body` and `Fix` as two separate documents under distinct keys — not one composed document, so
      an unbalanced fence in a body cannot swallow the fix and attribution below it
- [x] key on `f.matches[i]`, never the filtered-slice index
- [x] keep `Location()` and the attribution line on the existing single-line path, and therefore **keep
      `indent`** — it loses its `Body` and `Fix` callers and keeps its other two
- [x] keep `rows`, `matches` and the filter operating on `finding.Finding`, untouched by rendering
- [x] rewrite existing `findings_test.go` cases to the new expected output, keeping what each one pins:
      filter narrowing, severity grouping, empty report, the typing prompt,
      **`TestFindingsState_rendersTheWholeReport`** (`findings_test.go:146-175`), the canonical
      "body, fix and attribution are all on screen, each padded" test, and the exact-`[]string` subtests
      in **`TestModel_findingsPane`** (`:96-115`), which cannot survive block rendering unchanged and must
      be re-expressed rather than dropped
- [x] add test cases for a body containing a list, a fix containing a fenced snippet, and a body with an
      **unclosed** fence (the fix and attribution must still render)
- [x] add a test that filtering does not serve a stale cached body from another finding
- [x] run tests - must pass before task 4

### Task 4: Invalidate on resize and verify the pane model

**Files:**
- Modify: `app/ui/model.go` — `tea.WindowSizeMsg` is handled at `model.go:174-177`, not in `handlers.go`
- Modify: `app/ui/model_test.go` — per the one-test-file-per-source-file rule, the test for `Update`'s
  resize branch belongs here, not in `handlers_test.go` or `view_test.go`

- [x] evict on `tea.WindowSizeMsg` **to bound memory, not for correctness** — both maps are keyed by width,
      so a resize already renders at the new width. Keep the width key
- [x] evict lazily or keep only the current width rather than calling a blanket `reset` on every message:
      `Update` calls `m.maxScroll()` immediately after (`model.go:176`), which re-renders every finding
      through glamour, and a drag-resize delivers a stream of `WindowSizeMsg`
      — implemented as `reset` gated on `msg.Width != m.view.cols`: a height-only or repeated resize keeps
      the cache, and at the moment the width does move nothing is cached at the new width anyway
- [x] verify `maxScroll` is computed from the rendered line count, not the source line count
      — `view.go:57` measures `len(m.paneLines())`, which is the rendered slice; pinned by a test
- [x] write a test driving `Update` with a `WindowSizeMsg` and asserting the pane re-wraps
- [x] write a test that scroll position stays in range across a resize that shortens the document
- [x] write a test that the findings filter still narrows correctly after a resize
- [x] write a test that switching between the input viewer and the findings browser does not thrash the
      renderer cache (both widths stay resident)
- [x] write a resize test over a report large enough that a per-message full re-render would be visible
- [x] run tests - must pass before task 5

### Task 5: Stop esc and q from quitting a running review

Unrelated to the renderer, carried in this plan because it is the same package and the same test files.

**The problem.** `keys.quit` is `key.NewBinding(key.WithKeys("q", "ctrl+c", "esc"))` (`handlers.go:14`), so
all three quit at any moment, including mid-run. A reader who hits esc expecting to back out of something,
or q expecting a pager, kills the view of a review that is still running.

**The rule.** Only `ctrl+c` ends the program during a run. `q` quits once the report is in. `esc` never
quits — it keeps its two existing jobs and gains nothing.

Esc's three current roles, all verified in code: it abandons a filter (`editFilter`, `handlers.go:168`), it
leaves the input viewer (`handlers.go:37-40`), and it quits via `keys.quit`. The third is removed; the first
two are unchanged.

`ctrl+c` is already handled by the explicit `msg.Type == tea.KeyCtrlC` check at `handlers.go:32`, ahead of
the filter editor so a half-typed query is never a trap. That check is what makes ctrl+c unconditional, and
it stays exactly where it is.

The post-completion auto-exit countdown (`model.go:183-190`) is unaffected: it only ticks after
`CompletedMsg`, and any keypress already cancels it.

**Files:**
- Modify: `app/ui/handlers.go`
- Modify: `app/ui/handlers_test.go`

- [x] narrow `keys.quit` to `q` alone, dropping both `ctrl+c` (already handled explicitly ahead of it) and
      `esc`
- [x] gate the quit case on `m.done`, so `q` is inert until the report has arrived
      — as `Model.quitCmd`, not an inline `if`: `key` was already at gocyclo 20 and the branch pushed it to 21
- [x] leave the `tea.KeyCtrlC` check at the top of `key` untouched and unconditional
- [x] leave esc's input-viewer and filter-abandon paths untouched
- [x] update the `key` godoc, which describes quitting without the new gate
- [x] write a test that `q` mid-run returns no `tea.Quit`
- [x] write a test that `q` after a `CompletedMsg` does quit
- [x] write a test that `esc` mid-run returns no `tea.Quit`, in both review and findings modes
- [x] write a test that `esc` still leaves the input viewer and still abandons a filter
- [x] write a test that `ctrl+c` quits mid-run, including while typing a filter
- [x] ➕ `app/main_test.go`: three tests closed the TUI with a single `q` pressed before the report was in,
      which now hangs the run. They hold the key through `holdKey` instead, and `TestRunOpts_render`'s
      `finished` helper loses its `afterEvents` hook, whose only caller was one of them
- [x] run tests - must pass before task 6

### Task 6: Rewrite the documentation this change falsifies

**Files:**
- Modify: `.claude/rules/tui.md`
- Modify: `app/ui/wrap.go` (godoc at lines 19-24)
- Modify: `app/ui/findings.go` (godoc at lines 79-81, 152 and the deleted `indent`'s at 185-187)
- Modify: `README.md` (the key table at lines 641-643)
- Modify: `CLAUDE.md` (only if a keep-in-sync convention is affected)

- [x] rewrite the "there is one `Wrap`" passage in `.claude/rules/tui.md`: `Wrap` remains the one
      line-breaker for log panes, agent scrollback and `app/progress.go`; document panes wrap through
      glamour. State which is which and why, since the failure mode is two panes breaking the same text
      differently
- [x] rewrite "every color in this package is raw SGR" to carve out glamour's own output, keeping the
      raw-SGR rule for everything this package paints itself
- [x] record the AutoStyle hazard: glamour reads `os.Stdout` and `termenv.HasDarkBackground` when the style
      is `AutoStyle`, so the style name and color profile are always explicit
- [x] record that the log panes deliberately keep the inline renderer, so a later reader does not "finish
      the migration" and lose the one-line-per-event view
- [x] fix `Wrap`'s own godoc, which names the findings browser and input viewer Markdown prose as callers
      that "all wrap the same way" — after this change they do not
- [x] fix `findingsState.render`'s "Every row wraps" godoc
- [x] fix `.claude/rules/tui.md:106-107`, "Fenced code blocks hide their delimiter rows and indent their
      body as code", which describes the deleted `markdownFence` path
- [x] fix `.claude/rules/tui.md:161-163`, "The browser also renders the inline markdown a model writes into
      a finding … Raw ANSI, per the trap below" — false for body and fix, which glamour renders with full
      resets
- [x] fix `verbatimLine`'s godoc (`inputs.go:93-95`), which says it hard-wraps "input and fenced code";
      fenced code stops arriving there
- [x] fix `README.md:637`, "scroll, or move the cursor in the browser" — same stale-cursor class as the
      folding item above
- [x] record the test-path caveat: with `ModelConfig.Output` nil, which only happens in tests,
      `styles.profile` and `styles.dark` come from `lipgloss.DefaultRenderer()`, which profiles `os.Stdout`
      — the stream `tui.md:174-175` says a color decision must never be taken against. Production always
      passes the tty, so this is a note, not a defect
- [x] ➕ fix the stale folding documentation found during planning: `.claude/rules/tui.md` describes
      per-finding folding and a fold key, and the `detail` godoc says the body is "folded away on request",
      but `findingsState` has no fold state and its own godoc records that folding was removed
      — also took `README.md`'s `enter` fold row, `handlers.go`'s `browsing` godoc and the stale "cursor"
      in the Receivers section. The now-dead `keys.expand` binding was left in place here, Task 6 being
      documentation only, and deleted in the review pass afterwards along with its one test site
      (`view_test.go:221`)
- [x] rewrite the README key table (lines 641-643), which currently reads "`esc` | return from the input
      viewer; otherwise quit" and "`q`, `ctrl+c` | quit". Both are false after Task 5
- [x] rewrite the `.claude/rules/tui.md` line "`i` or `esc` restores the review tab and scroll position;
      `q` and `ctrl+c` still quit", and state the new rule: only ctrl+c ends a running review, q waits for
      the report, esc never quits
- [x] check the keep-in-sync list in `CLAUDE.md` for anything this falsifies — nothing: it names no
      rendering path, no `Wrap` and no key binding
- [x] no tests (documentation only) — verified by review

### Task 7: Verify acceptance criteria

- [x] verify a GFM table in an input file renders as a table, not as raw pipes
      — pinned by `TestModel_inputLines_markdownBlocksThatUsedToBeRaw`: `│` and `┼` present, `|---|---|` absent
- [x] verify `---`, italics, links and strikethrough render
      — same test; strikethrough was the one construct with no assertion and was added to it
- [x] verify a finding body containing a list renders as a list
      — `TestFindingsState_rendersBodyAndFixAsDocuments/a_list_in_a_body_renders_as_a_list`
- [x] verify the combined log and agent panes are unchanged — `git diff` over the branch touches neither
      `combined.go` nor `agentpane.go` nor `app/progress.go`; `wrap.go`'s only change is its godoc
- [x] verify esc and q do not end a running review and that ctrl+c does — `TestModel_key_quit`, seven cases
- [x] verify no `app/ui` code reads a file, spawns a process or touches stdout — `grep -n "os\.\|exec\."
      app/ui/*.go` returns only comment lines that mention stdout, now **three** rather than two:
      `style.go:45`, `style_test.go:14` and the new `mdrender.go:100` recording the AutoStyle hazard
- [x] verify every rendered line fits the pane width at 60, 80 and 200 columns
      — `TestModel_inputLines_fitsEveryWidth` already covered the input viewer at all three; the findings
      pane's equivalent ran at 60 only and was widened to the same three
- [x] run full test suite: `make test` — all 9 packages pass under `-race`
- [x] run `make lint` and `make check-plugins` — 0 issues; plugin trees agree
- [x] verify coverage did not drop below the project standard — 93.8% total, `app/ui` at 98.2%, against an
      80% bar

### Task 8: [Final] Update documentation

- [x] update `README.md` if the input viewer or findings browser description states rendering behavior
      — both did: the input-viewer line said "use the TUI's Markdown rendering" and the browser paragraph
      said nothing about how a finding's body reads. Both now state document rendering
- [x] update `CLAUDE.md` if new patterns were discovered during implementation — nothing to add: the
      keep-in-sync list names no rendering path, no `Wrap` and no key binding, and the glamour AutoStyle
      hazard belongs in `.claude/rules/tui.md`, which the `app/ui` rule already points at
- [x] move this plan to `docs/plans/completed/` (moved by the orchestrator after review phases)

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- run a real review with `revmux --task <id> --run <name>` and look at the input tabs and the findings
  browser on a real terminal. No test in this project drives a tty, so the visual result — colors against
  the surrounding frame, code-block backgrounds, table borders at narrow widths — can only be judged by eye.
- check behavior at a narrow terminal (60 columns or less), where glamour's table rendering degrades
  differently from the old raw-pipe passthrough
- check `revmux > findings.json` with the TUI running, confirming the report still lands on stdout clean

- check a **light** terminal, where `mdStyle(false)` picks `LightStyleConfig`. Nothing in the test suite
  can tell whether the result is legible next to the adaptive chrome

**Follow-up worth considering** (not in this plan):
- a full `ansi.StyleConfig` matching revmux's `colAccent`/`colMuted`/`colOK`/`colWarn`/`colErr` palette.
  This plan already overrides h1 to keep the heading-hash rule; the rest of the style stays glamour's until
  it has been seen next to the frame

Plan review: NEEDS REVISION addressed — all 5 critical and 10 important findings applied, plus the 4 minor
ones. Two of the review's premises were corrected rather than accepted: glamour does **not** strip all ATX
hashes (h2 and h3 keep their prefixes in `styles/dark.json`; only h1 does, and the plan now overrides that
one key instead of trading the rule away), and tables are not the width-invariant gap (`WithTableWrap`
defaults true) — fenced code is.

Smells pre-check: 12 items fixed before save — per-line fallback instead of `Wrap` on a whole document,
cache keyed on `f.matches[i]` instead of the filtered index, tab expansion kept, a Design Contract added to
Task 3 with the renderer arriving via `newFindings`, `styles.profile` added so the profile has a source,
`go mod tidy` added, a width-invariant test added, the in-code godoc sites added to Task 5, `mdKey` named
type against the swap hazard, body and fix rendered as separate documents, one renderer per width instead of
one rebuilt renderer, and the file named `mdrender.go` rather than `glamour.go`.
