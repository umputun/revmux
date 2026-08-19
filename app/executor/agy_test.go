package executor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

// agyFixture reads one of the live agy recordings. Derived cases below patch or cut these bytes in
// code, so a re-record regenerates the whole family.
func agyFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // fixture names are test literals
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

// agyStallFixture is the clean capture cut before its terminal result line: the activity still arrives,
// and then nothing does.
func agyStallFixture(t *testing.T) []byte {
	t.Helper()
	data := bytes.TrimRight(agyFixture(t, "agy-clean.jsonl"), "\n")
	cut := bytes.LastIndexByte(data, '\n')
	require.Positive(t, cut)
	return data[:cut+1]
}

// patchAgyResultError rewrites the error string of the bad-model capture, which is a single result
// event — the shape every agy diagnostic arrives in, stderr staying empty.
func patchAgyResultError(t *testing.T, errText string) []byte {
	t.Helper()
	var ev map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(agyFixture(t, "agy-error.jsonl")), &ev))
	res, ok := ev["result"].(map[string]any)
	require.True(t, ok)
	res["error"] = errText
	line, err := json.Marshal(ev)
	require.NoError(t, err)
	return append(line, '\n')
}

func TestAgy_args(t *testing.T) {
	path := writeFixture(t, agyFixture(t, "agy-clean.jsonl"))
	runner := fakeRunner("emit", path)
	a := executor.NewAgy(runner, executor.Opts{HardTimeout: 20 * time.Minute})

	req := executor.Request{Prompt: "review this", Model: "gemini-3.1-pro-low", Effort: "high",
		Schema: json.RawMessage(`{"type":"object"}`)}
	_, err := a.Run(context.Background(), req, discardSink())
	require.NoError(t, err)

	require.Len(t, runner.CommandCalls(), 1)
	call := runner.CommandCalls()[0]
	assert.Equal(t, "agy", call.Name)

	want := []string{
		"--print", "", // bare --print consumes the next argv token; the prompt goes in on stdin
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--sandbox",
		"--disable-slash-commands",
		"--print-timeout", "21m0s", // above the hard timeout, so agy's own 5m wait can never fire first
		"--model", "gemini-3.1-pro-low",
		"--effort", "high",
		"--json-schema", `{"type":"object"}`,
	}
	assert.Equal(t, want, call.Args)
}

func TestAgy_args_optionalFlagsOmitted(t *testing.T) {
	path := writeFixture(t, agyFixture(t, "agy-clean.jsonl"))
	runner := fakeRunner("emit", path)
	a := executor.NewAgy(runner, executor.Opts{})

	_, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)

	args := runner.CommandCalls()[0].Args
	assert.NotContains(t, args, "--model")
	assert.NotContains(t, args, "--effort")
	assert.NotContains(t, args, "--json-schema")
	assert.Contains(t, args, "--sandbox")
	assert.Contains(t, args, "--disable-slash-commands")
	idx := slices.Index(args, "--print-timeout")
	require.GreaterOrEqual(t, idx, 0, "agy's own 5m default would kill a long review")
	assert.Equal(t, "24h", args[idx+1], "with the hard timeout disabled the override is simply generous")
}

func TestAgy_Run_promptOnStdin(t *testing.T) {
	// the echo helper copies stdin to stdout, so res.Raw is exactly what the child was fed
	a := executor.NewAgy(fakeRunner("echo", "-"), executor.Opts{})
	res, err := a.Run(context.Background(), executor.Request{Prompt: "review this\nline two"}, discardSink())
	require.NoError(t, err)

	var msg struct {
		Event   string `json:"event"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Raw), &msg), "the prompt reaches agy as one stream-json line")
	assert.Equal(t, "user", msg.Event, "an unrecognized event name hangs the process with no result at all")
	assert.Equal(t, "user", msg.Message.Role)
	assert.Equal(t, "review this\nline two", msg.Message.Content)
	assert.True(t, strings.HasSuffix(res.Raw, "\n"))
}

func TestAgy_Run_clean(t *testing.T) {
	path := writeFixture(t, agyFixture(t, "agy-clean.jsonl"))
	sink := discardSink()
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	res, err := a.Run(context.Background(), executor.Request{Prompt: "x", Model: "gemini-3.1-pro-low"}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	assert.False(t, res.RateLimited)
	assert.Equal(t, "gemini-3.1-pro-low", res.RequestedModel)
	assert.Empty(t, res.ActualModel, "no agy event carries the model that ran; the request is all there is")
	assert.Equal(t, 8655+87+8135, res.Tokens, "input + output + cache reads off the result usage")
	assert.Empty(t, res.StructuredOutput, "no schema was sent")

	var activity []string
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == executor.EventActivity {
			activity = append(activity, call.Event.Text)
		}
	}
	assert.Equal(t, []string{"pong"}, activity)
}

func TestAgy_Run_schema(t *testing.T) {
	path := writeFixture(t, agyFixture(t, "agy-schema.jsonl"))
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	schema, err := os.ReadFile(filepath.Join("testdata", "finder-schema.json"))
	require.NoError(t, err)
	res, err := a.Run(context.Background(), executor.Request{Prompt: "x", Schema: schema}, discardSink())
	require.NoError(t, err)

	require.NotEmpty(t, res.StructuredOutput, "read off the result event, never scraped out of response")
	var out struct {
		Findings []struct {
			Title string `json:"title"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
	require.Len(t, out.Findings, 1)
	assert.Equal(t, "demo finding", out.Findings[0].Title)
}

func TestAgy_Run_schemaWithoutStructuredOutput(t *testing.T) {
	// a schema was sent but the result carries no structured_output: the field stays empty and the
	// pipeline degrades the source — no error, no scraping of the response string
	path := writeFixture(t, agyFixture(t, "agy-clean.jsonl"))
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	res, err := a.Run(context.Background(), executor.Request{Prompt: "x", Schema: json.RawMessage(`{"type":"object"}`)}, discardSink())
	require.NoError(t, err)
	assert.Empty(t, res.StructuredOutput)
}

func TestAgy_Run_toolErrorStatusStillSucceeds(t *testing.T) {
	// result.status ERROR beside exit 0 and a complete answer: a mid-run tool failure propagated into
	// the terminal result while the run recovered — the status string alone must not fail the source
	path := writeFixture(t, agyFixture(t, "agy-toolerror.jsonl"))
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	res, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.RateLimited)
}

