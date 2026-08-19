---
paths:
  - "app/executor/**"
---

## Executor — supervised subprocesses

Every rule below was learned by running these CLIs under supervision in anger.
They read like trivia until one of them bites, and each one costs a debugging session to rediscover.

### Verified `claude` CLI behavior

Measured directly against the real CLI. Do not re-derive, and do not assume any of it from documentation.

- `--json-schema` works together with `--output-format stream-json`.
  The model is forced through a `StructuredOutput` tool call,
  and the terminal `result` event carries a **pre-parsed `structured_output` object** alongside the raw `result` string.
  Read `structured_output`. Never scrape JSON out of prose, and never parse the `result` string when the object is present.
- **`--json-schema` takes the schema JSON itself as the argument value, never a path to a file holding it.**
  Passing a path fails immediately with `--json-schema is not valid JSON: JSON Parse error: Unrecognized token '/'`,
  and since a failed launch writes nothing to stdout, it looks exactly like an agent that produced no findings.
  `Request.Schema` therefore goes in as `string(req.Schema)`.
- The stream carries a typed `rate_limit_event`:
  `{"type":"rate_limit_event","rate_limit_info":{"status","resetsAt","rateLimitType","overageStatus"}}`.
  Use it. It is strictly better than matching error strings.
- The `result` event also carries per-model `usage`, `ttft_ms` and `permission_denials`.
  Read the token counts off `usage` — never estimate or recompute them.
- **`permission_denials` must be reported, and it is the one field whose absence is silent.**
  Each entry is `{tool_name, tool_use_id, tool_input}` — `tool_input.command` for a Bash denial.
  A refused tool call does not fail the agent: it writes a sentence in its own prose
  ("Bash is denied in this mode, so I'll work from the files directly") and continues with less,
  so the review narrows to whatever the permission layer allowed and still returns a full-looking
  report. Unread, the only trace is that sentence in one agent's scrollback.
  It goes out as `EventInfo`, for the same reason the codex banner does — it must not release the
  stagger gate. See `.claude/rules/pipeline.md`.
- **`--model` can be silently ignored.**
  A run with `--model haiku` actually executed `claude-sonnet-4-6`.
  Always read the model back from `modelUsage` in the result event and report what actually ran.
  A roster full of per-agent model pins is a claim, not a fact, until it is verified this way.
- **`--json-schema` makes the model silent unless the prompt asks otherwise, and that has to be asked for.**
  The schema forces every conclusion through a `StructuredOutput` tool call, so a review agent's text
  blocks come back all but empty and the run log is a wall of tool names with no sign of what is being
  investigated. Its thinking is no substitute: the blocks arrive with `thinking` empty and only a
  `signature`, so the plaintext never reaches the CLI at all. Text blocks are the only channel that
  carries what the agent is doing — `--include-partial-messages` streams bytes through the emit, but
  they are the answer being written, not commentary on it.
  Measured on one task with one schema, run twice: bare, 2 text blocks against 8 tool calls; with the
  narration instruction appended, 7 text blocks against 7 tool calls, same 8 findings. It costs
  nothing and it is the only way to get a claude agent to say what it is doing.
  That is `ClaudeNarrationContract`, appended by the executor and exported so the archive records it —
  the same shape and the same reason as `CodexOutputContract`. Codex needs none of it; its reasoning
  arrives in the rollout.
- Even a trivial call carries substantial input tokens — system prompt, project instructions, skills listing.
  Worth reporting per agent, which is all revmux does with it.

### Flags every claude invocation carries

```
claude --print --output-format stream-json --verbose
       --model <m> --effort <e>
       --permission-mode auto
       --disallowedTools "Edit,Write"
       --disable-slash-commands
       --no-session-persistence
       --include-partial-messages
       --json-schema <findings schema>
       < prompt
```

- `--permission-mode auto` so a headless run never blocks on a permission prompt.
  **Not `dontAsk`, which was measured and is wrong here.** `dontAsk` denies anything that would have
  prompted, and revmux passes no `--allowedTools`, so every Bash call an agent makes is refused —
  including the `git diff` the whole design depends on, since `CLAUDE.md` has agents run diff commands
  themselves. The agent does not fail: it says "Bash is denied in this mode" and reads files instead,
  so a diff-scoped review silently becomes a whole-tree review with the delta guessed. Measured
  directly: `dontAsk` with no allowlist denies, `auto` allows.
