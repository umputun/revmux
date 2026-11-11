//go:build !windows

package executor

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pipeRunner struct{}

func (pipeRunner) Command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...) //nolint:gosec // executable and arguments are test fixtures
}

func TestProcUnboundedHeldPipes(t *testing.T) {
	for _, held := range []string{"stdout", "stderr"} {
		for _, withProof := range []bool{false, true} {
			t.Run(held+map[bool]string{false: "", true: "-proof"}[withProof], func(t *testing.T) {
				var proof *ProcessProof
				if withProof {
					var err error
					proof, err = NewProcessProof(filepath.Join(t.TempDir(), "proof.json"))
					require.NoError(t, err)
				}
				p := newProc("sh", pipeRunner{}, Opts{Unbounded: true, ProcessProof: proof})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				script := `sleep 86400 >&2 &`
				if held == "stdout" {
					script = `sleep 86400 2>&1 &`
				}
				script += `
i=0
while [ "$i" -lt 10000 ]; do
  printf 'stdout-%s\n' "$i"
  printf 'stderr-%s\n' "$i" >&2
  i=$((i+1))
done
exit 7`
				var stderr []string
				var parsed string
				var res Result
				var runErr error
				done := make(chan struct{})
				go func() {
					defer close(done)
					res, runErr = p.run(ctx, Request{}, runSpec{
						argv: []string{"-c", script},
						parse: func(_ context.Context, r io.Reader) Result {
							data, _ := io.ReadAll(r)
							parsed = string(data)
							return Result{}
						},
						stderrLine: func(line string) { stderr = append(stderr, line) },
					})
				}()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					cancel()
					<-done
					t.Fatal("direct-child exit did not release inherited pipes")
				}
				require.NoError(t, runErr)
				assert.Equal(t, 7, res.ExitCode)
				assert.False(t, res.IdleTimedOut)
				assert.Equal(t, parsed, res.Raw)
				assert.Len(t, strings.Split(strings.TrimSuffix(parsed, "\n"), "\n"), 10000)
				assert.True(t, strings.HasSuffix(parsed, "stdout-9999\n"))
				assert.Len(t, stderr, 10000)
				assert.Equal(t, "stderr-9999", stderr[len(stderr)-1])
				if proof != nil {
					require.NoError(t, proof.Finish())
					assert.Equal(t, "observed", proof.document.State)
				}
			})
		}
	}
}

func TestProcUnboundedCancelHeldPipes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proof, err := NewProcessProof(filepath.Join(t.TempDir(), "proof.json"))
	require.NoError(t, err)
	p := newProc("sh", pipeRunner{}, Opts{Unbounded: true, ProcessProof: proof})
	var res Result
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, runErr = p.run(ctx, Request{}, runSpec{
			argv: []string{"-c", `sleep 86400 & printf 'ready\n' >&2; wait`},
			parse: func(_ context.Context, r io.Reader) Result {
				_, _ = io.Copy(io.Discard, r)
				return Result{}
			},
			stderrLine: func(string) { cancel() },
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not release inherited pipes")
	}
	require.ErrorIs(t, runErr, context.Canceled)
	assert.NotEqual(t, 0, res.ExitCode)
	assert.False(t, res.IdleTimedOut)
	require.NoError(t, proof.Finish())
	assert.Equal(t, "observed", proof.document.State)
}

func TestProcDrainsOutputAfterParserReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newProc("sh", pipeRunner{}, Opts{Unbounded: true})
	var res Result
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, runErr = p.run(ctx, Request{}, runSpec{
			argv:  []string{"-c", `i=0; while [ "$i" -lt 20000 ]; do printf 'remaining output\n'; i=$((i+1)); done`},
			parse: func(context.Context, io.Reader) Result { return Result{} },
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("early parser return blocked the child")
	}
	require.NoError(t, runErr)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, strings.Repeat("remaining output\n", 20000), res.Raw)
}

func TestProcObservesAlreadyExitedChild(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 7")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	done := make(chan error, 1)
	go func() {
		if err := waitForProcessExit(cmd.Process.Pid); err != nil {
			done <- err
			return
		}
		done <- waitForProcessExit(cmd.Process.Pid)
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("already-exited child was not observed")
	}
	require.Error(t, cmd.Wait())
	assert.Equal(t, 7, cmd.ProcessState.ExitCode())
}
