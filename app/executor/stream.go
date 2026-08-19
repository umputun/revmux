package executor

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// textLimit is a sanity bound on one event's text, not a display width. Clipping to the terminal is
// the renderers' job — they are the only ones who know how wide it is, and a decoder that cuts first
// destroys text no renderer can get back. It counts runes, not bytes, so a cut cannot land mid-rune
// and put invalid UTF-8 into the TUI, the stderr lines or events.jsonl.
const textLimit = 2000

// schemaToolName is the tool --json-schema forces the model through. It is plumbing, never work.
const schemaToolName = "StructuredOutput"

// streamEvent is one line of claude's stream-json output. Only the fields revmux acts on are decoded.
type streamEvent struct {
	Type             string                `json:"type"`
	Subtype          string                `json:"subtype"`
	StructuredOutput json.RawMessage       `json:"structured_output"`
	ModelUsage       map[string]modelUsage `json:"modelUsage"`
	Usage            *tokenUsage           `json:"usage"`
	TTFTMs           int                   `json:"ttft_ms"`
	IsError          bool                  `json:"is_error"`
	RateLimitInfo    *RateLimitInfo        `json:"rate_limit_info"`
	Denials          []permissionDenial    `json:"permission_denials"`
	Message          *streamMessage        `json:"message"`
}

type modelUsage struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
}

type tokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type streamMessage struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// toolArgKeys are the input fields worth showing beside a tool's name, most specific first. A tool
// call carries what it is acting on — the file being read, the pattern being searched — and a bare
// "Read" repeated down the log says only that the agent is alive, not what it is looking at.
var toolArgKeys = []string{"file_path", "pattern", "command", "path", "query", "description", "url"}

// arg is the one input value worth showing next to the tool's name, shortened for a status row: a
// path is cut to its last two elements, anything else to its first line.
func (b contentBlock) arg() string {
	if len(b.Input) == 0 {
		return ""
	}
	var in map[string]any
	if err := json.Unmarshal(b.Input, &in); err != nil {
		return ""
	}
	for _, key := range toolArgKeys {
		val, ok := in[key].(string)
		if !ok || strings.TrimSpace(val) == "" {
			continue
		}
		val = flattenLines(val)
		if key == "file_path" || key == "path" {
			val = shortPath(val)
		}
		return clampRunes(val)
	}
	return ""
}

// flattenLines folds a multi-line block into one line, because an event is one line by construction.
// Taking only the first line instead drops the body of anything that opens with a lead-in — a closing
// summary reduced to "Summary of what I checked and found:" and nothing else.
func flattenLines(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// clampRunes bounds text at textLimit without splitting a rune.
func clampRunes(s string) string {
	if utf8.RuneCountInString(s) <= textLimit {
		return s
	}
	return string([]rune(s)[:textLimit]) + "..."
}

// shortPath keeps the last two elements of a path. An absolute path eats a status row on its own, and
// the leading directories are the same for every file in one review anyway.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// permissionDenial is one tool call the permission layer refused. Only the fields worth naming in a
// log line are decoded; the raw tee keeps the rest.
type permissionDenial struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// denials names the tool calls the permission layer refused, or "" when it refused none. A denied agent
// does not fail — it says so in its own prose and carries on with less — so a review silently narrows to
// whatever the layer allowed and still returns a full-looking report.
func (e streamEvent) denials() string {
	if len(e.Denials) == 0 {
		return ""
	}
	seen, names := map[string]bool{}, []string{}
	for _, d := range e.Denials {
		name := d.ToolName
		if cmd := flattenLines(d.ToolInput.Command); cmd != "" {
			name += " " + clampRunes(cmd)
		}
		if !seen[name] {
			seen[name], names = true, append(names, name)
		}
	}
	// the whole line, not just each command: a denied Bash call carries a command and several of them
	// join into a line far past what any one of them is bounded to
	return clampRunes("denied " + strconv.Itoa(len(e.Denials)) + " tool call(s): " + strings.Join(names, ", "))
}

// tokens is the run's whole footprint, cache included. The CLI reports it, so nothing is estimated.
func (e streamEvent) tokens() int {
	if e.Usage == nil {
		return 0
	}
	return e.Usage.InputTokens + e.Usage.OutputTokens +
		e.Usage.CacheReadInputTokens + e.Usage.CacheCreationInputTokens
}

// actualModel reports what ran rather than what was asked for: --model can be silently ignored. The
// busiest entry wins, with the name breaking ties so the answer does not depend on map order.
func (e streamEvent) actualModel() string {
	name, out := "", -1
	for m, u := range e.ModelUsage {
		if u.OutputTokens > out || (u.OutputTokens == out && m < name) {
			name, out = m, u.OutputTokens
		}
	}
	return name
}

// activity is what the model actually said in one assistant turn: the first line of its first non-empty
// text block, or "" for a turn that only called tools or only thought. Tool names are deliberately not
// reported here — that is liveness, which progress covers, and the two must not share a field.
func (e streamEvent) activity() string {
	if e.Message == nil {
		return ""
	}
	for _, b := range e.Message.Content {
		if b.Type != "text" {
			continue
		}
		text := flattenLines(b.Text)
		if text == "" {
			continue
		}
		return clampRunes(text)
	}
	return ""
}

// progress is a short label for what the turn is doing right now: the tool it called. It feeds a status
// cell overwritten on every turn, never a scrollback line, and it is what keeps the stagger honest — an
// agent that spends its opening minute reading files produces no prose at all.
func (e streamEvent) progress() string {
	if e.Message == nil {
		return ""
	}
	for _, b := range e.Message.Content {
		if b.Type != "tool_use" {
			continue
		}
		// the schema tool is how claude is made to answer in JSON at all, not something the agent
		// chose to do, so naming it in a progress row reports plumbing as if it were work
		if b.Name == "" || b.Name == schemaToolName {
			continue
		}
		if arg := b.arg(); arg != "" {
			return b.Name + " " + arg
		}
		return b.Name
	}
	return ""
}
