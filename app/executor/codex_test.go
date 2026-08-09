package executor_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/finding"
)

// the prose a codex run puts around its answer when it does not honor the contract to the letter.
const (
	codexPrologue = "I read the file and applied both lenses.\n\n"
	codexEpilogue = "\n\nThat is the complete list.\n"
)

// codexCapture is the one live codex recording. Every other codex fixture below is derived from its
// bytes, so re-recording it regenerates the whole family.
func codexCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex-clean.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return bytes.TrimSpace(data)
}

// codexStderrCapture is the stderr of that same run: the resolved-configuration banner, the echoed
// prompt and the tool chatter, none of which stdout carries.
func codexStderrCapture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex-clean.err.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	return data
}

// codexRepeatedBannerCapture is the same stderr with its banner printed twice, which is what the
// once-per-process filter exists to absorb. The single capture cannot exercise it: codex printed each
// header line once, so a filter that forwarded every one of them would look correct against it.
func codexRepeatedBannerCapture(t *testing.T) []byte {
	t.Helper()
	data := codexStderrCapture(t)
	return append(slices.Clone(data), data...)
}

// codexStderrLines is how many lines that stderr scans to, which is how many watchdog touches it owes:
// liveness is every line the child printed, not the handful the filter keeps.
func codexStderrLines(t *testing.T) int {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader(codexStderrCapture(t)))
	n := 0
	for sc.Scan() {
		n++
	}
	require.NoError(t, sc.Err())
	return n
}

func codexProseCapture(t *testing.T) []byte {
	t.Helper()
	return []byte(codexPrologue + string(codexCapture(t)) + codexEpilogue)
}

// codexNoJSONCapture is the prose-wrapped run with the answer block stripped out.
func codexNoJSONCapture(t *testing.T) []byte {
	t.Helper()
	out := bytes.ReplaceAll(codexProseCapture(t), codexCapture(t), nil)
	require.NotContains(t, string(out), "{")
	return out
}

// codexStalledCapture cuts the recording inside the answer, which is what a killed process leaves.
func codexStalledCapture(t *testing.T) []byte {
	t.Helper()
	data := codexCapture(t)
	cut := len(data) / 2
	require.Positive(t, cut)
	return data[:cut]
}

func eventTexts(sink *mocks.EventSinkMock, kind executor.EventKind) []string {
	out := []string{}
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == kind {
			out = append(out, call.Event.Text)
		}
	}
	return out
}

func eventKinds(sink *mocks.EventSinkMock) []executor.EventKind {
	out := make([]executor.EventKind, 0, len(sink.EmitCalls()))
	for _, call := range sink.EmitCalls() {
		out = append(out, call.Event.Kind)
	}
	return out
}

func TestCodex_args(t *testing.T) {
	path := writeFixture(t, codexCapture(t))

	t.Run("model and effort from the request", func(t *testing.T) {
		runner := fakeRunner("emit", path)
		c := executor.NewCodex(runner, executor.Opts{})
		req := executor.Request{Prompt: "review this", Model: "gpt-5.6-sol", Effort: "xhigh"}
		_, err := c.Run(context.Background(), req, discardSink())
		require.NoError(t, err)

		require.Len(t, runner.CommandCalls(), 1)
		call := runner.CommandCalls()[0]
		assert.Equal(t, "codex", call.Name)
		want := []string{"exec", "--sandbox", "read-only", "-m", "gpt-5.6-sol", "-c", "model_reasoning_effort=xhigh"}
		assert.Equal(t, want, call.Args)
	})

	t.Run("optional flags omitted", func(t *testing.T) {
		runner := fakeRunner("emit", path)
		c := executor.NewCodex(runner, executor.Opts{})
		_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)

		args := runner.CommandCalls()[0].Args
		assert.Equal(t, []string{"exec", "--sandbox", "read-only"}, args)
		assert.Contains(t, args, "read-only", "codex never gets a writable sandbox")
	})
}