- `--disallowedTools` removes the edit tools from the agent's context — revmux never modifies source.
  This is best-effort, not a sandbox; the prompt must state the constraint too, and every shipped
  profile and stage prompt does.
  **Keep this list to the edit tools. Do not grow it into a Bash denylist.**
  `auto` was measured allowing `echo ... > main.go` and `rm -f main.go` to delete a tracked file, so
  the exposure is real — but a shell can write through a redirect, which is syntax rather than a
  command and cannot be denied by name at all, so a Bash denylist buys a fraction of a guarantee while
  costing real capability. `Bash(sed:*)` was tried and reverted: `sed -n '10,20p'` is an ordinary
  read-only way to print a range, and denying it takes investigation away to stop nothing.
  The read-only guarantee here rests on the prompt and on the agent having no reason to write.
  Anything stronger needs a read-only checkout, which is a different design.
- `--disable-slash-commands` prevents a lens agent invoking a skill that spawns its own subagent,
  which would put an agent inside the agent.
  The call site cannot prevent that any other way — it is a property of the invoked skill, not of the caller.
- `--no-session-persistence` avoids leaving one saved transcript per lens per run.
- **`--include-partial-messages` is armour for the idle watchdog, not something revmux decodes.**
  Without it the `StructuredOutput` tool call reaches stdout as a single line written only once it is
  complete, so a large answer means minutes with no byte on either pipe while the agent is working —
  measured killing synthesis twice at exactly 120.2s as it merged 39 findings, then failing the run.
  With it the tool input arrives as `content_block_delta`/`input_json_delta` chunks throughout, which
  is the one liveness signal that window has: the narration contract cannot reach inside a tool call
  the model is still composing, so no prompt wording closes this.
  The decoder needs no case for the new `stream_event` type and must not grow one — the touch is on
  raw bytes in `teeReader.Read`, and `parseStream`'s switch ignoring an unknown type is what keeps the
  deltas out of the log. The cost is archive size, and it is not small: measured at 80-84% growth per
  agent tee, where the deltas are 44-46% of the file. Most of it is `thinking_delta`, which carries
  plaintext reaching the tee through this flag alone — the assistant blocks arrive with `thinking` empty
  and only a `signature` — and the rest is every byte of text and tool input carried a second time
  under roughly 130 bytes of JSON wrapper per chunk.
- The prompt goes in on **stdin**, never as an argv positional.
  Windows `cmd.exe` caps a command line at 8191 characters and a composed lens prompt blows past that instantly.

### Child environment

- **Always strip `CLAUDECODE` from the child environment.**
  revmux is often launched from inside an AI coding session, so the variable is inherited
  and the child refuses to start as a nested session.
  There is no case where passing it through is correct.
- Strip `ANTHROPIC_API_KEY` by default so the child uses the interactive subscription auth rather than per-token billing.
  Expose a `--preserve-anthropic-api-key` escape hatch for users who authenticate by API key.
- Never pass `--bare`. It forces API-key auth and skips project-instruction discovery,
  which changes billing and strips the project context every lens agent depends on.

### Idle timeout and hard timeout

Two independent timers catching two different failures.

- **Idle timeout** is the one that matters: derive a cancellable context, arm `time.AfterFunc(idleTimeout, cancel)`,
  and reset it on **every output line** through a touch closure passed into the stream parser.
  When the derived context is canceled but the parent context is alive, that is an idle timeout, not an error —
  set `Result.IdleTimedOut` so the caller can retry rather than fail the run.
- **Every source of life touches it — stdout, stderr and the rollout.** None of the three is noise.
  Codex writes stdout only when it answers, and a modern one streams no reasoning on stderr either: on
  a long review both pipes are silent for minutes while only its rollout file moves. A watchdog missing
  any of the three kills a healthy codex run at the idle timeout, retries it into the same wall, and
  degrades the source that was working. Observed for stdout-only, then observed again for pipes-only.
  The touches are serialized against each other: stdout is read on the calling goroutine, stderr in its
  own, and the rollout tail in a third, so a `Timer` implementation is not required to tolerate
  concurrent callers.
  The hard timeout is what bounds a process that chatters forever without answering.
  Claude has two sources rather than three, and its blind window is its answer rather than its
  reasoning: see `--include-partial-messages` above, which is what fills it.
