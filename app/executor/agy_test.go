package executor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

func agyCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "agy-clean.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

func agyPatch(t *testing.T, match func(map[string]any) bool, patch func(map[string]any)) []byte {
	t.Helper()
	out := &bytes.Buffer{}
	patched := false
	for line := range bytes.SplitSeq(agyCapture(t), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev map[string]any
		require.NoError(t, json.Unmarshal(line, &ev))
		if !patched && match(ev) {
			patch(ev)
			patched = true
			line, _ = json.Marshal(ev)
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	require.True(t, patched, "no matching event in the recording")
	return out.Bytes()
}

func agyResultEvent(ev map[string]any) bool { return ev["event"] == "result" }

func agyToolDone(ev map[string]any) bool {
	step, ok := ev["step_update"].(map[string]any)
	return ok && step["step_type"] == "tool" && step["state"] == "DONE"
}

func collectSink() (*mocks.EventSinkMock, func() []executor.Event) {
	var mu sync.Mutex
	var got []executor.Event
	sink := &mocks.EventSinkMock{EmitFunc: func(ev executor.Event) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	}}
	return sink, func() []executor.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]executor.Event(nil), got...)
	}
}

func eventsOf(evs []executor.Event, kind executor.EventKind) []string {
	var out []string
	for _, ev := range evs {
		if ev.Kind == kind {
			out = append(out, ev.Text)
		}
	}
	return out
}

func TestAgy_args(t *testing.T) {
	path := writeFixture(t, agyCapture(t))
	runner := fakeRunner("emit", path)
	a := executor.NewAgy(runner, executor.Opts{})

	req := executor.Request{Prompt: "review this", Model: "gemini-4-neon", Effort: "high", Schema: json.RawMessage(`{"type":"object"}`)}
	_, err := a.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "agy", call.Name)
	want := []string{
		"--print=",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--disable-slash-commands",
		"--settings", `{"permissions":{"allow":["read_file(*)"],"deny":["write_file(*)"]}}`,
		"--model", "gemini-4-neon",
		"--effort", "high",
		"--json-schema", `{"type":"object"}`,
	}
	assert.Equal(t, want, call.Args)
	assert.NotContains(t, call.Args, "review this", "the prompt goes in on stdin, never on argv")
}

func TestAgy_args_optionalFlagsOmitted(t *testing.T) {
	path := writeFixture(t, agyCapture(t))
	runner := fakeRunner("emit", path)
	_, err := executor.NewAgy(runner, executor.Opts{}).Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)

	args := runner.CommandCalls()[0].Args
	for _, flag := range []string{"--model", "--effort", "--json-schema"} {
		assert.NotContains(t, args, flag)
	}
}

