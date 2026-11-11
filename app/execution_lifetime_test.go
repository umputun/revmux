package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/pipeline"
)

func TestExecutionLifetimeUnboundedOverridesConfiguration(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	writeConfig(t, user, "hard-timeout = 1ns\nidle-timeout = 1ns\n")
	writeConfig(t, filepath.Join(dir, projectDirName), "hard-timeout = 2ns\nidle-timeout = 2ns\n")
	o, err := parseArgs([]string{"--execution-lifetime=unbounded", "--config-dir", user})
	require.NoError(t, err)
	assert.Zero(t, o.HardTimeout)
	assert.Zero(t, o.IdleTimeout)
	assert.True(t, o.executorOpts(reviewContext{}, nil).Unbounded)
}

func TestExecutionLifetimeConflictingCLIOptions(t *testing.T) {
	isolate(t)
	for _, flag := range []string{"--hard-timeout=1h", "--idle-timeout=1s", "--hard-timeout=-1s"} {
		t.Run(flag, func(t *testing.T) {
			_, err := parseArgs([]string{"--execution-lifetime=unbounded", flag})
			require.ErrorContains(t, err, "conflicts with")
		})
	}
	_, err := parseArgs([]string{"--execution-lifetime=unbounded", "--hard-timeout=0s", "--idle-timeout=0s"})
	require.NoError(t, err)
	_, err = parseArgs([]string{"--execution-lifetime=forever"})
	require.Error(t, err)
}

func TestExecutionLifetimeBoundedRetainsExplicitWatchdogs(t *testing.T) {
	isolate(t)
	o, err := parseArgs([]string{"--execution-lifetime=bounded", "--hard-timeout=45m", "--idle-timeout=0s"})
	require.NoError(t, err)
	assert.Equal(t, 45*time.Minute, o.HardTimeout)
	assert.Zero(t, o.IdleTimeout)
	assert.False(t, o.executorOpts(reviewContext{}, nil).Unbounded)
	defaults, err := parseArgs(nil)
	require.NoError(t, err)
	assert.Equal(t, "bounded", defaults.ExecutionLifetime)
	assert.Equal(t, 20*time.Minute, defaults.HardTimeout)
	assert.Equal(t, 2*time.Minute, defaults.IdleTimeout)
}

func TestExecutionLifetimeUnboundedAtExecutorBoundary(t *testing.T) {
	isolate(t)
	o, err := parseArgs([]string{"--execution-lifetime=unbounded"})
	require.NoError(t, err)
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			clock := &mocks.ClockMock{NowFunc: time.Now}
			calls := 0
			runner := &mocks.CommandRunnerMock{CommandFunc: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				calls++
				_, hasDeadline := ctx.Deadline()
				assert.False(t, hasDeadline)
				return exec.CommandContext(ctx, "echo", "{}")
			}}
			opts := o.executorOpts(reviewContext{}, clock)
			opts.HardTimeout, opts.IdleTimeout = time.Nanosecond, time.Nanosecond
			var agent pipeline.Runner = executor.NewClaude(runner, opts)
			if name == "codex" {
				agent = executor.NewCodex(runner, opts)
			}
			result, runErr := agent.Run(context.Background(), executor.Request{Prompt: "x"}, nil)
			require.NoError(t, runErr)
			assert.Zero(t, result.ExitCode)
			assert.False(t, result.IdleTimedOut)
			assert.Equal(t, 1, calls)
			assert.Empty(t, clock.AfterFuncCalls())
		})
	}
}