- **Hard timeout** is a plain `context.WithTimeout` over the whole call,
  catching the slow-but-alive case where the agent keeps emitting output forever.
- Both default to disabled at the executor level; the composition root sets them from config when it builds
  each executor. Not the pipeline — it never constructs one, it receives an injected factory.
- **Draining stderr outlives the line reader that consumes it.**
  A read fault or a line past `maxLineBytes` ends `bufio.Scanner` for good, and returning from the drain
  goroutine there leaves the pipe unread — which fills, blocks the child mid-write, and hangs the stdout
  parse until a timeout kills a run that was working.
  The remainder is copied to `io.Discard` instead: liveness is gone with the scanner, so the run rides on
  the stdout tick and the two timeouts, but the child keeps moving.
  A canceled run skips it — the process group is already being torn down and the pipe closes with it.
- **Both timers come from an injected clock, never from `time.AfterFunc` directly.**
  `.claude/rules/testing.md` forbids wall-clock waits in tests, and an idle-timeout test that actually sleeps
  is either slow or flaky. A recorded fixture that simply ends is EOF, not a stall — proving the watchdog
  fires needs a fake runner that emits fixture bytes and then blocks until cancellation, plus a clock the
  test advances itself.

### Display width belongs to the renderers

The decoder emits text; how much of it fits is decided where the terminal width is known. `textLimit`
is a sanity bound against a pathological block, not a column count.

**Both renderers clip, and that is what makes this safe.** `app/ui` clips through lipgloss against the
width bubbletea reports; `app/progress.go` has no such width reported to it, so it measures the terminal
itself — `COLUMNS` first, then `term.GetSize` on its own writer, then a fallback for a redirected
stderr. Without that half, moving the cut out of the decoder just replaces a bad truncation with an
unbounded line on stderr.

Cutting in the decoder destroys what no renderer can recover: a Bash command lost its tail at a
constant 60 columns however wide the terminal was, and an assistant block cut at its first line lost
everything after a lead-in — a closing summary reduced to "Summary of what I checked and found:".
A multi-line block is therefore **flattened**, not truncated, since an event is one line by
construction. The one deliberate exception is `rolloutTitle`, which keeps only the first line because
some codex versions append a whole paragraph after the bold title.

**Thinking is not progress and is never emitted.** The blocks arrive with `thinking` empty and only a
`signature`, so a bare "thinking" line reported nothing at all while taking a line of the log. It
existed as a heartbeat for a stream with no other sign of life; the narration contract is what fills
that role now.

### Exit codes are not log lines

`exit 0` is emitted nowhere. The pipeline emits its own done event carrying what the agent actually
produced, and a bare "exit 0" beside it is the last thing a reader sees under an agent that just
finished — pure noise in the one position that should carry the outcome.
Only a non-zero exit emits `EventFinished`, where the code is the point.

That makes the event's absence meaningful, so a test asserting anything about reaping order needs a
failing process to have something to order against. The `fail` helper mode exists for that.

### Process groups

- Set `SysProcAttr{Setsid: true}` **before** `cmd.Start()`.
  `Setsid` (not just `Setpgid`) fully detaches the child from the controlling terminal,
  preventing SIGTTIN/SIGTTOU from stopping the child's process group when a descendant touches terminal I/O.
- Kill the whole **process group** (`-pid`), SIGTERM first, then SIGKILL after a short grace delay.
  Killing only the direct child leaves node subagents and MCP servers orphaned, and they accumulate across a run.
- Kill on normal exit too, not only on cancellation — that is what reaps the orphans.
- Return early on `ESRCH` from SIGTERM so a normal exit does not pay the grace sleep on every call.
- Guard both the wait and the kill with `sync.Once`; the wait must be safe to call repeatedly.

### Shared base, thin executors

`Claude` and `Codex` both need the same run loop, idle watchdog, process-group teardown and line reader.
Duplicating that gives two near-identical `Run` bodies, which `dupl` will fail in lint.

