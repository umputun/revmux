package executor

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// OpenCode runs the opencode CLI in headless JSON-stream mode and decodes its output. It uses the same
// output contract as codex — schema appended to the prompt as text — because opencode run has no
// equivalent of claude's --json-schema flag. Unlike codex, its stdout is structured JSON lines rather
// than prose, so JSON extraction is not needed: the final text event carries the structured answer.
type OpenCode struct {
	proc
}

// NewOpenCode builds an opencode executor. A nil Opts.Clock is filled with the production clock.
func NewOpenCode(runner CommandRunner, opts Opts) *OpenCode {
	return &OpenCode{proc: newProc("opencode", runner, opts)}
}

// Run executes one request. A non-zero exit or an idle timeout comes back on the Result rather than as
// an error — whether that degrades the source is the pipeline's call.
func (o *OpenCode) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	req.Prompt += OpenCodeOutputContract(req.Schema)
	// stderr carries nothing to read: opencode reports errors as stdout error events in --format json
	// mode, so no stderrLine is set here. proc still drains the pipe regardless, which is what keeps a
	// chatty child from blocking on a full one.
	spec := runSpec{
		argv:  o.args(req),
		sink:  sink,
		parse: func(ctx context.Context, r io.Reader) Result { return o.parseStream(ctx, r, sink) },
	}
	res, err := o.run(ctx, req, spec)
	res.RequestedModel = req.Model
	res.ActualModel = req.Model // opencode does not echo the resolved model; use the requested name
	return res, err
}

// OpenCodeOutputContract is opencode's substitute for claude's --json-schema, appended to every prompt
// Run dispatches. Exported because Run appends it after the caller archived the composed prompt, and an
// archived prompt missing it describes a run that did not happen.
func OpenCodeOutputContract(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	return "\n\nReturn ONLY a JSON object matching the schema below. No prose before or after it.\n\nSchema:\n" +
		string(schema) + "\n"
}

// args builds the invocation. --auto approves all permissions without prompting — required for headless
// use, since a prompt waiting for user input would stall indefinitely and fire the idle watchdog.
func (o *OpenCode) args(req Request) []string {
	argv := []string{"run", "--format", "json", "--auto"}
	if req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if req.Effort != "" {
		argv = append(argv, "--variant", req.Effort)
	}
	return argv
}

// parseStream consumes the JSON-line stream opencode emits and returns what it learned. A line that
// fails to decode is skipped: a truncated stream degrades to a partial Result rather than failing the run.
func (o *OpenCode) parseStream(ctx context.Context, r io.Reader, sink EventSink) Result {
	res := Result{}
	var lastText string // accumulate: the final text event is the structured answer
	_ = o.readLines(ctx, r, func(line string) {
		ev, ok := o.event(line)
		if !ok {
			return
		}
		switch ev.Type {
		case "text":
			if ev.Part.Text != "" {
				o.emit(sink, Event{Kind: EventActivity, Text: ev.Part.Text})
				lastText = ev.Part.Text
			}
		case "tool_use":
			if ev.Part.Tool != "" {
				o.emit(sink, Event{Kind: EventProgress, Text: ev.Part.Tool})
			}
		case "step_finish":
			res.Tokens += ev.Part.Tokens.Output + ev.Part.Tokens.Input
		case "error":
			if ev.Error != "" {
				o.emit(sink, Event{Kind: EventInfo, Text: ev.Error})
			}
		}
	})
	// the last text block is the model's answer; if a schema was present, it should be JSON
	if lastText != "" {
		res.StructuredOutput = o.extract(lastText)
	}
	return res
}

// extract pulls a JSON object out of the final text block. OpenCode answers with the schema-required
// JSON when the output contract is in the prompt, but may wrap it in a code fence or add prose.
// Returns nil when nothing parses cleanly — the pipeline degrades that source rather than crashing.
func (o *OpenCode) extract(text string) json.RawMessage {
	for i, ch := range text {
		if ch != '{' {
			continue
		}
		var out json.RawMessage
		if err := json.NewDecoder(strings.NewReader(text[i:])).Decode(&out); err == nil {
			return out
		}
	}
	return nil
}

func (o *OpenCode) event(line string) (openCodeEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return openCodeEvent{}, false
	}
	var ev openCodeEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return openCodeEvent{}, false
	}
	return ev, ev.Type != ""
}

// openCodeEvent is the subset of fields revmux reads from opencode's --format json stream.
type openCodeEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionID"`
	Error     string          `json:"error,omitempty"`
	Part      openCodePart    `json:"part"`
}

type openCodePart struct {
	// text event
	Text string `json:"text,omitempty"`
	// tool_use event
	Tool   string `json:"tool,omitempty"`
	CallID string `json:"callID,omitempty"`
	// step_finish event
	Tokens openCodeTokens `json:"tokens"`
	Reason string         `json:"reason,omitempty"`
}

type openCodeTokens struct {
	Total  int `json:"total"`
	Input  int `json:"input"`
	Output int `json:"output"`
}