func TestAgy_Run_badModelError(t *testing.T) {
	path := writeFixture(t, agyFixture(t, "agy-error.jsonl"))
	a := executor.NewAgy(fakeRunner("fail", path), executor.Opts{})

	res, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.Error(t, err, "a non-zero exit with a result error string is a hard diagnostic")
	assert.Contains(t, err.Error(), "agy failed:")
	assert.Contains(t, err.Error(), "no-such-model", "the diagnostic is the result event's error string")
	assert.Equal(t, 3, res.ExitCode)
}

func TestAgy_Run_patternTiers(t *testing.T) {
	tests := []struct {
		name        string
		errText     string
		wantErr     string
		wantLimited bool
	}{
		{name: "transient server hiccup is a retryable failure",
			errText: "API Error: 503 upstream unavailable", wantErr: "agy transient failure: api error: 503"},
		{name: "quota exhaustion is a limit, not an error",
			errText: "rate limit exceeded for model", wantLimited: true},
		{name: "anything else is a hard diagnostic",
			errText: "timeout waiting for response", wantErr: "agy failed: timeout waiting for response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, patchAgyResultError(t, tt.errText))
			sink := discardSink()
			a := executor.NewAgy(fakeRunner("fail", path), executor.Opts{})

			res, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
			assert.Equal(t, tt.wantLimited, res.RateLimited)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantLimited {
				assert.Equal(t, "limited", res.RateLimit.Status)
				kinds := make([]executor.EventKind, 0, len(sink.EmitCalls()))
				for _, call := range sink.EmitCalls() {
					kinds = append(kinds, call.Event.Kind)
				}
				assert.Contains(t, kinds, executor.EventRateLimit)
			}
		})
	}
}

func TestAgy_Run_cleanExitIsNeverAPatternMatch(t *testing.T) {
	// the toolerror capture's result carries an error string, and the run still exited 0: patterns are
	// consulted only when the process failed
	path := writeFixture(t, agyFixture(t, "agy-toolerror.jsonl"))
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	res, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.False(t, res.RateLimited)
}

func TestAgy_Run_truncatedStream(t *testing.T) {
	data := agyFixture(t, "agy-clean.jsonl")
	cut := len(data) - 40
	require.Positive(t, cut)
	require.NotEqual(t, byte('\n'), data[cut-1], "the cut must land inside a line")
	path := writeFixture(t, data[:cut])
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	res, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a malformed stream degrades rather than failing the run")
	assert.Empty(t, res.StructuredOutput, "the result event never arrived intact")
	assert.NotEmpty(t, res.Raw, "the partial stream is still available for the archive")
}

func TestAgy_Run_teesRawOutput(t *testing.T) {
	data := agyFixture(t, "agy-clean.jsonl")
	path := writeFixture(t, data)
	raw := &bytes.Buffer{}
	a := executor.NewAgy(fakeRunner("emit", path), executor.Opts{})

	res, err := a.Run(context.Background(), executor.Request{Prompt: "x", RawOutput: raw}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, data, raw.Bytes(), "archived bytes are what the process produced")
	assert.Equal(t, string(data), res.Raw)
}

func TestAgy_Run_idleTimeout(t *testing.T) {
	// the fixture ends in a block, per the testing rule: a recording that simply ends is EOF, not a
	// stall, so the stall helper emits the cut stream and then hangs until the watchdog kills it
	path := writeFixture(t, agyStallFixture(t))

	fired := make(chan func(), 1)
	clk := &mocks.ClockMock{
		NowFunc: func() time.Time { return time.Unix(0, 0).UTC() },
		AfterFuncFunc: func(_ time.Duration, f func()) executor.Timer {
			fired <- f
			return &mocks.TimerMock{
				StopFunc:  func() bool { return true },
				ResetFunc: func(time.Duration) bool { return true },
			}
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

	a := executor.NewAgy(fakeRunner("stall", path), executor.Opts{IdleTimeout: time.Minute, Clock: clk})
	res, err := a.Run(context.Background(), executor.Request{Prompt: "x"}, sink)

	require.NoError(t, err, "an idle timeout is a retryable outcome, not a failure")
	assert.True(t, res.IdleTimedOut)
	assert.Empty(t, res.StructuredOutput)
	assert.NotEmpty(t, res.Raw, "whatever the agent managed to emit is kept")
	assert.NotEmpty(t, clk.AfterFuncCalls())
}
