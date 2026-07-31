---
paths:
  - "app/ui/**"
---

## TUI — bubbletea model

Built on bubbletea, bubbles and lipgloss.
A single `Model` with state grouped into sub-structs (`cfg`, `view`, `agents`, `combined`, `findings`),
methods split across files by concern, each source file with a matching `_test.go`.

### This package does no OS work. Ever.

No `exec.Command`, no file reads or writes, no network, no git, no writes to stdout — not even in a small helper.
The pipeline runs headless and emits typed events; the TUI is one subscriber and the plain progress renderer is another.

The cost of a new package is lower than keeping `app/ui` entangled with OS boundaries.
When the TUI appears to need OS work, extract it and consume it through a consumer-side interface.
The input viewer follows that boundary: `package main` captures the files after opening the tty and before
the pipeline starts, then passes immutable documents through `ModelConfig`.

**The final report is not written from here, and not carried out from here either.**
`Pipeline.Run` returns it to `package main`, which owns it.
The model receives a copy through a completion message purely to render the findings browser,
and `package main` writes it to stdout after the bubbletea program returns.

It arrives as a bubbletea message rather than a pipeline event on purpose:
the event channel drops under load, and a dropped completion would park the TUI on the agent panes forever.
That split is also what makes the handoff testable without a terminal.

**The run ending is not a reason to quit, and the model must never quit itself.**
The channel closing yields `eventsDone`, which stops the read loop and does nothing else; `CompletedMsg`
opens the findings browser. Quitting is the reader's decision, or `package main`'s when the run failed and
there is nothing to browse. The wait for the program to return is what keeps the report off stdout until the
terminal is free — writing it while the TUI still owns the screen interleaves it with the final frame.

**Only `ctrl+c` ends a review that is still running.**
`q` quits once the report is in and is inert until then; `esc` never quits at all — it abandons a filter and
it leaves the input viewer, and that is the whole of it.
A reader who hits esc to back out of something, or q expecting a pager, would otherwise lose the view of a
live run, which is the one thing this binary exists to keep.
The `ctrl+c` check sits at the top of `key`, ahead of the filter editor that treats every other key as text,
so a half-typed query is never a trap.

### Output streams

Render to the **tty**, never to stdout. stdout belongs to the report alone.

Gate the TUI on the tty being openable, **never** on stdout being a TTY.
With `revmux > findings.json` the stdout check is false while the user is sitting at a terminal
expecting to watch the run — that check would silently disable the TUI in one of the most common invocations.

That same tty handle is the program's **input** as well as its output: pass it to both `tea.WithInput` and
`tea.WithOutput`. Leaving input at bubbletea's `os.Stdin` default makes the key bindings work only when
stdin happens to be a terminal, which is not safe for a binary whose caller is a model, and it is unrelated
to the condition the gate actually tests. `package main` opens it, hands it over, and closes it after the
program returns.

`--no-tui` and a non-openable tty both fall back to the plain stderr renderer over the same event channel.

### Layout

A status table on top, one row per supervised process: name, state, elapsed, last activity.
The roster fills it first and `Model.agent` opens a row for any other name an event carries, so the
synthesis and verify processes appear as they start — the table shows what is running, not only what the
profile named.
Below it, one focused detail pane, switched with tab and number keys.

**The header's findings total is not the sum of those rows.**
A roster agent adds to it and a stage process replaces it, because synthesis merges what the finders already
reported: summing the two counts every finding twice and shows a number that is neither the raw total nor
the merged one. `Model.rostered` reads the roster to tell them apart — never the status rows, which grow to
cover both.

