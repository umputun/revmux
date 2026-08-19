package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agyCapture reads one of the recorded agy fixtures. Each is a live recording of agy 1.1.15; the
// derived cases below patch or cut these bytes in code rather than committing hand-written streams.
func agyCapture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // fixture names are test literals
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

// feedAgy runs the decoder over a capture line by line, the way readLines will hand it lines.
func feedAgy(data []byte, haveSchema bool) (*agyStream, []Event) {
	var events []Event
	s := newAgyStream(func(ev Event) { events = append(events, ev) }, haveSchema)
	for line := range strings.SplitSeq(string(data), "\n") {
		s.line(line)
	}
	return s, events
}

func eventTexts(events []Event, kind EventKind) []string {
	var texts []string
	for _, ev := range events {
		if ev.Kind == kind {
			texts = append(texts, ev.Text)
		}
	}
	return texts
}

func TestAgyStream_clean(t *testing.T) {
	s, events := feedAgy(agyCapture(t, "agy-clean.jsonl"), false)

	// the answer streams as an ACTIVE "pong" delta plus a DONE "\n" tail: one activity line, whole
	// text, emitted once at completion rather than once per chunk
	assert.Equal(t, []string{"pong"}, eventTexts(events, EventActivity))
	assert.Empty(t, eventTexts(events, EventProgress), "a tool-free run has no dispatches to report")

	require.True(t, s.sawResult)
	assert.Equal(t, "SUCCESS", s.final.Status)
	assert.Equal(t, "pong\n", s.final.Response)
	assert.Empty(t, s.final.StructuredOutput, "no schema was sent")
	assert.Equal(t, 8655+87+8135, s.final.tokens(),
		"input + output + cache reads; thinking is already inside output and must not be added again")
}

func TestAgyStream_stdinFedRunDecodesTheSame(t *testing.T) {
	// the stdin-fed capture is shape-identical to the argv-fed one, which is what lets the executor
	// pick the stdin path for the Windows argv cap without a second decoder
	s, events := feedAgy(agyCapture(t, "agy-stdin-clean.jsonl"), false)

	assert.Equal(t, []string{"pong"}, eventTexts(events, EventActivity),
		"a DONE carrying the whole text with no ACTIVE deltas before it is still one activity line")
	require.True(t, s.sawResult)
	assert.Equal(t, "SUCCESS", s.final.Status)
}

func TestAgyStream_tools(t *testing.T) {
	s, events := feedAgy(agyCapture(t, "agy-tools.jsonl"), false)

	// step 2 is agent_response DONE with no text at all — a thinking-only step before the tool call —
	// and must emit nothing, not a blank line
	assert.Equal(t, []string{"The command printed **2**."}, eventTexts(events, EventActivity))
	assert.Equal(t, []string{"run_command wc -l /tmp/agy-cap/ws/notes.txt"}, eventTexts(events, EventProgress),
		"dispatch only: the DONE completion repeats the parameters and must not double the line")

	require.True(t, s.sawResult)
	assert.Equal(t, "SUCCESS", s.final.Status)
}

