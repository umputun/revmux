package executor

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// rateLimitRejected is the one rate_limit_event status that means the request was refused. The CLI's
// vocabulary is allowed / allowed_warning / rejected, and allowed_warning rides along on a successful
// response once utilization passes a threshold — treating it as a limit throws away a completed run.
const rateLimitRejected = "rejected"

// Claude runs the claude CLI in print mode and decodes its stream-json output.
type Claude struct {
	proc
}

// NewClaude builds a claude executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewClaude(runner CommandRunner, opts Opts) *Claude {
	return &Claude{proc: newProc("claude", runner, opts)}
}

// Run executes one request and reports what happened. A non-zero exit or an idle timeout comes back on
// the Result, not as an error — whether that degrades the source is the pipeline's call.
func (c *Claude) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	req.Prompt += ClaudeNarrationContract(req.Schema)
	spec := runSpec{
		argv: c.args(req),
		sink: sink,
		parse: func(ctx context.Context, r io.Reader) Result {
			return c.parseStream(ctx, r, sink)
		},
	}
	res, err := c.run(ctx, req, spec)
	res.RequestedModel = req.Model
	return res, err
}

// ClaudeNarrationContract asks the model to say what it is doing, and exists because --json-schema
// otherwise makes it silent: the schema forces every conclusion through a StructuredOutput tool call, so
// text blocks come back all but empty. Only with a schema — a schema-less call writes prose already.
// Exported for the same reason CodexOutputContract is: Run appends it after the prompt was archived.
func ClaudeNarrationContract(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	return "\n\nAs you work, narrate what you are doing. This is a running commentary read live by a " +
		"human watching the run, and it is separate from your answer, which goes only in the structured " +
		"output.\n\n" +
		"- Before each group of related tool calls, write one short line saying what you are about to " +
		"check and why: \"checking whether the stagger gate can still open on a fork\".\n" +
		"- When something turns out to matter, say so in one line as you find it.\n" +
		"- Keep going for the whole review. Do not narrate the opening few steps and then fall silent " +
		"for the rest of it — a reader who stops seeing lines cannot tell you apart from a hung " +
		"process.\n" +
		"- One line at a time, under a dozen words, and never a summary of what you already said.\n"
}

// args builds the invocation. The three context-trimming flags — the --tools allowlist,
// --strict-mcp-config with no --mcp-config, and --setting-sources project — cut what a reviewer
// carries before it reads anything, and they reach only this child. --tools is variadic, so it must
// be one argv element or a later positional is swallowed; it also drops Edit and Write, which is why
// no --disallowedTools follows, and Task, so a reviewer cannot fan out into subagents of its own.
// The numbers and what the child stops inheriting are in .claude/rules/executor.md.
// --include-partial-messages is there for the idle watchdog rather than for
// anything revmux decodes: without it the whole StructuredOutput tool call arrives as one line written
// after it is complete, so a large answer puts the stream past the idle timeout with the agent working —
// measured killing synthesis twice at 120s while it merged 39 findings. The deltas are the only liveness
// there is in that window; the narration contract cannot reach into a tool call the model is composing.
func (c *Claude) args(req Request) []string {
	argv := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "auto",
		"--tools=Bash,Read,Grep,Glob,WebFetch,WebSearch",
		"--strict-mcp-config",
		"--setting-sources", "project",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--include-partial-messages",
	}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if req.Effort != "" {
		argv = append(argv, "--effort", req.Effort)
	}
	if len(req.Schema) > 0 {
		argv = append(argv, "--json-schema", string(req.Schema))
	}
	return argv
}

// parseStream consumes the stream and returns what it learned. A line that fails to decode is skipped:
// a truncated stream must degrade to a partial Result rather than fail the run.
func (c *Claude) parseStream(ctx context.Context, r io.Reader, sink EventSink) Result {
	res := Result{}
	_ = c.readLines(ctx, r, func(line string) {
		ev, ok := c.event(line)
		if !ok {
			return
		}
		switch ev.Type {
		case "assistant":
			// two separate questions about one turn: what the model said, and whether it is alive.
			// prose is worth a scrollback line, a tool name is worth only a status cell, and a turn
			// that does both emits both.
			if text := ev.activity(); text != "" {
				c.emit(sink, Event{Kind: EventActivity, Text: text})
			}
			if note := ev.progress(); note != "" {
				c.emit(sink, Event{Kind: EventProgress, Text: note})
			}
		case "rate_limit_event":
			if ev.RateLimitInfo == nil {
				return
			}
			res.RateLimit = *ev.RateLimitInfo
			res.RateLimited = ev.RateLimitInfo.Status == rateLimitRejected
			if res.RateLimited {
				c.emit(sink, Event{Kind: EventRateLimit, Text: ev.RateLimitInfo.Status})
			}
		case "result":
			// a refused tool call is reported, never swallowed: the agent works around it silently
			// and the review narrows without anything else saying so
			if note := ev.denials(); note != "" {
				c.emit(sink, Event{Kind: EventInfo, Text: note})
			}
			res.StructuredOutput = ev.StructuredOutput
			res.ActualModel = ev.actualModel()
			res.Tokens = ev.tokens()
			res.TTFTMs = ev.TTFTMs
		}
	})
	return res
}

func (c *Claude) event(line string) (streamEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return streamEvent{}, false
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return streamEvent{}, false
	}
	return ev, ev.Type != ""
}
