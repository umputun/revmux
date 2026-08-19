package executor

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// agyDefaultPrintTimeout is what --print-timeout falls back to when the hard timeout is disabled. The
// flag has no practical maximum (100000h was accepted), so generous is safe; agy's own 5m0s default is
// not, since it would kill a long review well under revmux's supervision.
const agyDefaultPrintTimeout = "24h"

// Agy runs the agy CLI (Google Antigravity) in print mode and decodes its NDJSON output — agy's own
// dialect, handled by agyStream.
type Agy struct {
	proc
}

// NewAgy builds an agy executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewAgy(runner CommandRunner, opts Opts) *Agy {
	return &Agy{proc: newProc("agy", runner, opts)}
}

// Run executes one request and reports what happened. A non-zero exit or an idle timeout comes back on
// the Result, not as an error — whether that degrades the source is the pipeline's call. ActualModel
// stays empty by necessity: no agy event carries the model that ran (claude has modelUsage, codex its
// stderr banner), so the manifest records the request alone. Structured output comes from the result
// event only, never scraped out of response; its absence under a sent schema leaves the field empty,
// which the pipeline reads as a degraded source.
func (a *Agy) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	stream := newAgyStream(func(ev Event) { a.emit(sink, ev) }, len(req.Schema) > 0)
	spec := runSpec{
		argv: a.args(req),
		sink: sink,
		parse: func(ctx context.Context, r io.Reader) Result {
			_ = a.readLines(ctx, r, stream.line)
			return Result{}
		},
	}
	req.Prompt = agyStdinMessage(req.Prompt)

	res, err := a.run(ctx, req, spec)
	res.RequestedModel = req.Model
	res.StructuredOutput = stream.final.StructuredOutput
	res.Tokens = stream.final.tokens()
	if err != nil {
		return res, err
	}
	// status ERROR beside exit 0 is a mid-run tool failure the run recovered from — agy-toolerror.jsonl
	// answered completely under one — so the outcome weighs the exit code, never the status string
	// alone. On a non-zero exit the diagnostic is the result event's error string: agy's stderr was
	// empty in every capture, errors included, so there is no stderr line to mine.
	return a.classifyFailure(res, stream.final.Error, sink)
}

// args builds the invocation. --print carries an empty value deliberately: bare --print consumes the
// next argv token as the prompt, so the flag is emptied and the real prompt arrives on stdin as a
// stream-json user turn — the path that survives the Windows 8191-char argv cap, same as claude.
// --sandbox always: revmux never lets an agent write, and the prompt states the constraint too.
func (a *Agy) args(req Request) []string {
	argv := []string{
		"--print", "",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--sandbox",
		"--disable-slash-commands",
		"--print-timeout", a.printTimeout(),
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

// printTimeout overrides agy's internal answer wait, which defaults to 5m and would kill a long review
// while revmux's own supervision was content. Derived above the hard timeout so agy's timer can never
// be the one that fires first.
func (a *Agy) printTimeout() string {
	if a.opts.HardTimeout > 0 {
		return (a.opts.HardTimeout + time.Minute).String()
	}
	return agyDefaultPrintTimeout
}

// agyUserEvent is the one stdin message an --input-format stream-json run reads: the user turn
// carrying the prompt.
type agyUserEvent struct {
	Event   string `json:"event"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

// agyStdinMessage wraps the composed prompt as the user turn agy reads from stdin. Getting the shape
// wrong fails quietly in the worst case — an unrecognized event name produces no result event at all
// and the process just sits — so the message is built through the encoder, never by hand.
func agyStdinMessage(prompt string) string {
	msg := agyUserEvent{Event: "user"}
	msg.Message.Role = "user"
	msg.Message.Content = prompt
	b, _ := json.Marshal(msg) // a struct of plain strings cannot fail to marshal
	return string(b) + "\n"
}
