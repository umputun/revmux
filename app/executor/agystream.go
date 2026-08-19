package executor

import (
	"encoding/json"
	"strings"
)

// agyStateActive is the step state at dispatch; DONE and ERROR are completions.
const agyStateActive = "ACTIVE"

// agyToolArgKeys are the tool_info.parameters worth showing beside a tool's name, most specific first.
// This is agy's own parameter vocabulary — CapitalizedCamel where claude's keys are snake_case.
var agyToolArgKeys = []string{"CommandLine", "AbsolutePath", "Pattern", "Query", "SearchDirectory", "Url"}

// agyEvent is one line of agy's NDJSON output — its own dialect, not claude stream-json. Only the
// fields revmux acts on are decoded; the raw tee keeps the rest.
type agyEvent struct {
	Event      string     `json:"event"`
	StepUpdate *agyStep   `json:"step_update"`
	Result     *agyResult `json:"result"`
}

// agyStep is one step_update payload. An agent_response streams its text through ACTIVE text_delta
// chunks and may be DONE with no text at all — a thinking-only step before a tool call, carrying only
// usage and duration (agy-tools.jsonl, step 2).
type agyStep struct {
	StepIndex int          `json:"step_index"`
	State     string       `json:"state"`
	StepType  string       `json:"step_type"`
	TextDelta string       `json:"text_delta"`
	ToolName  string       `json:"tool_name"`
	ToolInfo  *agyToolInfo `json:"tool_info"`
}

type agyToolInfo struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters"`
}

// agyResult is the terminal event. status:"ERROR" does not imply a failed run — a mid-run tool error
// propagates here while the process exits 0 and response carries the complete answer
// (agy-toolerror.jsonl) — so the executor weighs the exit code and the answer, never this string alone.
type agyResult struct {
	Status           string          `json:"status"`
	Response         string          `json:"response"`
	Error            string          `json:"error"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            *agyUsage       `json:"usage"`
}

// agyUsage is the result's token accounting. No event anywhere carries the actual model (claude has
// modelUsage), so usage is all the terminal event reports about what the run spent.
type agyUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
}

// tokens is the run's whole footprint, cache included, read from the terminal result — the per-step
// usage entries are partials of the same total. output_tokens already includes thinking (measured 87
// output / 86 thinking on a one-word answer), so thinking_tokens is deliberately not added again.
func (r agyResult) tokens() int {
	if r.Usage == nil {
		return 0
	}
	return r.Usage.InputTokens + r.Usage.OutputTokens + r.Usage.CacheReadTokens
}

// agyStream decodes one run's NDJSON output down to events and the terminal result. It is per-run
// state — the delta accumulator — and therefore never a field on the executor, which serves the whole
// roster concurrently from one instance.
type agyStream struct {
	emit       func(Event)
	haveSchema bool
	steps      map[int]string // accumulated agent_response deltas, keyed by step index
	final      agyResult
	sawResult  bool
}

func newAgyStream(emit func(Event), haveSchema bool) *agyStream {
	return &agyStream{emit: emit, haveSchema: haveSchema, steps: map[int]string{}}
}

// line handles one stdout line. A line that fails to decode is skipped — a truncated stream degrades
// to a partial result rather than failing the run — and an unknown event kind is ignored, not an
// error. init is known and carries nothing worth an event.
func (s *agyStream) line(l string) {
	l = strings.TrimSpace(l)
	if l == "" {
		return
	}
	var ev agyEvent
	if err := json.Unmarshal([]byte(l), &ev); err != nil {
		return
	}
	switch {
	case ev.Event == "step_update" && ev.StepUpdate != nil:
		s.step(*ev.StepUpdate)
	case ev.Event == "result" && ev.Result != nil:
		s.final, s.sawResult = *ev.Result, true
	}
}

// step maps one step_update onto the emit vocabulary: agent prose is activity, a tool dispatch is
// progress, and the lifecycle steps — user_input, checkpoint, system_message, finish — report nothing.
func (s *agyStream) step(st agyStep) {
	switch st.StepType {
	case "agent_response":
		// deltas accumulate until the completion: an ACTIVE event carries a chunk of the answer being
		// written, and emitting each one would report the same sentence several times over
		if st.TextDelta != "" {
			s.steps[st.StepIndex] += st.TextDelta
		}
		if st.State == agyStateActive {
			return
		}
		text := flattenLines(s.steps[st.StepIndex])
		delete(s.steps, st.StepIndex)
		// a completion with no text is a thinking-only step, and a schema-forced answer is the raw
		// JSON duplicated by the result's structured_output — plumbing, not commentary, the same
		// reason claude's StructuredOutput tool call never reaches progress
		if text == "" || s.structuredAnswer(text) {
			return
		}
		s.emit(Event{Kind: EventActivity, Text: clampRunes(text)})
	case "tool":
		// dispatch only: the completion event repeats the parameters and would double every line
		if st.State != agyStateActive {
			return
		}
		if note := st.progress(); note != "" {
			s.emit(Event{Kind: EventProgress, Text: note})
		}
	}
}

// structuredAnswer reports whether text is the schema-forced answer rather than prose: under
// --json-schema the whole JSON object streams through agent_response deltas (agy-schema.jsonl) and
// arrives again on the result as structured_output, so reporting it puts a wall of raw JSON in the log.
func (s *agyStream) structuredAnswer(text string) bool {
	return s.haveSchema && strings.HasPrefix(text, "{") && json.Valid([]byte(text))
}

// progress is the short label for a tool dispatch. A run_command whose command line cannot be
// recovered is dropped rather than reported by name — the codex rule: a bare shell name says only that
// the agent called a tool. Every other tool keeps the name fallback, which names what it is doing.
func (st agyStep) progress() string {
	name := st.ToolName
	if name == "" && st.ToolInfo != nil {
		name = st.ToolInfo.Name
	}
	if name == "" {
		return ""
	}
	arg := st.arg()
	if arg == "" {
		if name == "run_command" {
			return ""
		}
		return name
	}
	return name + " " + clampRunes(arg)
}

// arg is the one parameter worth showing next to the tool's name, shortened for a status row the same
// way claude's tool inputs are.
func (st agyStep) arg() string {
	if st.ToolInfo == nil {
		return ""
	}
	for _, key := range agyToolArgKeys {
		val, ok := st.ToolInfo.Parameters[key].(string)
		if !ok || strings.TrimSpace(val) == "" {
			continue
		}
		val = flattenLines(val)
		if key == "AbsolutePath" {
			val = shortPath(val)
		}
		return val
	}
	return ""
}