func TestCodex_outputContract(t *testing.T) {
	tests := []struct {
		name   string
		schema json.RawMessage
		want   string
	}{
		{"finder stage", finding.FinderSchema(), "revmux finder findings"},
		{"verify stage", finding.VerifySchema(), "verdict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, codexCapture(t))
			c := executor.NewCodex(fakeRunner("echo", path), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "review this", Schema: tt.schema}, discardSink())
			require.NoError(t, err)

			assert.Contains(t, res.Raw, "review this")
			assert.Contains(t, res.Raw, "Return ONLY a JSON object matching the schema below")
			assert.Contains(t, res.Raw, string(tt.schema), "the stage's own schema is rendered inline")
			assert.Contains(t, res.Raw, tt.want)
		})
	}

	t.Run("no schema, no contract", func(t *testing.T) {
		path := writeFixture(t, codexCapture(t))
		c := executor.NewCodex(fakeRunner("echo", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "review this"}, discardSink())
		require.NoError(t, err)
		assert.Equal(t, "review this", res.Raw)
	})

	t.Run("a claude prompt never gets the codex contract", func(t *testing.T) {
		path := writeFixture(t, codexCapture(t))
		c := executor.NewClaude(fakeRunner("echo", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "review this", Schema: finding.FinderSchema()}, discardSink())
		require.NoError(t, err)
		assert.NotContains(t, res.Raw, "Return ONLY a JSON object", "claude gets its output contract from --json-schema")
		assert.Contains(t, res.Raw, "narrate",
			"it gets the narration contract instead, because the schema otherwise leaves it with nothing to say")
	})
}

func TestCodex_extract(t *testing.T) {
	tests := []struct {
		name    string
		output  []byte
		wantErr bool
	}{
		{name: "clean", output: codexCapture(t)},
		{name: "prose wrapped", output: codexProseCapture(t)},
		{name: "no json", output: codexNoJSONCapture(t), wantErr: true},
		{name: "stalled mid-answer", output: codexStalledCapture(t), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, tt.output)
			c := executor.NewCodex(fakeRunner("emit", path), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
			require.NoError(t, err, "output holding no answer degrades the source, it does not fail the run")

			if tt.wantErr {
				assert.Empty(t, res.StructuredOutput)
				return
			}
			var out struct {
				Findings []struct {
					File   string   `json:"file"`
					Lenses []string `json:"lenses"`
				} `json:"findings"`
			}
			require.NoError(t, json.Unmarshal(res.StructuredOutput, &out))
			assert.NotEmpty(t, out.Findings)
			assert.NotEmpty(t, out.Findings[0].File)
			assert.NotEmpty(t, out.Findings[0].Lenses)
		})
	}
}

