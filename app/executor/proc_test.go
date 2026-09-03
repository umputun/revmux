package executor_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

// helperCmd re-executes the test binary as a stand-in for a model CLI. Everything after "--" is ours:
// the testing package stops flag parsing there and leaves the rest in os.Args. An optional third
// argument names a file the helper writes to stderr, which is where codex puts its header and its
// plan-quota diagnostics.
func helperCmd(mode, path string, errPath ...string) *exec.Cmd {
	argv := append([]string{"-test.run=TestHelperProcess", "--", mode, path}, errPath...)
	//nolint:gosec // every argument comes from the test that builds this command
	return exec.Command(os.Args[0], argv...)
}

func fakeRunner(mode, path string, errPath ...string) *mocks.CommandRunnerMock {
	return &mocks.CommandRunnerMock{
		CommandFunc: func(_ context.Context, _ string, _ ...string) *exec.Cmd { return helperCmd(mode, path, errPath...) },
	}
}

func discardSink() *mocks.EventSinkMock {
	return &mocks.EventSinkMock{EmitFunc: func(executor.Event) {}}
}

// TestHelperProcess is the fake CLI. It returns immediately during a normal suite run.
// It exits through syscall.Exit, not os.Exit: os.Exit runs Go's coverage exit hook, which under a
// -coverpkg naming no dependency of this package writes "program not built with -cover" to this
// child's stderr and corrupts the fixture the spawning test asserts on.
func TestHelperProcess(t *testing.T) {
	idx := slices.Index(os.Args, "--")
	if idx < 0 || idx+2 >= len(os.Args) {
		return
	}
	mode, path := os.Args[idx+1], os.Args[idx+2]

	switch mode {
	case "env":
		fmt.Print(strings.Join(os.Environ(), "\n"))
		syscall.Exit(0)
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		syscall.Exit(0)
	}

	if idx+3 < len(os.Args) {
		errData, err := os.ReadFile(os.Args[idx+3]) //nolint:gosec // the path is built by the test that spawned this
		if err != nil {
			syscall.Exit(1)
		}
		if _, err := os.Stderr.Write(errData); err != nil {
			syscall.Exit(1)
		}
	}

	data, err := os.ReadFile(path) //nolint:gosec // the path is built by the test that spawned this
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		syscall.Exit(1)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		syscall.Exit(1)
	}

	switch mode {
	case "stall":
		time.Sleep(time.Minute)
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
	case "fail":
		syscall.Exit(3)
	}
	syscall.Exit(0)
}

func TestClaude_Run_scrubsChildEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("OPENCODE", "1")
	t.Setenv("ANTHROPIC_API_KEY", "secret-key")

	t.Run("stripped by default", func(t *testing.T) {
		c := executor.NewClaude(fakeRunner("env", "-"), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)
		assert.NotContains(t, res.Raw, "CLAUDECODE=")
		assert.NotContains(t, res.Raw, "OPENCODE=")
		assert.NotContains(t, res.Raw, "ANTHROPIC_API_KEY=")
		assert.Contains(t, res.Raw, "PATH=", "the rest of the environment is passed through")
	})

	t.Run("api key preserved on request", func(t *testing.T) {
		c := executor.NewClaude(fakeRunner("env", "-"), executor.Opts{PreserveAPIKey: true})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)
		assert.Contains(t, res.Raw, "ANTHROPIC_API_KEY=secret-key")
		assert.NotContains(t, res.Raw, "CLAUDECODE=", "never passed through")
		assert.NotContains(t, res.Raw, "OPENCODE=", "never passed through")
	})
}

func TestClaude_Run_teesRawOutput(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"clean stream", cleanCapture(t)},
		{"stream that fails to parse", truncatedCapture(t)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, tt.data)
			raw := &bytes.Buffer{}
			c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x", RawOutput: raw}, discardSink())
			require.NoError(t, err)
			assert.Equal(t, tt.data, raw.Bytes(), "archived bytes are what the process produced")
			assert.Equal(t, string(tt.data), res.Raw)
		})
	}
}