- Tab `1 all` is the combined chronological view and is **focused by default**.
  Tabs are labeled from one, because that is what a reader types, and the label carries the exact key:
  `1`-`9`, then letters. **The letter set omits every key already bound** — `f`, `g`, `h`, `j`, `k`,
  `i`, `l`, `q` — because those match before the token lookup is reached, so a tab assigned one would be
  unreachable, and only on a run with enough panes to get that far. One character always: a two-digit
  token costs a column on every tab and reads as two numbers beside a name that may end in one.
  It is deliberately compact: tool calls, state transitions and findings emitted, one line each, agent-prefixed and colored.
  The color arrives on the agent's spec — `color` front matter, or a palette entry by roster position when
  it is omitted (`.claude/rules/prompts.md`). This package never picks one, or the plain `--no-tui`
  renderer would color the same agent differently.
  A process the roster does not name — a stage, a verify group — takes its color from
  `prompt.DerivedSpec`, which both renderers call. It hashes the name rather than counting arrivals,
  because the two renderers build their rows independently and a derived agent is created on first
  sight, so an index would have to be threaded through and could disagree.
  **Every pane renders the markdown a model writes and wraps what will not fit** — the combined log,
  each agent's scrollback and the findings browser alike, every row of it including the browser's
  headings and titles.
  **There are two renderers, and which one a pane gets follows from what the pane holds.**
  A pane whose unit is one line — the combined log, an agent's scrollback, the browser's title,
  location and attribution rows, and the plain `--no-tui` renderer — renders inline spans with
  `markdown()` and breaks lines with `Wrap`.
  A pane whose unit is a *document* — an input file, and a finding's body and fix — renders through
  glamour in `mdrender.go`, which does its own wrapping.
  **The document path has a size ceiling, `mdMaxDoc`, and above it the inline path takes over.**
  glamour's cost is structural rather than content-dependent: its `Document` style carries a color, so
  every emitted row is padded to the wrap width with one escape pair per trailing space and the output
  runs many times the source. The render is synchronous on the bubbletea `Update` goroutine and the
  result is then held by the `lines` cache, so a large input both stalls the loop — during which the
  event channel drops — and is retained. The snapshotter's own 1 MiB-per-file limit was sized for a
  line-at-a-time renderer and does not bound either cost, which is why the ceiling lives here.
  The fallback is the same inline renderer the log panes use, so an oversized input stays fully
  readable rather than blank or truncated.
  **The log panes keep the inline path deliberately.** They are one line per event with a timestamp and
  an agent prefix; handing that to a document renderer costs exactly the one-line-per-event view that
  makes the combined log the situational-awareness pane it exists to be. Do not "finish the migration".
  **There is one `Wrap`, in `app/ui`, and there were three.** Separate copies in the log, the browser
  and the plain renderer had already diverged — two measured display width and one counted runes, so
  the same text broke differently depending on which pane it landed in. It stays the one line-breaker
  for every caller on the inline path, and it is exported because `app/progress.go` lives in
  `package main`.
  It walks runes, never bytes: trimming a byte at a time while measuring display cells exits inside a
  multi-byte rune, and since markdown rendering runs first the text also carries ANSI, so a byte cut
  can land inside an escape and spill it as literal characters. A model writes backticks and emphasis into
  its prose whichever pane it lands in, and a forensic view is the last place to throw the end of a
  line away, since it is where a reader went looking for the detail.
  Headings keep their hashes on **both** paths rather than having them stripped: the pane is showing a
  markdown document and a reader with the report open beside it should see the same thing.
  glamour's own style breaks that at exactly one level — it renders h1 as a padded band while h2 and h3
  keep their prefixes — so `mdStyle` restores `H1.Prefix` and drops the band rather than trading the
  rule away.
  Long entries wrap with a hanging indent rather than being clipped: a narrated step or a command is
  the informative part of the log and the part most likely to run long, and continuation rows carry no
  timestamp and no agent name so the entry still reads as one thing. The plain renderer wraps the same
  way, for the same reason both renderers do everything else the same way.
  It must stay that compact — four concurrent agents scrolling their full reasoning would run faster
  than anyone can read, and it would stop being the situational-awareness view it exists to be.