func TestCodex_Run_clean(t *testing.T) {
	data := codexCapture(t)
	path := writeFixture(t, data)
	raw := &bytes.Buffer{}
	sink := discardSink()

	c := executor.NewCodex(fakeRunner("emit", path), executor.Opts{})
	req := executor.Request{Prompt: "x", Model: "gpt-5.6-sol", RawOutput: raw}
	res, err := c.Run(context.Background(), req, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	assert.False(t, res.RateLimited)
	assert.NotEmpty(t, res.StructuredOutput)
	assert.Equal(t, "gpt-5.6-sol", res.RequestedModel)
	assert.Equal(t, data, raw.Bytes(), "the archive keeps the bytes the process produced")
}

func TestCodex_Run_firstStdoutWriteEmitsActivity(t *testing.T) {
	path := writeFixture(t, codexCapture(t))
	sink := discardSink()
	clk := &mocks.ClockMock{}

	// "fail" rather than "emit": a clean exit emits no lifecycle event at all, and the ordering below
	// needs the reaping to be observable to prove the release does not wait for it
	c := executor.NewCodex(fakeRunner("fail", path), executor.Opts{Clock: clk})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	// progress rather than activity: the write is codex answering, long after its rollout has narrated
	// what it was doing, so it opens the gate without appending a bare line to a log that said it all
	kinds := eventKinds(sink)
	release := slices.Index(kinds, executor.EventProgress)
	require.GreaterOrEqual(t, release, 0, "the raw stdout write is a codex leader's fallback release signal")
	reaped := slices.Index(kinds, executor.EventFinished)
	require.GreaterOrEqual(t, reaped, 0, "a non-zero exit is still reported")
	assert.Less(t, release, reaped, "the release arrives on the write, not when the process is reaped")
	assert.Empty(t, clk.AfterFuncCalls(), "the release came from the write, never from a timer")
}

func TestCodex_Run_stderrHeader(t *testing.T) {
	header := []string{"model: gpt-5.6-sol", "sandbox: read-only", "reasoning effort: high"}

	// the header lines are forwarded once each and the rest of the banner is dropped. Their position
	// among the stdout events is deliberately not asserted: stderr is drained alongside the stdout
	// parse, so whether a header line or the first raw write reaches the sink first is unspecified.
	run := func(t *testing.T, stderr []byte) []string {
		t.Helper()
		path := writeFixture(t, codexCapture(t))
		errPath := writeFixture(t, stderr)
		sink := discardSink()

		c := executor.NewCodex(fakeRunner("emit", path, errPath), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "requested"}, sink)
		require.NoError(t, err)

		assert.Equal(t, "requested", res.RequestedModel)
		assert.Equal(t, "gpt-5.6-sol", res.ActualModel, "the report states what actually ran")

		texts := eventTexts(sink, executor.EventInfo)
		for _, text := range texts {
			assert.NotContains(t, text, "session id", "the rest of the banner is suppressed")
			assert.NotContains(t, text, "provider")
			assert.NotContains(t, text, "hook:")
		}
		// codex prints the banner before it has contacted a model, so a header must never arrive as
		// activity: that kind is a stagger leader's release signal and the banner proves nothing
		for _, text := range eventTexts(sink, executor.EventActivity) {
			for _, h := range header {
				assert.NotEqual(t, h, text, "a resolved-config line is not activity")
			}
		}
		return texts
	}

	t.Run("as recorded", func(t *testing.T) {
		texts := run(t, codexStderrCapture(t))
		for _, want := range header {
			assert.Contains(t, texts, want)
		}
	})

	t.Run("a banner printed twice is still forwarded once", func(t *testing.T) {
		joined := strings.Join(run(t, codexRepeatedBannerCapture(t)), "\n")
		for _, want := range header {
			assert.Equal(t, 1, strings.Count(joined, want), "%q must reach the sink once per process", want)
		}
	})
}

func TestCodex_Run_tokensFromStderrFooter(t *testing.T) {
	// codex has no result event carrying usage, so the footer it prints on stderr is the only count
	path := writeFixture(t, codexCapture(t))

	tests := []struct {
		name   string
		stderr string
		want   int
	}{
		{name: "as recorded", stderr: string(codexStderrCapture(t)), want: 13549},
		{name: "the last footer wins", stderr: "tokens used\n10\ntokens used\n2,500\n", want: 2500},
		{name: "an echoed prompt line is not a total", stderr: "tokens used\nis what the reviewed code logs\n", want: 0},
		{name: "no footer at all", stderr: "model: gpt-5.6-sol\n", want: 0},
		{name: "an echoed marker and number the run never printed a footer after", stderr: "tokens used\n123\nhook: Stop\n", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errPath := writeFixture(t, []byte(tt.stderr))
			c := executor.NewCodex(fakeRunner("emit", path, errPath), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.Tokens)
		})
	}
}

func TestCodex_Run_failedRunReportsNoEchoedTokens(t *testing.T) {
	// codex echoes the whole prompt to stderr, so a lens body can carry the marker and a number on
	// consecutive lines. A run that then fails never prints the real footer over it, and charging the
	// failed attempt for whatever the prompt happened to contain is worse than reporting nothing
	echoed := "user\ntokens used\n123\nERROR: unexpected argument --nope\n"
	path := writeFixture(t, nil)
	errPath := writeFixture(t, []byte(echoed))

	c := executor.NewCodex(fakeRunner("fail", path, errPath), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())

	require.Error(t, err)
	assert.Equal(t, 3, res.ExitCode)
	assert.Equal(t, 0, res.Tokens, "no footer arrived, so the run spent nothing it can report")
}