func TestProc_Run_drainsStderrWithoutAFilter(t *testing.T) {
	// claude supplies no stderr filter, and an unread pipe would fill and block the child
	noise := writeFixture(t, bytes.Repeat([]byte("chatter on stderr\n"), 8000))
	path := writeFixture(t, cleanCapture(t))

	c := executor.NewClaude(fakeRunner("emit", path, noise), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.NotEmpty(t, res.StructuredOutput)
	assert.NotContains(t, res.Raw, "chatter on stderr", "stderr never reaches the parsed stream")
}

func TestProc_Run_keepsDrainingStderrPastAnOverlongLine(t *testing.T) {
	// a line past the scanner cap ends the line reader but not the stream, and stopping there leaves the
	// pipe to fill and block the child mid-write — the run then dies at a timeout with stdout unread
	const lineCap = 8 << 20 // mirrors maxLineBytes

	var noise bytes.Buffer
	noise.Write(bytes.Repeat([]byte("x"), lineCap+1))
	noise.WriteString("\n")
	noise.Write(bytes.Repeat([]byte("chatter on stderr\n"), 20000)) // more than any pipe buffer holds

	errPath := writeFixture(t, noise.Bytes())
	path := writeFixture(t, cleanCapture(t))

	// a real bound rather than an injected clock: nothing waits on it when the drain works, and it is what
	// turns the regression from a hung test into a failed one
	c := executor.NewClaude(fakeRunner("emit", path, errPath), executor.Opts{HardTimeout: 20 * time.Second})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())

	require.NoError(t, err, "the child wrote all of stderr rather than blocking on a full pipe")
	assert.Equal(t, 0, res.ExitCode)
	assert.NotEmpty(t, res.StructuredOutput, "stdout is still parsed in full")
}

func TestClaude_Run_reportsExitCode(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("fail", path), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a non-zero exit is reported on the result, not as an error")
	assert.Equal(t, 3, res.ExitCode)
	assert.NotEmpty(t, res.StructuredOutput, "output produced before the failure is still parsed")
}

func TestClaude_Run_startFailure(t *testing.T) {
	runner := &mocks.CommandRunnerMock{
		CommandFunc: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "revmux-no-such-binary")
		},
	}
	c := executor.NewClaude(runner, executor.Opts{})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude")
}

func TestClaude_Run_nilClockDefaultsToReal(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{IdleTimeout: time.Hour})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a nil Opts.Clock must not panic on the first AfterFunc")
	assert.False(t, res.IdleTimedOut)
}

func TestClaude_Run_nilSink(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, res.StructuredOutput)
}

func TestClaude_Run_concurrentOnOneInstance(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{IdleTimeout: time.Hour})

	results := make([]executor.Result, 4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "opus"}, discardSink())
			assert.NoError(t, err)
			results[i] = res
		})
	}
	wg.Wait()

	for _, res := range results {
		assert.Equal(t, 0, res.ExitCode)
		assert.NotEmpty(t, res.StructuredOutput, "one executor instance holds no per-run state")
	}
}

func TestClaude_Run_parentCancel(t *testing.T) {
	path := writeFixture(t, stallCapture(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seen := make(chan struct{})
	var once sync.Once
	sink := &mocks.EventSinkMock{EmitFunc: func(ev executor.Event) {
		if ev.Kind == executor.EventActivity {
			once.Do(func() { close(seen) })
		}
	}}

	go func() {
		<-seen
		cancel()
	}()

	c := executor.NewClaude(fakeRunner("stall", path), executor.Opts{})
	res, err := c.Run(ctx, executor.Request{Prompt: "x"}, sink)
	require.ErrorIs(t, err, context.Canceled, "a canceled parent is an error, not an idle timeout")
	assert.False(t, res.IdleTimedOut)
}

// TestProc_run_emitsNothingAtFork pins the regression a comprehensive review caught: proc used to
// announce the fork, and the stagger's leader gate opens on any output-shaped event the sink sees, so
// the leader's own fork released the whole roster. The delay went dead and four agents hit the same
// auth wall together - the exact amplification the stagger exists to prevent.
func TestProc_run_emitsNothingAtFork(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	sink := discardSink()
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})

	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
	require.NoError(t, err)

	calls := sink.EmitCalls()
	require.NotEmpty(t, calls)
	for _, call := range calls {
		assert.NotEqual(t, "claude", call.Event.Text,
			"the bare binary name means a fork was announced, and a fork is not output")
	}

	// whatever arrives first must be something the process actually produced, never a lifecycle event
	first := calls[0].Event
	assert.NotEqual(t, executor.EventProgress, first.Kind,
		"a gate-opening kind must not be the first thing a run emits")
}

func TestProc_finishedEventOnlyOnFailure(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))

	t.Run("a clean exit is silent", func(t *testing.T) {
		sink := discardSink()
		c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
		require.NoError(t, err)
		require.Equal(t, 0, res.ExitCode)
		for _, call := range sink.EmitCalls() {
			assert.NotEqual(t, executor.EventFinished, call.Event.Kind,
				"exit 0 adds nothing the pipeline's own done event does not already carry")
		}
	})

	t.Run("a non-zero exit is reported with its code", func(t *testing.T) {
		sink := discardSink()
		c := executor.NewClaude(fakeRunner("fail", path), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, sink)
		require.NoError(t, err, "a bad exit comes back on the Result, not as an error")
		require.Equal(t, 3, res.ExitCode)

		var texts []string
		for _, call := range sink.EmitCalls() {
			if call.Event.Kind == executor.EventFinished {
				texts = append(texts, call.Event.Text)
			}
		}
		assert.Equal(t, []string{"exit 3"}, texts, "the code is what makes the line worth having")
	})
}