Put the shared machinery on an unexported `proc` struct that both embed.
Each executor supplies only its own `args()` and its own output parsing.
Model and effort belong on the **per-run request**, not on construction-time options —
a single executor instance has to serve roster entries with different models.

### Raw output belongs to the caller, not just the parser

`Request` carries an optional `RawOutput io.Writer`, and `proc` tees every byte to it **before** parsing.

Without this the archive cannot do its job. Raw stdout is consumed inside `proc` and the per-executor
parsers, so a caller holding only parsed events can never reconstruct byte-identical claude stream-json or
codex prose. Re-serializing parsed events is not the same artifact: a reflection agent reading a paraphrase
of what the model emitted is worse off than one with no data, because it cannot tell the difference.

Tee before parse, not after — a stream that fails to parse is exactly the one worth having on disk.

### Codex differences

Codex is a peer executor, not a special case in the pipeline — but the executor itself is genuinely different.

- **Codex has no `stream-json` equivalent, and its stdout stays empty until it answers.**
  A review agent spends minutes reading and reasoning with not one byte on stdout, so anything watching
  that stream alone shows a banner and then nothing for the whole review.
  Assistant text and tool dispatch land only in its session rollout file, whose path derives from a session id
  printed in the stderr header banner. `app/executor/rollout.go` tails it: stderr is mined for the id and
  nothing else, the file is found by globbing `~/.codex/sessions/*/*/*/rollout-*-<id>.jsonl`, and the tail
  outlives the process so the last reasoning step and the answer are not lost.
  Never parse stderr prose for activity — that is what the rollout exists to spare.
- **The glob repeats on every pass; a single look at the banner is a race, and it loses.**
  Codex prints `session id:` *before* it creates the rollout — measured at 32ms between the two — so the
  tail is asked for a file that is not there yet. Returning at that first miss made a codex source silent
  for an entire eleven-minute review while its rollout filled beside it with 28 reasoning records and 12
  tool calls, and one silent source is indistinguishable from a codex that never worked.
  The tail therefore keeps looking until the run's context ends. Nothing is lost by finding the file a
  poll interval late: the read starts at offset zero, so the records written before it was found are
  still reported.
  A test for this must assert the tail has **not** returned before the file is created. Asserting only
  that the records eventually arrive races the test's own write against the tail's first glob, and a
  single-glob tail passes whenever the write wins — which is most of the time. That test was written
  that way first and let the reverted code through.
- **Take reasoning from `response_item`/`reasoning` only.**
  Codex writes an `event_msg`/`agent_reasoning` record for the same step as well, so handling both reports
  every step twice. Only the first line of a summary is used: some versions append a whole paragraph after
  the bold title, and forwarding it reintroduces the flood the rollout is read to avoid.
- **A shell tool call whose command cannot be recovered is dropped, not reported by name.**
  `exec`, `exec_command` and `shell` carry the command in the payload, so the name on its own says only
  that codex called a tool. Most calls yield a command — `arguments` carries `cmd`, or `cmdPattern`
  matches it out of a `custom_tool_call`'s snippet — but codex composes some multi-command snippets as a
  JS array with no `cmd` key at all, and those left a bare `exec` in the activity column for minutes.
  Every other tool keeps the name fallback: `apply_patch` and `update_plan` name what the agent is doing.
  Dropping is safe because liveness never rode on these events — the tail touches the idle timer whenever
  the file advances, never from the sink, and a codex leader releases the stagger gate on its first raw
  stdout write.
- **Codex has no `--json-schema`.**
  The executor appends its own "return only JSON matching this shape" contract to the composed prompt,
  rendering `Request.Schema` inline. That field is set for **both** executors and carries the running
  stage's schema, so a codex entry running synthesis or verify asks for that stage's shape.
  Hardcoding a finder-shaped contract here breaks the moment a profile resolves a stage onto codex.
  The wrapper text lives in the executor, never in a lens file, which must stay executor-agnostic.