func TestAgyStream_schema(t *testing.T) {
	s, events := feedAgy(agyCapture(t, "agy-schema.jsonl"), true)

	// under a schema the whole JSON answer streams through agent_response deltas and arrives again as
	// structured_output — reporting the delta text puts a wall of raw JSON in the log
	assert.Empty(t, eventTexts(events, EventActivity), "the schema-forced answer is plumbing, not commentary")

	require.True(t, s.sawResult)
	require.NotEmpty(t, s.final.StructuredOutput)
	var out struct {
		Findings []struct {
			Title  string   `json:"title"`
			Lenses []string `json:"lenses"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(s.final.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "demo finding", out.Findings[0].Title)
	assert.NotEmpty(t, out.Findings[0].Lenses)
}

func TestAgyStream_schemaFlagLeavesProseAlone(t *testing.T) {
	// the suppression is for the JSON answer specifically, never for prose a schema run also writes
	_, events := feedAgy(agyCapture(t, "agy-tools.jsonl"), true)
	assert.Equal(t, []string{"The command printed **2**."}, eventTexts(events, EventActivity))
}

func TestAgyStream_toolError(t *testing.T) {
	s, events := feedAgy(agyCapture(t, "agy-toolerror.jsonl"), false)

	// status ERROR beside a complete response: a mid-run tool failure propagated into the terminal
	// result while the run still answered — the decoder reports both and judges neither
	require.True(t, s.sawResult)
	assert.Equal(t, "ERROR", s.final.Status)
	assert.Contains(t, s.final.Error, "resource temporarily unavailable")
	assert.Contains(t, s.final.Response, "Summary of Work")

	activity := eventTexts(events, EventActivity)
	require.Len(t, activity, 1)
	assert.Contains(t, activity[0], "Summary of Work")
	assert.Contains(t, activity[0], "3 total lines including the final trailing newline.",
		"the ACTIVE and DONE deltas join into the whole answer; taking either alone loses half of it")

	progress := eventTexts(events, EventProgress)
	assert.Contains(t, progress, "find_by_name notes.txt", "Pattern is the parameter worth showing")
	assert.Contains(t, progress, "view_file ws/notes.txt", "an absolute path is shortened for a status row")
	assert.Contains(t, progress, "manage_task", "a tool with no recoverable parameter keeps its name")

	for _, ev := range events {
		assert.NotContains(t, ev.Text, "\n", "an event is one line by construction")
		assert.NotEmpty(t, ev.Text, "a thinking-only step must emit nothing rather than a blank line")
	}
	count := 0
	for _, p := range progress {
		if p == "run_command wc -l /tmp/agy-cap/ws/notes.txt" {
			count++
		}
	}
	assert.Equal(t, 1, count, "a tool call is one dispatch line, not a dispatch plus its completion")
}

func TestAgyStream_badModelError(t *testing.T) {
	s, events := feedAgy(agyCapture(t, "agy-error.jsonl"), false)

	require.True(t, s.sawResult, "the diagnostic is a single result event on stdout, no init before it")
	assert.Equal(t, "ERROR", s.final.Status)
	assert.Contains(t, s.final.Error, "no-such-model")
	assert.Empty(t, events, "a run that never started has nothing to report")
	assert.Zero(t, s.final.tokens())
}

func TestAgyStream_printTimeout(t *testing.T) {
	s, events := feedAgy(agyCapture(t, "agy-timeout.jsonl"), false)

	require.True(t, s.sawResult)
	assert.Equal(t, "ERROR", s.final.Status)
	assert.Equal(t, "timeout waiting for response", s.final.Error)
	assert.Contains(t, eventTexts(events, EventProgress), "run_command sleep 30",
		"the step stream up to the expiry is intact and still decodes")
}

func TestAgyStream_garbageDegrades(t *testing.T) {
	data := append([]byte("not json at all\n{}\n\n{\"event\":\"weird\"}\n"), agyCapture(t, "agy-clean.jsonl")...)
	s, events := feedAgy(data, false)

	assert.Equal(t, []string{"pong"}, eventTexts(events, EventActivity),
		"garbage and unknown event kinds are skipped, never errors")
	require.True(t, s.sawResult)
	assert.Equal(t, "SUCCESS", s.final.Status)
}

func TestAgyStream_truncatedStream(t *testing.T) {
	// the clean capture cut before its result line, which is what a killed CLI leaves behind
	data := agyCapture(t, "agy-clean.jsonl")
	cut := bytes.LastIndex(bytes.TrimRight(data, "\n"), []byte("\n"))
	require.Positive(t, cut)
	s, events := feedAgy(data[:cut], false)

	assert.False(t, s.sawResult, "no terminal event arrived")
	assert.Equal(t, []string{"pong"}, eventTexts(events, EventActivity),
		"everything before the cut is still reported")
}

func TestAgyStream_shellWithoutCommandDropped(t *testing.T) {
	// derived from the recording: the one ACTIVE run_command dispatch with its CommandLine removed.
	// The codex rule — a bare shell name says only that the agent called a tool — applies here too.
	out := &bytes.Buffer{}
	patched := false
	for line := range bytes.SplitSeq(agyCapture(t, "agy-tools.jsonl"), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev map[string]any
		require.NoError(t, json.Unmarshal(line, &ev))
		if su, ok := ev["step_update"].(map[string]any); ok && !patched &&
			su["step_type"] == "tool" && su["state"] == agyStateActive && su["tool_name"] == "run_command" {
			info, ok := su["tool_info"].(map[string]any)
			require.True(t, ok)
			delete(info, "parameters")
			patched = true
			line, _ = json.Marshal(ev)
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	require.True(t, patched, "no ACTIVE run_command dispatch in the recording")

	_, events := feedAgy(out.Bytes(), false)
	for _, p := range eventTexts(events, EventProgress) {
		assert.NotContains(t, p, "run_command", "a shell call whose command cannot be recovered is dropped")
	}
}