- The tabs after it are per-agent full-detail scrollback. Those are the forensic views.
- `i` switches the detail area to the startup input snapshot while the status table keeps updating.
  Input tabs are scope, goal, profile and each context file in lexical relative-path order.
  Missing optional inputs keep a tab that says they were not provided.
  Markdown files render as documents through glamour and other safe UTF-8 files render verbatim.
  Tables, rules, links, emphasis and fenced code all render as themselves; tabs are expanded per line
  before the document is handed over, since `expandTabs` carries a running column that a whole-document
  call would never reset on a newline.
  **The stop it expands at is four for a document and eight for a verbatim row, and the two are not
  interchangeable.** Which one a caller wants, and what breaks when it takes the other, is written on
  the two constants in `tabs.go`.
  `i` or `esc` restores the review tab and scroll position.
  Review and input navigation are independent.
  Completion does not interrupt an open input; it makes findings the review tab restored on return.
  The snapshot never refreshes and is limited to 1 MiB per file, 8 MiB total, 128 context files and
  1024 traversed filesystem entries.
  Binary, unsafe, unreadable and truncated files remain visible as non-fatal notices.
  Directory symlinks are listed but never traversed.
- **The tab bar measures itself and degrades before it is clipped.** There is no horizontal scroll on
  that line, so clipping alone cuts the rightmost tabs mid-word and a reader cannot tell how many
  panes exist or what is past the edge.
  The rule is **collapse the fewest names that fit, and at each count keep the padding if it still
  fits** — a search, not a one-way ladder. That ordering is what puts the padding ahead of the first
  name, since a padded bar one name down is only tried after the tight bar with every name has
  failed; it also lets the padding return once a further name has been spent, which is the right
  trade, because two spaces per tab buys less than a word. Collapse the fewest that fit: dropping
  every name the moment the bar is one column over throws away information nothing asked for.
  Names go from the left, because panes fill left to right as a run goes on and the right-hand end
  carries the recent work. Tab one is exempt entirely — `1 all` is four columns and is the view a reader
  falls back to from anywhere, so it is never the name that gets spent.
  **The focused tab keeps its name at every width the search can satisfy**, wherever it sits: its
  content is what fills the pane below, and a bare token there would leave the one pane on screen
  unnamed. Below that width the clip backstop cuts from the right and takes the focused name with
  everything else — collapsing is what keeps that rare, not a guarantee it never happens.
  A verify group is named for what it covers rather than for its position — `verify ui`, not
  `verify 3` — since a row has one column and "ui" tells a reader more than a number does. The label
  spelling out every directory stays as the archived prompt's filename, where the space exists.
- **The header degrades rather than being clipped, longest part first.** `statusTable` clips it, and
  the completion notice is the rightmost thing on the line — so the severity breakdown, the longest
  thing on it, would push "complete, closing in 5s" off the edge exactly when it matters. It gives up
  the breakdown, then the quit hint, then the agent count, then the stage, then the total, and clips only
  under all of that.
  The total outlives the stage: a reader who has lost the stage still learns whether anything was found,
  while a stage name with no count says only that something is happening.
  **While the run is live and the width allows, the header ends in `ctrl+c to quit`.** That is the only key
  that ends a running review, and q and esc are inert until the report is in — so a reader who reaches for
  either sees a frame that does not change, and the header is the one thing on screen that can tell him why.
  It outlives the breakdown rather than being spent first, because the two degrade differently: the total
  that replaces the breakdown keeps the worst severity in its color and the report restates the split at the
  end, while the hint has no shorter form and nothing else on screen carries it. On the shipped
  `comprehensive` roster that is the difference between a hint that survives a normal 80-column pane and one
  that disappears the moment the first finding lands — the full line is 86 columns, the same line with the
  total alone plus the hint is 55, and the input viewer adds 28 more to both.
  The hint goes at the third rung, so below roughly 55 columns in review mode the header carries run state
  only. The completion notice that replaces it sits outside the ladder entirely.
  **The count is rebuilt from the final report, not left as the last event's.** Verify moves rejected
  findings into `Immaterial` and `PreExisting` and `--min-confidence` filters, both without emitting a
  findings event, so a header fed only by events names severities the browser below it does not list.
  **Its color is the worst severity in it, never a fixed accent** — red on any critical, yellow on any
  major, green only when nothing above minor was found. Green is what a reader takes as a verdict on the
  run, and a fixed one paints a review that turned up a critical exactly like a clean one.