func TestCodex_Run_stderrKeepsTheWatchdogAlive(t *testing.T) {
	// codex reports its reasoning and every tool call on stderr and writes stdout only when it answers,
	// so a watchdog ticking on stdout alone kills a healthy run at the idle timeout
	//
	// CODEX_HOME is redirected because the recorded stderr carries a real session id: left alone, the
	// rollout tail globs the developer's own ~/.codex, finds that session and touches the watchdog from
	// outside the test — which both breaks the count below and reads a directory no test may touch
	t.Setenv("CODEX_HOME", t.TempDir())
	path := writeFixture(t, nil)
	errPath := writeFixture(t, codexStderrCapture(t))

	timer := &mocks.TimerMock{
		StopFunc:  func() bool { return true },
		ResetFunc: func(time.Duration) bool { return true },
	}
	clk := &mocks.ClockMock{
		NowFunc:       func() time.Time { return time.Unix(0, 0).UTC() },
		AfterFuncFunc: func(time.Duration, func()) executor.Timer { return timer },
	}

	sink := discardSink()
	c := executor.NewCodex(fakeRunner("emit", path, errPath), executor.Opts{IdleTimeout: time.Minute, Clock: clk})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	assert.False(t, res.IdleTimedOut)
	assert.Empty(t, res.Raw, "the process wrote nothing to stdout, so only stderr could have touched it")

	// per line, not per line the filter keeps: the run's reasoning and tool chatter are the only liveness
	// a long codex run has, and a watchdog touched by the banner alone still kills it at the idle timeout
	lines := codexStderrLines(t)
	assert.Len(t, timer.ResetCalls(), lines, "every stderr line is liveness")
	assert.Greater(t, lines, len(eventTexts(sink, executor.EventInfo)), "the fixture is more than its banner")
	for _, call := range timer.ResetCalls() {
		assert.Equal(t, time.Minute, call.D, "a touch rearms the configured idle timeout, not a zero one")
	}
}