- The idle watchdog ticks on raw stdout writes rather than parsed events, **on stderr lines, and on
  rollout records**. All three, and the third is not optional.
  A modern codex streams no reasoning on stderr at all: during a long review both pipes are silent for
  minutes while the rollout fills with the steps it is working through. A watchdog reading only the
  pipes kills a healthy run at the idle timeout, retries it into the same wall and degrades the source
  that was working — observed doing exactly that, with both tees zero bytes and the rollout showing
  the agent reasoning two minutes before it was killed.
  `proc` cannot see the rollout, so `runSpec.shareTouch` hands the touch out and the tail calls it
  **whenever the file advances** — never from the sink.
  Touching from the sink ties liveness to the display filter: a `function_call_output`, an `event_msg`
  or a reasoning record with an empty summary all move the file without producing an event, so codex
  could be demonstrably working and still starve the timer.
  The touch reaches the tail through an atomic, because the tail starts before `proc.run` exists to
  hand it over; the goroutine blocks on the session id first, so it is always stored before anything
  is read.
  **`Codex.Run` withdraws it before canceling the tail, and that order is the fix — not a comment on
  the final pass.** `run` stops the idle timer on its way out while the tail is still looping, so the
  pass at the top of that loop can otherwise re-arm a timer belonging to a finished run. Suppressing
  the touch only on the tail's own last pass leaves that window wide open.
  **A test must pin both directions of the advance guard.** Asserting only that the timer *is* reset
  passes a tail that touches on every poll — and a watchdog fed by its own polling never fires at all,
  which is invisible from outside. An empty rollout gives the negative case with no timing at all.
  A test for this must isolate the rollout as the only possible source of a reset — empty stdout and a
  single stderr line — or the pipes' own touches drown the signal and it passes with the wiring removed.
- Extraction must tolerate JSON wrapped in surrounding prose; finding no JSON is a degraded source, not a crash.
- Codex stderr is noisy — startup banner, exec echo, hook lifecycle lines, reasoning stream.
  Forward at most the resolved `model:` / `sandbox:` / `reasoning effort:` header lines, once per process, and suppress the rest.
  **Those go out as `EventInfo`, never `EventActivity`**: the banner prints before codex has contacted a
  model, and `EventActivity` is what releases the pipeline's stagger gate. See `.claude/rules/pipeline.md`.
  Once per process is a `seen` map keyed on the header name, not a position check:
  codex reprints the banner in some runs, and the guard is what makes "once" true.
  **stderr drains in its own goroutine alongside the stdout parse**, so where a forwarded header lands
  relative to the stdout events is unspecified — assert that a repeated header is forwarded once, never that
  it arrives at a particular index. A test doing the latter is flaky by construction and was.
- The resolved `model:` header is also where codex's actual model comes from, since there is no `result`
  event to read it off.
- **The `tokens used` footer on stderr is the run's token count, for the same reason** — there is no usage
  object anywhere else, and leaving it unread reports every codex source as having spent nothing.
  The marker and the number are separate lines, so the marker arms the read and a non-numeric line disarms it:
  codex echoes the whole prompt to stderr, and a lens body carrying those two words must not become a total.
  **Disarming alone is not enough, because an echoed prompt can carry the marker and a number on consecutive lines**,
  and a run that then fails never prints the real footer to overwrite it — so the failed attempt would be charged
  for whatever number the prompt happened to contain.
  The footer is the last thing codex prints, so the count is only reported when nothing followed it once stderr
  has drained. The last footer still wins; a count with content after it is treated as echo and dropped.
- Plan-quota errors arrive on **stderr with an empty stdout**, so a stdout-only error check misses them entirely.
  Only lines prefixed `error:` are read as diagnostics — that prefix gate is what keeps a reasoning stream
  discussing a rate limit from being mistaken for one.
  **The last such line wins, not the first**, for the same reason the token footer does:
  the echoed prompt precedes everything codex reports itself, so a first-match diagnostic names a line the
  prompt quoted — a lens body, a finding describing an error — as the failure,
  and hands `classify` a line that can carry a limit pattern the run never hit.
- `--sandbox read-only` always. revmux never lets an agent write.

### Verified `agy` CLI behavior

Measured directly against agy 1.1.15 (Google Antigravity; plan discovery ran 1.1.14 and every shape below matched).
The captures live in `app/executor/testdata/` as `agy-*.jsonl`; each fact names what it costs to rediscover.