func TestAgy_Run_promptOnStdin(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	a := executor.NewAgy(fakeRunner("echo", ""), executor.Opts{})
	res, err := a.Run(context.Background(), executor.Request{Prompt: "review \"this\"\nnow", Schema: schema}, discardSink())
	require.NoError(t, err)

	var in struct {
		Event   string `json:"event"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace([]byte(res.Raw)), &in))
	assert.Equal(t, "user", in.Event)
	assert.Equal(t, "user", in.Message.Role)
	assert.Equal(t, "review \"this\"\nnow"+executor.NarrationContract(schema), in.Message.Content)
	assert.Equal(t, 1, bytes.Count([]byte(res.Raw), []byte("\n")), "exactly one message, so agy runs one turn")
}

func TestAgy_Run_clean(t *testing.T) {
	path := writeFixture(t, agyCapture(t))
	sink, events := collectSink()
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	req := executor.Request{Prompt: "x", Model: "gemini-3.8-flash-medium", Schema: json.RawMessage(`{"type":"object"}`)}
	res, err := a.Run(context.Background(), req, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	assert.JSONEq(t, `{"findings":[]}`, string(res.StructuredOutput))
	assert.Equal(t, "gemini-3.8-flash-medium", res.RequestedModel)
	assert.Equal(t, "gemini-3.8-flash-medium", res.ActualModel)
	assert.Equal(t, 39166+758+170581, res.Tokens, "input, output and cache reads, as claude counts them")

	activity := eventsOf(events(), executor.EventActivity)
	require.NotEmpty(t, activity)
	assert.Equal(t, "Sizing stream.go and checking line count.", activity[0], "deltas of one step join into one line")
	assert.Equal(t, "No bugs found after verifying parsing and helper logic.", activity[len(activity)-1],
		"the answer streamed after the closing narration is cut off")
	for _, line := range activity {
		assert.NotContains(t, line, `"findings"`)
	}

	progress := eventsOf(events(), executor.EventProgress)
	assert.Contains(t, progress, "run_command wc -l stream.go")
	assert.Contains(t, progress, "read_file executor/stream.go", "a path is shortened like claude's")
	assert.Empty(t, eventsOf(events(), executor.EventInfo))
}

func TestAgy_Run_reportsIdleTimeout(t *testing.T) {
	sink, events := collectSink()
	a := executor.NewAgy(fakeRunner("emit", writeFixture(t, agyCapture(t))), executor.Opts{IdleTimeout: 5 * time.Minute})
	_, err := a.Run(context.Background(), executor.Request{Prompt: "x", Schema: json.RawMessage(`{"type":"object"}`)}, sink)
	require.NoError(t, err)

	evs := events()
	require.NotEmpty(t, evs)
	assert.Equal(t, executor.Event{Kind: executor.EventInfo, Text: "idle timeout: 5m0s"}, evs[0],
		"the watchdog agy runs under is on record before anything it streams")
	assert.Equal(t, []string{"idle timeout: 5m0s"}, eventsOf(evs, executor.EventInfo))
}

func TestAgy_Run_modelMismatch(t *testing.T) {
	data := agyPatch(t, func(ev map[string]any) bool { return ev["event"] == "init" }, func(ev map[string]any) {
		ev["init"].(map[string]any)["model"] = "gemini-3.8-flash-low"
	})
	res, err := executor.NewAgy(fakeRunner("emit", writeFixture(t, data)), executor.Opts{}).Run(context.Background(),
		executor.Request{Prompt: "x", Model: "gemini-3.8-flash-medium"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.8-flash-medium", res.RequestedModel)
	assert.Equal(t, "gemini-3.8-flash-low", res.ActualModel)
}

func TestAgy_Run_answerKeptWithoutSchema(t *testing.T) {
	path := writeFixture(t, agyCapture(t))
	sink, events := collectSink()
	_, err := executor.NewAgy(fakeRunner("emit", path), executor.Opts{}).Run(context.Background(),
		executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	activity := eventsOf(events(), executor.EventActivity)
	require.NotEmpty(t, activity)
	assert.Equal(t, `No bugs found after verifying parsing and helper logic.{"findings":[]}`, activity[len(activity)-1],
		"without a schema the text is the answer and nothing is cut")
}

func TestAgy_Run_nullStructuredOutput(t *testing.T) {
	// regression: "null" decoded to four bytes and passed every len()==0 no-answer check downstream
	data := agyPatch(t, agyResultEvent, func(ev map[string]any) {
		ev["result"].(map[string]any)["structured_output"] = nil
	})
	res, err := executor.NewAgy(fakeRunner("emit", writeFixture(t, data)), executor.Opts{}).Run(context.Background(),
		executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.Empty(t, res.StructuredOutput)
}

func TestAgy_Run_errorStatus(t *testing.T) {
	data := agyPatch(t, agyResultEvent, func(ev map[string]any) {
		r := ev["result"].(map[string]any)
		r["status"], r["error"] = "ERROR", "model request failed:\n429 RESOURCE_EXHAUSTED"
		delete(r, "structured_output")
	})
	sink, events := collectSink()
	res, err := executor.NewAgy(fakeRunner("emit", writeFixture(t, data)), executor.Opts{}).Run(context.Background(),
		executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	assert.Empty(t, res.StructuredOutput)
	assert.Equal(t, []string{"agy error: model request failed: 429 RESOURCE_EXHAUSTED"}, eventsOf(events(), executor.EventInfo))
}

func TestAgy_Run_toolError(t *testing.T) {
	tests := []struct {
		name  string
		error map[string]any
		want  string
	}{
		{name: "no reason reported", want: "tool failed: run_command wc -l stream.go"},
		{name: "reason reported", error: map[string]any{"type": "TOOL_ERROR", "message": "file does not exist"},
			want: "tool failed: run_command wc -l stream.go: file does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := agyPatch(t, agyToolDone, func(ev map[string]any) {
				step := ev["step_update"].(map[string]any)
				step["state"] = "ERROR"
				if tt.error != nil {
					step["tool_info"].(map[string]any)["error"] = tt.error
				}
			})
			sink, events := collectSink()
			_, err := executor.NewAgy(fakeRunner("emit", writeFixture(t, data)), executor.Opts{}).Run(context.Background(),
				executor.Request{Prompt: "x"}, sink)
			require.NoError(t, err)
			assert.Equal(t, []string{tt.want}, eventsOf(events(), executor.EventInfo))
		})
	}
}

func TestAgy_Run_truncatedStream(t *testing.T) {
	data := agyCapture(t)
	cut := len(data) - 200
	require.NotEqual(t, byte('\n'), data[cut-1], "the cut must land inside a line")

	res, err := executor.NewAgy(fakeRunner("emit", writeFixture(t, data[:cut])), executor.Opts{}).Run(context.Background(),
		executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a malformed stream degrades rather than failing the run")
	assert.Empty(t, res.StructuredOutput, "the result event never arrived intact")
	assert.NotEmpty(t, res.Raw)
}

func TestAgy_Run_idleTimeout(t *testing.T) {
	out := &bytes.Buffer{}
	for line := range bytes.SplitAfterSeq(agyCapture(t), []byte("\n")) {
		out.Write(line)
		if bytes.Contains(line, []byte(`"state":"DONE","step_type":"agent_response"`)) {
			break
		}
	}
	require.NotContains(t, out.String(), `"event":"result"`)

	fired := make(chan func(), 1)
	clk := &mocks.ClockMock{
		NowFunc: func() time.Time { return time.Unix(0, 0).UTC() },
		AfterFuncFunc: func(_ time.Duration, f func()) executor.Timer {
			fired <- f
			return &mocks.TimerMock{StopFunc: func() bool { return true }, ResetFunc: func(time.Duration) bool { return true }}
		},
	}
	seen := make(chan struct{})
	var once sync.Once
	sink := &mocks.EventSinkMock{EmitFunc: func(ev executor.Event) {
		if ev.Kind == executor.EventActivity {
			once.Do(func() { close(seen) })
		}
	}}
	go func() {
		expire := <-fired
		<-seen
		expire()
	}()

	a := executor.NewAgy(fakeRunner("stall", writeFixture(t, out.Bytes())), executor.Opts{IdleTimeout: time.Minute, Clock: clk})
	res, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err, "an idle timeout is a retryable outcome, not a failure")
	assert.True(t, res.IdleTimedOut)
	assert.Empty(t, res.StructuredOutput)
}

func TestAgy_Run_answerCutAfterBracesInProse(t *testing.T) {
	const lead = "No bugs found after verifying parsing and helper logic."
	tests := []struct {
		name, delta, want string
	}{
		{"brace in prose, then the answer", `see m{k} first.{"findings":[]}`, lead + "see m{k} first."},
		{"brace in prose, no answer", "see m{k} first.", lead + "see m{k} first."},
		{"answer alone", `{"findings":[]}`, lead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := agyPatch(t, func(ev map[string]any) bool {
				step, ok := ev["step_update"].(map[string]any)
				return ok && step["step_index"] == float64(21) && step["state"] == "DONE"
			}, func(ev map[string]any) { ev["step_update"].(map[string]any)["text_delta"] = tt.delta })
			sink, events := collectSink()
			_, err := executor.NewAgy(fakeRunner("emit", writeFixture(t, data)), executor.Opts{}).Run(context.Background(),
				executor.Request{Prompt: "x", Schema: json.RawMessage(`{"type":"object"}`)}, sink)
			require.NoError(t, err)
			activity := eventsOf(events(), executor.EventActivity)
			require.NotEmpty(t, activity)
			assert.Equal(t, tt.want, activity[len(activity)-1])
		})
	}
}