- On completion the model switches to the findings browser unless the reader is inspecting an input.
  In that case the input stays visible and returning to review mode opens findings.
  Agent tabs stay reachable so a reader can check *why* a finding was raised.
- **The browser renders the report and nothing more.** It lays out what the rendered report carries
  — a severity heading, then each finding as its title, where it is, its body, its fix and its
  attribution — wrapped to the pane rather than clipped at its edge, since a body is prose several
  sentences long and the tail is not the disposable half.
  A cursor, per-row folding and expand-on-demand were all built here and all removed: each put part of
  the review behind a keystroke and added state that had to be kept in step with the pane's own
  scrolling. What is left is the pane's scrolling and a filter. It opens at the top, not at the newest
  line — that is right for a log and wrong for a report.
  **Every finding is on screen showing its body, fix and attribution, and there is no key that hides
  any of it.** A summary line is an index entry, and a browser that lists nothing else puts the whole
  review behind one keypress per row.
- **A finding's body and fix are documents, and they are rendered separately.** A model writes lists,
  tables and fenced snippets into them, so they go through glamour rather than the inline renderer.
  Never compose the two into one document with the attribution under them: an unbalanced fence in a body
  would swallow everything below it.
  Each is cached on the finding's index into `rows`, never on its index into the filtered slice —
  keyed on the latter, typing a filter serves the previous finding's body at the same width.
- The browser's own rows — the severity heading, the title line, the location and the attribution —
  stay on the inline path and keep their raw ANSI, per the trap below: these are inline spans inside a
  line lipgloss later clips, so a nested lipgloss render would end in a reset that clears the enclosing
  style.

### lipgloss and ANSI traps

- `lipgloss.Render()` emits a full reset (`\033[0m`), which kills an enclosing style's background.
  For a styled substring inside a lipgloss container — a status separator, an agent-name prefix, a severity chip —
  emit raw ANSI sequences instead.
  Never call `lipgloss.NewStyle().Render()` for an inline element inside a lipgloss-rendered parent.
- As built, that leaves **lipgloss doing measuring and clipping only, never color**: `lipgloss.Width` to size
  the status column and `MaxWidth(...).Render` to clip a pane line, because both have to count display cells
  while ignoring the ANSI a colored line carries. Every color this package paints itself is raw SGR.
  **The one carve-out is glamour's output**, which arrives already colored and is passed through
  untouched: it is a whole document rather than a span inside a styled line, so its own resets close
  nothing but themselves.
  lipgloss's default renderer also reads its color profile from **stdout**, which is not where the TUI
  writes, so a color decision made through it would be taken against the wrong stream.
- **glamour reads stdout too, and only on one path.** `getDefaultStyle` consults `os.Stdout` and
  `termenv.HasDarkBackground` when the style is `AutoStyle`; an explicit style name skips it entirely.
  So the renderer is always built with `WithStyles` and `WithColorProfile` and never with
  `WithAutoStyle` or `WithEnvironmentConfig`.
  Both facts come from the one lipgloss renderer `newStyles` already builds against the tty, which is
  why `styles` carries `profile` and `dark` rather than a second renderer being built in `New`: two
  answers to "what can this terminal do" can disagree, and then the frame and the panes inside it do.
  With `ModelConfig.Output` nil — which happens only in tests — both fall back to
  `lipgloss.DefaultRenderer()` and are therefore taken against stdout. Production always passes the tty,
  so that is a caveat of the test path, not a defect.
  **`HasDarkBackground` is a terminal round-trip, not a lookup**: termenv puts the tty in raw mode,
  writes OSC 11 and CSI 6n, reads the reply back and discards everything before the first ESC — so it
  can swallow a keystroke already sitting in the buffer. `newStyles` runs it once at construction,
  before the bubbletea program owns the terminal, which is the only moment where that is safe. This is
  the one place in this package that talks to the terminal at all, and it stays a single call in
  `newStyles` rather than being reached for again from anywhere else.
  **glamour's h1 needs a second override the background choice does not cover.** Both of its styles
  spell h1 as color 228 on background 63; `mdStyle` clears the band to keep the markdown hash, so it
  clears the foreground with it, or a light terminal draws near-white text on white.