- Non-interactive form: `agy --print "<prompt>" --output-format stream-json --sandbox
  --disable-slash-commands [--model <id>] [--effort low|medium|high] [--json-schema <schema>] --print-timeout <dur>`.
- **The prompt can reach agy on stdin, and that is the path that survives the Windows argv cap.**
  `agy --print "" --input-format stream-json --output-format stream-json` reads NDJSON lines from stdin;
  the accepted message is `{"event":"user","message":{"role":"user","content":"<prompt>"}}`,
  with `content` as a plain string or a claude-style `[{"type":"text","text":...}]` array — both measured working.
  The output stream is shape-identical to the argv-fed run (`agy-stdin-clean.jsonl` beside `agy-clean.jsonl`),
  and the process exits after the turn once stdin is closed.
  Getting the message shape wrong fails three different ways, and only one of them is loud:
  a line without `"event"` returns a result ERROR naming the field,
  `{"event":"user"}` without `"message"` likewise,
  but an unrecognized event name (`user_input`, `prompt`) produces **no result event at all** — the process sits until something kills it.
  **Bare `--print` with the prompt piped on stdin does not work**: the flag consumes the next argv token as the prompt.
  Measured: `--print --input-format stream-json ...` answered a question about `--input-format`, in plain text,
  which means the piped prompt and both format flags were all silently ignored.
- Output is agy's **own NDJSON dialect**, not claude stream-json. Three event kinds:
  `init` (cwd, `tools[]`, `permission_mode` — print mode showed `"always-proceed"` with no flag passed),
  `step_update`, and a terminal `result`. Unknown event kinds must be ignored, not errors.
- `step_update` carries `conversation_id`, `step_index`, `state` (`ACTIVE`|`DONE`|`ERROR`),
  `step_type` (observed: `user_input`, `checkpoint`, `agent_response`, `tool`, `system_message`, `finish`),
  plus optional `text_delta`, `duration_seconds` and a per-step `usage`.
- **An `agent_response` step can be DONE with no `text_delta` at all** — a thinking-only step before a tool
  call, carrying only `usage` and `duration_seconds`. A parser that treats every `agent_response` as text
  emits blank lines for half the steps of a tool-using run (`agy-tools.jsonl`, step 2).
- Tool steps: `step_type:"tool"` with `tool_name` and `tool_info{name, parameters, output?, error?}`.
  The command is recoverable from `tool_info.parameters` — `CommandLine` for `run_command`,
  `AbsolutePath` for `view_file`, `Pattern`/`SearchDirectory` for `find_by_name`.
  ACTIVE fires at dispatch with the parameters; the completion event repeats them and adds `output`,
  and a failed tool completes with `state:"ERROR"` and `error:{type:"TOOL_ERROR",message}` (`agy-toolerror.jsonl`).
- `--json-schema` works like claude's: the result carries a **pre-parsed `structured_output` object**
  plus a `json_schema` echo, and `response` duplicates the raw JSON string. Read `structured_output`;
  never scrape `response`. agy accepts a schema file path as well as inline JSON — pass inline anyway,
  for symmetry with claude (`agy-schema.jsonl`, recorded under `testdata/finder-schema.json`).
- **A schema-forced answer is a blind window, and agy has no `--include-partial-messages` to fill it.**
  A 4,225-character structured answer arrived as one `agent_response` DONE event with zero ACTIVE deltas
  before it — nothing on stdout while the model composes. Plain-text answers do stream ACTIVE
  `text_delta` chunks, and tool steps stream throughout, so a tool-using review has liveness between
  answers; the composing window itself rides on the idle timeout being generous and the hard timeout.
  Do not invent a third liveness source — there is none.
- Token counts come from the result `usage`:
  `input_tokens`, `output_tokens`, `thinking_tokens`, `cache_read_tokens`, `total_tokens`,
  where `output_tokens` already includes thinking (measured 87 output / 86 thinking on a one-word answer).
  Per-step `usage` exists too; the result is the total. **No event anywhere carries the actual model**
  (claude has `modelUsage`), so the manifest records the requested model only for agy.
