package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// readChunk is how much stdout one read takes. Codex answers in one long block, so the parser reads raw
// chunks rather than lines: the first chunk is what tells the stagger this process is alive.
const readChunk = 32 << 10

// codexHeaderKeys are the resolved-configuration lines worth forwarding out of codex's noisy stderr.
var codexHeaderKeys = []string{"model", "sandbox", "reasoning effort"}

// codexTokensMarker is the footer codex prints when a run ends. The count lands on the line after it,
// so the marker only arms the read.
const codexTokensMarker = "tokens used"

// Codex runs the codex CLI. Its stdout is prose rather than stream-json, so the output contract rides on
// the prompt and the answer is extracted from whatever the process printed.
type Codex struct {
	proc
}

// NewCodex builds a codex executor. A nil Opts.Clock is filled with the production clock, because the
// composition root assembles Opts from flags that carry no clock at all.
func NewCodex(runner CommandRunner, opts Opts) *Codex {
	return &Codex{proc: newProc("codex", runner, opts)}
}

// Run executes one request. A non-zero exit or an idle timeout comes back on the Result rather than as
// an error, and output holding no JSON degrades the source instead of failing the run.
func (c *Codex) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	req.Prompt += CodexOutputContract(req.Schema)
	session := make(chan string, 1)
	errs := newCodexStderr(func(ev Event) { c.emit(sink, ev) }, session)

	// the rollout is codex's liveness, so it has to reach the watchdog: both pipes stay silent through a
	// long review while the rollout fills. It arrives through an atomic because the tail starts before
	// proc.run exists to hand it over. A nil load means no idle timeout is armed.
	var touch atomic.Pointer[func()]
	live := func() {
		if f := touch.Load(); f != nil {
			(*f)()
		}
	}

	spec := runSpec{
		argv:       c.args(req),
		sink:       sink,
		parse:      func(ctx context.Context, r io.Reader) Result { return c.drain(ctx, r, sink) },
		stderrLine: errs.line,
		shareTouch: func(f func()) { touch.Store(&f) },
	}

	// follow the rollout for as long as the process runs, canceled only after run returns: the last
	// reasoning step and the answer both land after the last byte anyone was waiting on
	tailCtx, stopTail := context.WithCancel(context.WithoutCancel(ctx))
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		select {
		case id := <-session:
			c.tailRollout(tailCtx, id, sink, live)
		case <-tailCtx.Done():
		}
	}()

	res, err := c.run(ctx, req, spec)
	// withdrawn before the tail is stopped, and that order is the fix: run stops the idle timer on its
	// way out while the tail is still looping, so its next pass could re-arm a finished run's timer.
	// Suppressing the touch on the tail's own final pass leaves the top-of-loop window open.
	touch.Store(nil)
	stopTail()
	<-tailDone
	res.RequestedModel = req.Model
	res.ActualModel = errs.model
	res.Tokens = errs.total()
	if err != nil {
		return res, err
	}
	if out, exErr := c.extract(res.Raw); exErr == nil {
		res.StructuredOutput = out
	}
	return c.classifyFailure(res, errs.diag, sink)
}

func (c *Codex) args(req Request) []string {
	argv := []string{"exec", "--sandbox", "read-only"}
	if req.Model != "" {
		argv = append(argv, "-m", req.Model)
	}
	if req.Effort != "" {
		argv = append(argv, "-c", "model_reasoning_effort="+req.Effort)
	}
	return argv
}

// CodexOutputContract is codex's substitute for claude's --json-schema, appended to every prompt Run
// dispatches. The schema comes off the request, so a codex entry running synthesis or verify asks for
// that stage's shape. Exported because Run appends it after the caller archived the composed prompt, and
// an archived prompt missing it describes a run that did not happen.
func CodexOutputContract(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	return "\n\nReturn ONLY a JSON object matching the schema below. No prose before or after it.\n\nSchema:\n" +
		string(schema) + "\n"
}