- **glamour deletes raw HTML, and CommonMark calls far more things raw HTML than a reader does.**
  `ansi.NewRenderContext` hardcodes a bluemonday `StrictPolicy` and every inline and block raw-HTML node
  goes through it, so `<task>`, `<T>` and `<binary>` are stripped out of a rendered pane with no marker
  that anything was there — a path template or a type parameter disappears mid-sentence.
  The inline path keeps that text verbatim, so the two renderers would disagree about what survives, and
  a forensic view silently dropping text is worse than clipping it.
  `mdRenderer.escapeHTML` entity-escapes those spans ahead of the render, so glamour prints them instead.
  **The spans come from a goldmark parse, never from a scan for angle brackets.** Escaping every `<`
  reaches into fenced and indented code, where an entity is literal text and `&lt;` is what a reader
  gets; escaping `>` with it turns a blockquote marker into text. A raw-HTML node is none of those by
  construction — goldmark has already ruled out the code span, the code block, the autolink and the
  marker. glamour exposes no option to disable the stripper, so the source side is the only lever.
- The agent-name painter is `prompt.AgentSpec.Paint`, not a helper here.
  Both renderers call it, which is what makes one agent one color in the TUI and under `--no-tui`.
- Pane rendering and viewport padding emit plain spaces after a reset, so themed panes show the terminal's
  default background in the gaps. Pad lines to full width before assembly.
- A factory returning a typed nil pointer through an interface return type produces a non-nil interface.
  Guard explicitly: `if x == nil { return nil }`.

### Events

- Events carry the agent name; route on that, never infer an owner from message content.
- A new `EventKind` needs a case here **and** in the plain renderer, or it is invisible in one of them.
- The model must tolerate events for an agent it has not seen yet and events arriving after an agent finished.
  Ordering across concurrent agents is not guaranteed.
- Never block on the event channel inside `Update` — a slow render must not stall the pipeline.
- **Elapsed time is measured between event timestamps, so this package takes no clock.**
  `Event.At` is stamped by the pipeline off the injected clock. Reading a clock here would be OS work by
  the definition above, and it would make the displayed elapsed disagree with the timings the archive
  recorded.
  **Which two timestamps depends on whether the agent has finished, and both halves are load-bearing.**
  A running agent is measured to `Model.now`, the newest event time anywhere in the run, so any agent
  speaking advances every row — measured to its own last event instead, a row freezes the moment it goes
  quiet, which is exactly when a reader wants to know how long it has been quiet for. A finished one is
  measured to its own last event, because measuring it to the run makes every completed row climb toward
  the run's age until a reader can no longer tell the finder that took forty seconds from the one that
  took four minutes. Getting either half wrong is invisible: nothing fails, a reader is just shown a
  number that is not true.
- **An agent's clock starts when it starts running, not when the run announces it.**
  `EventAgentStarted` is emitted after the stagger slot is acquired, so a row counts the time it spent
  working rather than the time it spent queued. Emitted on entry instead, every agent's clock starts at
  once and every row reads the same elapsed early on — which is the opposite of what the stagger exists
  to make visible. The roster shows an agent as `waiting` until then.

### Receivers

Keep receivers consistent per type.
A value receiver on a state sub-struct copies scroll and filter state on every render, which is both
wasteful and a source of "why did my mutation vanish" bugs.
Mutating and reading methods on the same state struct should both take pointers.

### Testing

Drive `Update` with synthetic messages and assert on `View()` output.
Never drive a real terminal, never spawn a process, never require a tty in a test.