- **`result.status:"ERROR"` does not imply a failed run.** A mid-run tool error
  (`read s1: resource temporarily unavailable` from a sandboxed `find /`) propagated into the terminal
  result as `status:"ERROR"` with an `error` string — while the process exited 0 and `response` carried
  the complete, correct answer (`agy-toolerror.jsonl`). Outcome mapping must weigh the exit code and the
  presence of a usable answer, not the status string alone.
- A bad model id (`agy-error.jsonl`): exit 1, and the diagnostic is a **single `result` event on stdout** —
  no `init` first — with `status:"ERROR"` and an `error` string that includes the available-model list.
  **stderr was empty in every capture, errors included**; agy's diagnostics ride the result event on stdout,
  so there is no stderr prose to mine and no codex-style `error:` prefix gate to apply.
  A rate-limit/quota error was not cheaply reproducible and no typed rate-limit event was observed;
  until one is captured, the error tier is the result `error` string.
- `--print-timeout` defaults to **5m0s** and is agy-internal — it would kill a long review well under
  revmux's own hard timeout. It accepted `24h` and `100000h` without complaint, so there is no practical
  maximum; the executor must pass a value derived above the hard timeout. Expiry (`agy-timeout.jsonl`):
  exit 1, terminal result `status:"ERROR"`, `error:"timeout waiting for response"`, with the step stream
  up to that point intact.
- **agy sets no nested-session guard.** Its tool children get `ANTIGRAVITY_LS_ADDRESS`,
  `ANTIGRAVITY_AGENTAPI_EXE`, `ANTIGRAVITY_CSRF_TOKEN`, `ANTIGRAVITY_CONVERSATION_ID` and
  `ANTIGRAVITY_SOURCE_METADATA`, and launching agy with all five set succeeds — measured, status SUCCESS.
  Nothing agy-specific to strip; still strip `CLAUDECODE` as always, since agy proxies claude models and
  revmux is often launched from a claude session.
- Under `--sandbox`, `run_command` executed in `~/.gemini/antigravity-cli/scratch`, **not the launch cwd**
  (measured: the agent's first `ls -la` listed an empty directory). Composed prompts must use absolute
  paths, which revmux's path-expanding variables already are.
- `agy models` ids look like `gemini-3.7-flash-{high,medium,low}`, `gemini-3.1-pro-{high,low}`,
  `claude-sonnet-4-6`; effort exists both as `--effort` and baked into some id suffixes.
  Pass `--effort` only when the `model:` string carried one, same as the other binaries.

### Error and limit patterns

claude gets its rate-limit signal from the typed `rate_limit_event`, so string matching is only needed
for codex and agy — codex's diagnostic is its last stderr `error:` line, agy's is the result event's
`error` string, and both feed the one shared `classifyFailure` tiering.
Where patterns are used, tier them: **retry → limit → error**.

- Retry tier covers transient server hiccups: `API Error: 529`, `502`, `503`, `504`.
  `500` is deliberately excluded — it can be a deterministic failure and belongs in the error tier.
- **Never match patterns against the whole output.**
  A review agent's findings text will literally contain the words "rate limit" and "API Error"
  when it is reviewing code that handles rate limits — including this project's own code.
  Check only the tail, and only when the process exited non-zero.
- Skip pattern checks entirely when the context was canceled — a canceled run's tail is meaningless.

### Cross-platform

- Platform-specific code goes in `foo_unix.go` (`//go:build !windows`) and `foo_windows.go` (`//go:build windows`).
- Methods split across build-tagged files are fine, so platform code does not force standalone helpers.
- Windows has no process groups; the stub kills the direct process only, and that is an accepted limitation.
- Verify with `GOOS=windows GOARCH=amd64 go build ./...` before shipping anything touching this package.

### Capturing real streams for fixtures

To learn what the upstream CLIs actually emit, do not launch `claude` or `codex` from an AI agent's own tool shell —
nested launches are commonly blocked by the host tool's permission layer, and the capture silently fails.
Run the capture in a separate, independent terminal session, redirect stdout and stderr to files, and inspect those.
`agy` is not a host tool here and launches fine from an agent shell — measured while recording the `agy-*` fixtures.
Recorded captures belong in `app/executor/testdata/` as fixtures; see `.claude/rules/testing.md`.