// drain consumes stdout, emitting one activity event on the first bytes to arrive. Codex has no
// stream-json, so that raw write is the only signal a codex leader has to release the rest of the
// roster with, and without it stagger-delay becomes the only release path.
func (c *Codex) drain(ctx context.Context, r io.Reader, sink EventSink) Result {
	buf := make([]byte, readChunk)
	var once sync.Once
	for ctx.Err() == nil {
		n, err := r.Read(buf)
		if n > 0 {
			// progress, not activity: it fires when codex finally answers, long after the rollout has
			// narrated what it is doing. It still opens the stagger gate, which is all it is for.
			once.Do(func() { c.emit(sink, Event{Kind: EventProgress, Text: "answering"}) })
		}
		if err != nil {
			break
		}
	}
	return Result{}
}

// extract pulls the answer out of output that may carry prose around it. Decoding starts at each brace
// in turn, and an incomplete tail ends the search rather than continuing into it: a nested object inside
// a truncated answer would otherwise come back looking like the whole answer.
func (c *Codex) extract(raw string) (json.RawMessage, error) {
	for i, ch := range raw {
		if ch != '{' {
			continue
		}
		var out json.RawMessage
		err := json.NewDecoder(strings.NewReader(raw[i:])).Decode(&out)
		if err == nil {
			return out, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil, errors.New("no JSON object in codex output")
}

// codexStderr filters one run's stderr down to what is worth keeping. It is per-run state and therefore
// never a field on Codex, which serves the whole roster concurrently from one instance.
type codexStderr struct {
	emit       func(Event)
	seen       map[string]bool
	model      string
	diag       string
	tokens     int
	wantTokens bool
	atEnd      bool          // the accepted count is still the last thing stderr printed
	session    chan<- string // receives the session id once the banner surfaces it, buffered, sent once
	sentID     bool
}

func newCodexStderr(emit func(Event), session chan<- string) *codexStderr {
	return &codexStderr{emit: emit, seen: make(map[string]bool), session: session}
}

// line handles one stderr line: it forwards the resolved model, sandbox and effort once each, counts the
// tokens the run reported, and records the last CLI diagnostic — a plan-quota failure arrives here with
// an empty stdout. The last diagnostic wins rather than the first, since codex echoes the whole prompt to
// stderr ahead of anything it reports itself. The headers go out as EventInfo, never EventActivity: the
// banner prints before codex has contacted a model, and activity is what releases the stagger gate.
func (s *codexStderr) line(l string) {
	s.count(l)
	if s.diagnostic(l) {
		s.diag = strings.TrimSpace(l)
	}
	// the id that names the rollout file, which is where codex's actual activity goes. Sent once,
	// non-blocking, so a missing reader cannot stall the stderr drain and wedge the process.
	if !s.sentID && s.session != nil {
		if id := s.sessionID(l); id != "" {
			s.sentID = true
			select {
			case s.session <- id:
			default:
			}
		}
	}

	key, val, ok := strings.Cut(l, ":")
	key, val = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)
	if !ok || val == "" || s.seen[key] || !slices.Contains(codexHeaderKeys, key) {
		return
	}
	s.seen[key] = true
	if key == "model" {
		s.model = val
	}
	s.emit(Event{Kind: EventInfo, Text: key + ": " + val})
}

// count reads the token footer, codex's only report of what a run spent. The marker and the count are
// separate lines, so the marker arms the next one and a non-number disarms it — codex echoes the whole
// prompt to stderr. Disarming alone is not enough: an echoed prompt can carry both on consecutive lines,
// which is why total additionally checks that nothing followed the count.
func (s *codexStderr) count(l string) {
	t := strings.TrimSpace(l)
	if t == "" {
		return
	}
	if strings.EqualFold(t, codexTokensMarker) {
		s.wantTokens, s.atEnd = true, false
		return
	}
	armed := s.wantTokens
	s.wantTokens, s.atEnd = false, false
	if !armed {
		return
	}
	if n, err := strconv.Atoi(strings.ReplaceAll(t, ",", "")); err == nil && n >= 0 {
		s.tokens, s.atEnd = n, true
	}
}

// total is the run's token count, read once stderr has drained. A count something else followed came from
// the echoed prompt rather than the footer, so it is dropped rather than reported.
func (s *codexStderr) total() int {
	if !s.atEnd {
		return 0
	}
	return s.tokens
}

// diagnostic reports whether a line is a codex CLI error rather than progress chatter. Gating on the
// prefix is what keeps a reasoning stream discussing a rate limit from being read as one.
func (s *codexStderr) diagnostic(l string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "error:")
}
