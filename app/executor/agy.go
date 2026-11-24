package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// agySettings is the per-run settings overlay. Denying write_file covers every file-writing tool,
// edit_file included, and a subagent inherits the rule. Allowing read_file(*) mounts the whole host
// read-only into the terminal sandbox: without it a git config including a file outside the sandbox
// fails every git command with exit 128. Tool-name rules such as invoke_subagent(*) are accepted and
// silently ignored, so none are listed.
const agySettings = `{"permissions":{"allow":["read_file(*)"],"deny":["write_file(*)"]}}`

// Agy runs the agy (Antigravity) CLI in print mode and decodes its stream-json output.
type Agy struct {
	proc
}

// NewAgy builds an agy executor. A nil Opts.Clock is filled with the production clock.
func NewAgy(runner CommandRunner, opts Opts) *Agy {
	return &Agy{proc: newProc("agy", runner, opts)}
}

// Run executes one request and reports what happened. A non-zero exit or an idle timeout comes back on
// the Result, not as an error.
func (a *Agy) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	line, err := a.input(req.Prompt + NarrationContract(req.Schema))
	if err != nil {
		return Result{}, err
	}
	req.Prompt = line
	// agy's idle timeout can differ from the other executors', so the value this run is killed at is
	// put on record; an info event, because it must not release the stagger gate
	if a.opts.IdleTimeout > 0 {
		a.emit(sink, Event{Kind: EventInfo, Text: "idle timeout: " + a.opts.IdleTimeout.String()})
	}
	structured := len(req.Schema) > 0
	spec := runSpec{
		argv: a.args(req),
		sink: sink,
		parse: func(ctx context.Context, r io.Reader) Result {
			return a.parseStream(ctx, r, sink, structured)
		},
	}
	res, err := a.run(ctx, req, spec)
	res.RequestedModel = req.Model
	return res, err
}

// input wraps the prompt as the one stream-json message agy reads from stdin. A plain stdin prompt is
// never read: --print takes the prompt as its value, and that would put it on argv.
func (a *Agy) input(prompt string) (string, error) {
	msg := struct {
		Event   string `json:"event"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}{Event: "user"}
	msg.Message.Role, msg.Message.Content = "user", prompt
	data, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("encode agy prompt: %w", err)
	}
	return string(data) + "\n", nil
}

// args builds the invocation. --print= is the empty prompt value stream-json input requires; stdin EOF
// after the one message ends the run.
func (a *Agy) args(req Request) []string {
	argv := []string{
		"--print=",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--disable-slash-commands",
		"--settings", agySettings,
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

// agyEvent is one line of agy's stream-json output. Only the fields revmux acts on are decoded.
type agyEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Model string `json:"model"`
	} `json:"init"`
	Step   *agyStep   `json:"step_update"`
	Result *agyResult `json:"result"`
}

// agyStep is one step_update. An agent_response streams its text as text_delta fragments over several
// updates of one step_index; a tool step reports ACTIVE once and then DONE or ERROR.
type agyStep struct {
	Index     int    `json:"step_index"`
	State     string `json:"state"`
	Type      string `json:"step_type"`
	TextDelta string `json:"text_delta"`
	ToolName  string `json:"tool_name"`
	ToolInfo  *struct {
		Parameters map[string]any `json:"parameters"`
		Error      *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"tool_info"`
}

type agyResult struct {
	Status           string          `json:"status"`
	Error            string          `json:"error"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            struct {
		InputTokens     int `json:"input_tokens"`
		OutputTokens    int `json:"output_tokens"`
		CacheReadTokens int `json:"cache_read_tokens"`
	} `json:"usage"`
}

// label is the tool's name with the one argument worth showing beside it.
func (s agyStep) label() string {
	if s.ToolInfo == nil {
		return s.ToolName
	}
	if arg := toolArg(s.ToolInfo.Parameters); arg != "" {
		return s.ToolName + " " + arg
	}
	return s.ToolName
}

// parseStream consumes the stream and returns what it learned. A line that fails to decode is skipped:
// a truncated stream must degrade to a partial Result rather than fail the run.
func (a *Agy) parseStream(ctx context.Context, r io.Reader, sink EventSink, structured bool) Result {
	res := Result{}
	texts := map[int]*strings.Builder{}
	_ = a.readLines(ctx, r, func(line string) {
		var ev agyEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil {
			return
		}
		switch {
		case ev.Init != nil:
			// agy reports no per-model usage, so the model it says it started with is all there is
			res.ActualModel = ev.Init.Model
		case ev.Step != nil:
			a.step(*ev.Step, texts, sink, structured)
		case ev.Result != nil:
			a.result(*ev.Result, &res, sink)
		}
	})
	return res
}

// step turns one step_update into events. Text is held until its step is done, because a delta is a
// fragment of a sentence and a log line per fragment is unreadable.
func (a *Agy) step(s agyStep, texts map[int]*strings.Builder, sink EventSink, structured bool) {
	if s.Type == "agent_response" {
		b, ok := texts[s.Index]
		if !ok {
			b = &strings.Builder{}
			texts[s.Index] = b
		}
		b.WriteString(s.TextDelta)
		if s.State == "ACTIVE" {
			return
		}
		delete(texts, s.Index)
		text := b.String()
		if structured {
			text = a.withoutAnswer(text)
		}
		if text = flattenLines(text); text != "" {
			a.emit(sink, Event{Kind: EventActivity, Text: clampRunes(text)})
		}
		return
	}
	if s.ToolName == "" {
		return
	}
	switch s.State {
	case "ACTIVE":
		a.emit(sink, Event{Kind: EventProgress, Text: s.label()})
	case "ERROR":
		// a refused or failed tool call does not fail the agent, it carries on with less, so the
		// review narrows silently unless this is said
		text := "tool failed: " + s.label()
		// the reason is what tells a missing file from a denied permission
		if s.ToolInfo != nil && s.ToolInfo.Error != nil && s.ToolInfo.Error.Message != "" {
			text += ": " + flattenLines(s.ToolInfo.Error.Message)
		}
		a.emit(sink, Event{Kind: EventInfo, Text: clampRunes(text)})
	}
}

// withoutAnswer cuts the structured answer off the end of a response. agy streams it as the text of
// its last step, often right after a closing line of narration in the same step; it is already on the
// result event, and as a log line it is a wall of JSON. The first brace whose remainder is one valid
// JSON value is the answer, since prose before it may mention braces of its own.
func (a *Agy) withoutAnswer(text string) string {
	trimmed := strings.TrimSpace(text)
	for i := strings.IndexByte(trimmed, '{'); i >= 0; {
		if json.Valid([]byte(trimmed[i:])) {
			return trimmed[:i]
		}
		next := strings.IndexByte(trimmed[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return text
}

func (a *Agy) result(r agyResult, res *Result, sink EventSink) {
	if r.Status == "ERROR" && r.Error != "" {
		a.emit(sink, Event{Kind: EventInfo, Text: clampRunes("agy error: " + flattenLines(r.Error))})
	}
	// a run that ends without an answer reports structured_output as null, which decodes to the four
	// bytes "null" and would pass every len()==0 check downstream as an answer
	if out := bytes.TrimSpace(r.StructuredOutput); len(out) > 0 && !bytes.Equal(out, []byte("null")) {
		res.StructuredOutput = out
	}
	res.Tokens = r.Usage.InputTokens + r.Usage.OutputTokens + r.Usage.CacheReadTokens
}