func TestCodex_Run_patternTiers(t *testing.T) {
	tests := []struct {
		name        string
		stdout      string
		stderr      string
		wantErr     string
		wantLimited bool
	}{
		{name: "transient server hiccup is a retryable failure", stdout: "stream failed\nAPI Error: 503 upstream unavailable\n",
			wantErr: "codex transient failure: api error: 503"},
		{name: "quota diagnostic is a limit", stderr: "ERROR: You've hit your usage limit.\n", wantLimited: true},
		{name: "rate limit in the output tail is a limit", stdout: "429 Too Many Requests\n", wantLimited: true},
		{name: "any other diagnostic is a hard error", stderr: "ERROR: unexpected argument --nope\n",
			wantErr: "codex failed: ERROR: unexpected argument --nope"},
		{name: "500 is not transient", stderr: "ERROR: API Error: 500 internal\n",
			wantErr: "codex failed: ERROR: API Error: 500 internal"},
		// codex echoes the whole prompt to stderr before it reports anything of its own, so the first
		// "error:" line is routinely one the prompt quoted and the real diagnostic is the last printed
		{name: "an echoed prompt line does not shadow the real diagnostic",
			stderr:  "error: the write error is dropped here\nERROR: unexpected argument --nope\n",
			wantErr: "codex failed: ERROR: unexpected argument --nope"},
		{name: "an echoed prompt line does not manufacture a rate limit",
			stderr:  "error: retry once you've hit your usage limit\nERROR: unexpected argument --nope\n",
			wantErr: "codex failed: ERROR: unexpected argument --nope"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, []byte(tt.stdout))
			errPath := writeFixture(t, []byte(tt.stderr))
			sink := discardSink()

			c := executor.NewCodex(fakeRunner("fail", path, errPath), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)

			assert.Equal(t, 3, res.ExitCode)
			assert.Equal(t, tt.wantLimited, res.RateLimited)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Contains(t, eventKinds(sink), executor.EventRateLimit)
				assert.NotEmpty(t, res.RateLimit.RateLimitType)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCodex_Run_cleanExitIsNeverAPatternMatch(t *testing.T) {
	// a findings body reviewing rate-limit handling contains the words itself
	body := append(codexCapture(t), []byte("\nRate limit exceeded is unhandled in the reviewed code.\n")...)
	path := writeFixture(t, body)
	sink := discardSink()

	c := executor.NewCodex(fakeRunner("emit", path), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	assert.Equal(t, 0, res.ExitCode)
	assert.False(t, res.RateLimited, "patterns are only consulted when the process failed")
	assert.NotContains(t, eventKinds(sink), executor.EventRateLimit)
	assert.NotEmpty(t, res.StructuredOutput, "the answer is still extracted")
}

func TestCodex_Run_patternsSeeOnlyTheTail(t *testing.T) {
	// a long findings body naming a limit early on, then 64k of ordinary review prose after it
	const phrase = "Rate limit exceeded is unhandled at line 40.\n"
	filler := bytes.Repeat([]byte("the handler returns the wrapped error to its caller.\n"), 1200)

	t.Run("a match buried above the tail is invisible", func(t *testing.T) {
		path := writeFixture(t, append([]byte(phrase), filler...))
		sink := discardSink()

		c := executor.NewCodex(fakeRunner("fail", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
		require.NoError(t, err)

		assert.Equal(t, 3, res.ExitCode)
		assert.False(t, res.RateLimited, "matching the whole output self-triggers on findings about rate limits")
		assert.NotContains(t, eventKinds(sink), executor.EventRateLimit)
	})

	t.Run("the same phrase in the tail is a limit", func(t *testing.T) {
		path := writeFixture(t, append(filler, []byte(phrase)...))

		c := executor.NewCodex(fakeRunner("fail", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)

		assert.True(t, res.RateLimited)
		assert.Equal(t, "rate limit exceeded", res.RateLimit.RateLimitType)
	})
}

func TestCodex_Run_planQuotaOnStderrWithEmptyStdout(t *testing.T) {
	path := writeFixture(t, nil)
	errPath := writeFixture(t, []byte("OpenAI Codex v0.145.0\nERROR: You've hit your usage limit.\n"))
	sink := discardSink()

	c := executor.NewCodex(fakeRunner("fail", path, errPath), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	assert.Empty(t, res.Raw, "a stdout-only check would miss this entirely")
	assert.True(t, res.RateLimited)
	assert.Equal(t, "you've hit your usage limit", res.RateLimit.RateLimitType)
	assert.Contains(t, eventKinds(sink), executor.EventRateLimit)
}

func TestCodex_Run_canceledContextSkipsPatterns(t *testing.T) {
	path := writeFixture(t, []byte("429 Too Many Requests\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := executor.NewCodex(fakeRunner("fail", path), executor.Opts{})
	res, err := c.Run(ctx, executor.Request{Prompt: "x"}, discardSink())
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, res.RateLimited, "a canceled run's tail is meaningless")
}

func TestCodex_Run_rolloutKeepsTheWatchdogAlive(t *testing.T) {
	// codex's own liveness during a long review: both pipes are silent for minutes while it reasons,
	// and the rollout is the only thing moving. A watchdog that cannot see it kills a working run,
	// retries it into the same wall and degrades the source — observed doing exactly that.
	const session = "019fa0ca-1111-2222-3333-444455556666"
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dir := filepath.Join(home, "sessions", "2026", "07", "26")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	rollout := filepath.Join(dir, "rollout-2026-07-26T18-38-04-"+session+".jsonl")
	require.NoError(t, os.WriteFile(rollout, []byte(
		`{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**Analyzing the stagger gate**"}]}}`+"\n"),
		0o600))

	// the process is held open until the rollout's own touch has landed, and that is what makes the
	// count readable: Run withdraws the touch as soon as the process exits, so a child that finishes
	// first leaves the tail calling a withdrawn closure and the assertion sampling a run it lost a race
	// to. The deadline bounds the case where the touch never comes at all.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var resets atomic.Int64
	clk := &mocks.ClockMock{
		NowFunc: func() time.Time { return time.Unix(0, 0).UTC() },
		AfterFuncFunc: func(time.Duration, func()) executor.Timer {
			return &mocks.TimerMock{
				StopFunc: func() bool { return true },
				ResetFunc: func(time.Duration) bool {
					if resets.Add(1) > 1 {
						cancel()
					}
					return true
				},
			}
		},
	}

	// stdout is empty and stderr carries exactly one line, so the pipes account for exactly one reset
	// between them. That is the baseline, and it is what makes any further reset attributable to the
	// rollout rather than to pipe traffic — the shape that starved the watchdog in the first place.
	out := writeFixture(t, nil)
	errPath := writeFixture(t, []byte("session id: "+session+"\n"))

	seen := make(chan struct{})
	var once sync.Once
	sink := &mocks.EventSinkMock{EmitFunc: func(ev executor.Event) {
		if ev.Kind == executor.EventActivity && strings.Contains(ev.Text, "Analyzing the stagger gate") {
			once.Do(func() { close(seen) })
		}
	}}

	c := executor.NewCodex(fakeRunner("stall", out, errPath), executor.Opts{IdleTimeout: time.Minute, Clock: clk})
	_, err := c.Run(ctx, executor.Request{Prompt: "x"}, sink)

	select {
	case <-seen:
	default:
		t.Fatal("the rollout record never reached the sink, so the touch cannot be asserted")
	}
	assert.Greater(t, resets.Load(), int64(1),
		"the one stderr line is the only reset the pipes can account for, so a rollout record must add its own — "+
			"without it codex is killed at the idle timeout while it is reasoning")
	assert.ErrorIs(t, err, context.Canceled, "the run ended because the rollout touched, not because the deadline expired")
}

func TestCodex_Run_rolloutLivenessIsNotTheDisplayFilter(t *testing.T) {
	// a rollout record that renders nothing is still codex working: function_call_output, event_msg
	// and a reasoning record with an empty summary all advance the file without producing an event.
	// Touching from the sink would tie the watchdog to what happens to be worth displaying, which is
	// the same starvation the tail exists to prevent, one layer in.
	const session = "019fa0ca-aaaa-bbbb-cccc-ddddeeeeffff"
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dir := filepath.Join(home, "sessions", "2026", "07", "26")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	rollout := filepath.Join(dir, "rollout-2026-07-26T19-00-00-"+session+".jsonl")
	require.NoError(t, os.WriteFile(rollout, []byte(
		`{"type":"response_item","payload":{"type":"function_call_output","output":"ok"}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"token_count"}}`+"\n"), 0o600))

	var resets atomic.Int64
	clk := &mocks.ClockMock{
		NowFunc: func() time.Time { return time.Unix(0, 0).UTC() },
		AfterFuncFunc: func(time.Duration, func()) executor.Timer {
			return &mocks.TimerMock{
				StopFunc:  func() bool { return true },
				ResetFunc: func(time.Duration) bool { resets.Add(1); return true },
			}
		},
	}

	sink := discardSink()
	c := executor.NewCodex(fakeRunner("emit", writeFixture(t, nil), writeFixture(t, []byte("session id: "+session+"\n"))),
		executor.Opts{IdleTimeout: time.Minute, Clock: clk})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	var rollouts int
	for _, call := range sink.EmitCalls() {
		if call.Event.Kind == executor.EventActivity {
			rollouts++
		}
	}
	require.Zero(t, rollouts, "neither record renders, which is the whole point of the case")
	assert.Greater(t, resets.Load(), int64(1),
		"the file advancing is the liveness, not the subset of records worth showing")
}
