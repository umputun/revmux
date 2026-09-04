package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	emocks "github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/prompt"
)

func TestFinder_run(t *testing.T) {
	t.Run("collects one result per roster entry", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":80,"title":"one"}`), Tokens: 5},
			"impl": {StructuredOutput: findingsJSON(`{"file":"b.go","line":2,"severity":"minor","confidence":60,"title":"two"}`), Tokens: 6},
		})

		f := h.finder(func(Event) {})
		got, err := f.run(context.Background())
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "bugs", got[0].spec.Name)
		assert.Equal(t, "impl", got[1].spec.Name)
		assert.True(t, got[0].ok())
		assert.Len(t, got[0].findings, 1)
	})

	t.Run("empty roster", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Roster = nil
		f := h.finder(func(Event) {})
		_, err := f.run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "roster is empty")
	})

	t.Run("one degraded source still reports", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":80,"title":"one"}`)},
		})

		f := h.finder(func(Event) {})
		got, err := f.run(context.Background())
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.True(t, got[0].ok())
		assert.False(t, got[1].ok())
	})

	t.Run("every source degraded is a tool error", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				return executor.Result{}, errors.New("stalled twice")
			}}
		}

		f := h.finder(func(Event) {})
		_, err := f.run(context.Background())
		require.Error(t, err)
		assert.ErrorIs(t, err, errNoSources)
	})
}

func TestFinder_runAgent(t *testing.T) {
	t.Run("tees raw output to the archive verbatim and closes it", func(t *testing.T) {
		// a stream cut mid-line is exactly the one worth having on disk, so the tee must not tidy it
		const stream = "{\"type\":\"system\"}\n{\"type\":\"resu"
		h := newHarness(t)
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				_, err := req.RawOutput.Write([]byte(stream))
				require.NoError(t, err)
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok())

		raw := h.get("agents/bugs.jsonl")
		require.NotNil(t, raw)
		//nolint:testifylint // byte-exactness is the assertion; JSONEq would pass on reformatted bytes
		assert.Equal(t, stream, raw.String(), "the tee is verbatim, byte for byte")
		assert.True(t, raw.isClosed(), "the agent goroutine owns its own tee")
	})

	t.Run("a tee close failure degrades the source", func(t *testing.T) {
		h := newHarness(t)
		h.archived["agents/bugs.jsonl"] = &syncBuffer{closeErr: errors.New("close failed")}
		h.archived["agents/bugs.retry.jsonl"] = &syncBuffer{closeErr: errors.New("close failed")}
		h.cfg.NewRunner = h.runner(map[string]executor.Result{"bugs": {StructuredOutput: findingsJSON(``)}})

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.False(t, res.ok())
		assert.Contains(t, res.err.Error(), "close raw output")
	})

	t.Run("a tee that cannot be opened degrades the source", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Archive = &mocks.ArchiverMock{WriterFunc: func(string) (io.WriteCloser, error) {
			return nil, errors.New("disk full")
		}}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.False(t, res.ok())
		assert.Contains(t, res.err.Error(), "open raw output")
	})

	t.Run("an unresolved variable degrades the source", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Vars = prompt.Vars{}
		h.cfg.NewRunner = h.runner(map[string]executor.Result{"bugs": {StructuredOutput: findingsJSON(``)}})

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.False(t, res.ok())
		assert.Contains(t, res.err.Error(), "unresolved variable")
	})

	t.Run("retries once, then degrades once, and reports no findings", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				return executor.Result{Tokens: 9, ActualModel: "claude-opus-5"}, errors.New("boom")
			}}
		}

		var kinds []EventKind
		f := h.finder(func(ev Event) { kinds = append(kinds, ev.Kind) })
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.False(t, res.ok())
		assert.Equal(t, []EventKind{EventAgentStarted, EventAgentRetried, EventAgentDegraded}, kinds)
		assert.Equal(t, 18, res.stat.Tokens, "both attempts spent tokens and both are reported")
		assert.Equal(t, "claude-opus-5", res.stat.ActualModel)
	})

	t.Run("the retry writes its own raw file rather than splicing onto the first", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				if attempts.Add(1) == 1 {
					_, _ = req.RawOutput.Write([]byte(`{"type":"resu`))
					return executor.Result{IdleTimedOut: true}, nil
				}
				_, _ = req.RawOutput.Write([]byte(`{"type":"result"}`))
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		require.True(t, f.runAgent(context.Background(), h.cfg.Roster[0], 0).ok())
		//nolint:testifylint // byte-exactness is the assertion, and the stalled attempt is not valid JSON at all
		assert.Equal(t, `{"type":"resu`, h.get("agents/bugs.jsonl").String(), "the stalled attempt is kept whole")
		//nolint:testifylint // same: the retry file must be byte-identical, not merely equivalent
		assert.Equal(t, `{"type":"result"}`, h.get("agents/bugs.retry.jsonl").String())
	})

	t.Run("the request carries the finder schema and the agent's model", func(t *testing.T) {
		h := newHarness(t)
		var got executor.Request
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				got = req
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		require.True(t, f.runAgent(context.Background(), h.cfg.Roster[0], 0).ok())
		assert.Equal(t, "opus", got.Model)
		assert.Equal(t, "high", got.Effort)
		assert.JSONEq(t, string(finding.FinderSchema()), string(got.Schema))
		assert.Contains(t, got.Prompt, "review the change", "the profile body")
		assert.Contains(t, got.Prompt, "lens bugs", "the agent's own lens")
		assert.NotContains(t, got.Prompt, "lens impl", "and nobody else's")
		assert.Contains(t, got.Prompt, "/abs/scope.md", "variables expand to paths")
	})

	t.Run("the runner spec names the agent's executor", func(t *testing.T) {
		h := newHarness(t)
		var mu sync.Mutex
		var specs []RunnerSpec
		h.cfg.NewRunner = func(spec RunnerSpec) Runner {
			mu.Lock()
			specs = append(specs, spec)
			mu.Unlock()
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		_, err := f.run(context.Background())
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []RunnerSpec{
			{Executor: "claude", Model: "opus", Effort: "high"},
			{Executor: "claude", Model: "opus", Effort: "high"},
		}, specs)
	})
}

func TestFinder_retry(t *testing.T) {
	tests := []struct {
		name  string
		first executor.Result
		err   error
	}{
		{name: "a stall", first: executor.Result{IdleTimedOut: true}},
		{name: "a dead process", first: executor.Result{ExitCode: 1}},
		{name: "a rate limit", first: executor.Result{RateLimited: true, RateLimit: executor.RateLimitInfo{Status: "rejected"}}},
		{name: "a transport error", err: errors.New("pipe closed")},
	}

	for _, tt := range tests {
		t.Run(tt.name+" is retried and the second attempt reports", func(t *testing.T) {
			h := newHarness(t)
			var attempts atomic.Int64
			h.cfg.NewRunner = func(RunnerSpec) Runner {
				return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
					if attempts.Add(1) == 1 {
						return tt.first, tt.err
					}
					return executor.Result{StructuredOutput: findingsJSON(
						`{"file":"a.go","line":1,"severity":"major","confidence":85,"title":"survived"}`)}, nil
				}}
			}

			var kinds []EventKind
			f := h.finder(func(ev Event) { kinds = append(kinds, ev.Kind) })
			res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
			require.True(t, res.ok(), "a retry that succeeds is not a degraded source")
			require.Len(t, res.findings, 1)
			assert.Equal(t, int64(2), attempts.Load(), "exactly one retry")
			assert.Equal(t, []EventKind{EventAgentStarted, EventAgentRetried, EventFindings, EventAgentDone}, kinds)
		})
	}

	t.Run("a stall carrying structured output is not retried", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{IdleTimedOut: true, StructuredOutput: findingsJSON(
					`{"file":"a.go","line":1,"severity":"major","confidence":85,"title":"delivered"}`)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok(), "the watchdog fired on the teardown, not on a dead agent")
		require.Len(t, res.findings, 1)
		assert.Equal(t, "delivered", res.findings[0].Title)
		assert.Equal(t, int64(1), attempts.Load(), "a retry would pay for the same findings twice")
	})

	t.Run("a stall reaped by signal is not retried on its exit code", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{IdleTimedOut: true, ExitCode: -1, StructuredOutput: findingsJSON(
					`{"file":"a.go","line":1,"severity":"major","confidence":85,"title":"delivered"}`)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok(), "the watchdog kill set that exit code, and the answer is already whole")
		require.Len(t, res.findings, 1)
		assert.Equal(t, "delivered", res.findings[0].Title)
		assert.Equal(t, int64(1), attempts.Load(), "the stall carve-out is dead if the exit code retries anyway")
	})

	t.Run("a rate limit carrying structured output is not retried", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{
					RateLimited: true, RateLimit: executor.RateLimitInfo{Status: "rejected"},
					StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":85,"title":"delivered"}`),
				}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok(), "the CLI survived the limit and returned the whole answer")
		require.Len(t, res.findings, 1)
		assert.Equal(t, "delivered", res.findings[0].Title)
		assert.Equal(t, int64(1), attempts.Load(), "a retry would hit the same limit and degrade a source that reported")
	})

	t.Run("the first attempt's context is canceled before the second runs", func(t *testing.T) {
		h := newHarness(t)
		var mu sync.Mutex
		var seen []context.Context
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(ctx context.Context, _ executor.Request, _ executor.EventSink) (executor.Result, error) {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, ctx)
				if len(seen) == 1 {
					return executor.Result{IdleTimedOut: true}, nil
				}
				require.Error(t, seen[0].Err(), "the stalled attempt must be torn down before its replacement starts")
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		require.True(t, f.runAgent(context.Background(), h.cfg.Roster[0], 0).ok())
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, seen, 2)
		assert.NotSame(t, seen[0], seen[1], "each attempt owns its own context")
	})

	t.Run("a clean exit carrying nothing is retried", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				if attempts.Add(1) == 1 {
					return executor.Result{}, nil
				}
				return executor.Result{StructuredOutput: findingsJSON(
					`{"file":"a.go","line":1,"severity":"major","confidence":85,"title":"delivered"}`)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok(), "the second attempt delivered")
		require.Len(t, res.findings, 1)
		assert.Equal(t, "delivered", res.findings[0].Title)
		assert.Equal(t, int64(2), attempts.Load(), "an empty answer is owed the same retry a crash gets")
	})

	t.Run("a clean exit carrying nothing twice degrades the source", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.False(t, res.ok())
		assert.Contains(t, res.err.Error(), "no structured output")
		assert.Equal(t, int64(2), attempts.Load(), "retry once, then degrade")
	})

	t.Run("an empty findings list is delivered, not retried", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{StructuredOutput: findingsJSON()}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok(), "an agent that found nothing answered the question")
		assert.Empty(t, res.findings)
		assert.Equal(t, int64(1), attempts.Load(), "a clean nothing-found must not be retried as an empty answer")
	})

	t.Run("a parse failure is not retried", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{StructuredOutput: json.RawMessage(`{"findings":`)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.False(t, res.ok())
		assert.Equal(t, int64(1), attempts.Load(), "a malformed answer is a delivered one, not a stall")
	})

	t.Run("an answer of another shape degrades the source rather than reporting nothing", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{StructuredOutput: json.RawMessage(`{"note":"here is an example finding"}`)}, nil
			}}
		}

		var seen []Event
		var mu sync.Mutex
		f := h.finder(func(ev Event) { mu.Lock(); seen = append(seen, ev); mu.Unlock() })
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.False(t, res.ok(), "a source that answered something else has not reported")
		assert.Equal(t, int64(1), attempts.Load(), "a wrong-shaped answer is a delivered one, not a stall")

		degraded := 0
		for _, ev := range seen {
			if ev.Kind == EventAgentDegraded {
				degraded++
			}
		}
		assert.Equal(t, 1, degraded, "silently counting it as zero findings is what hides a dead source")
	})

	t.Run("a canceled run degrades without a second attempt", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				attempts.Add(1)
				return executor.Result{}, errors.New("run stopped")
			}}
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f := h.finder(func(Event) {})
		res := f.runAgent(ctx, h.cfg.Roster[0], 0)
		require.False(t, res.ok())
		assert.Equal(t, int64(1), attempts.Load(), "retrying into a canceled context buys nothing")
	})
}

func TestFinder_retryPause(t *testing.T) {
	// a failing first attempt and a delivering second, so every case here reaches the pause
	retryThenDeliver := func(attempts *atomic.Int64) func(RunnerSpec) Runner {
		return func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				if attempts.Add(1) == 1 {
					return executor.Result{ExitCode: 1}, nil
				}
				return executor.Result{StructuredOutput: findingsJSON(
					`{"file":"a.go","line":1,"severity":"major","confidence":85,"title":"survived"}`)}, nil
			}}
		}
	}

	t.Run("the retry waits out the delay before it relaunches", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = retryThenDeliver(&attempts)
		h.cfg.RetryDelay = 2 * time.Second

		var armed []time.Duration
		var waited atomic.Int64
		h.cfg.Clock = &emocks.ClockMock{
			NowFunc: func() time.Time { return time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC) },
			AfterFuncFunc: func(d time.Duration, fire func()) executor.Timer {
				armed = append(armed, d)
				waited.Store(attempts.Load())
				fire()
				return &emocks.TimerMock{StopFunc: func() bool { return true }, ResetFunc: func(time.Duration) bool { return true }}
			},
		}

		var kinds []EventKind
		f := h.finder(func(ev Event) { kinds = append(kinds, ev.Kind) })
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok())
		assert.Equal(t, int64(2), attempts.Load())
		assert.Equal(t, int64(1), waited.Load(), "the wait falls between the two launches")
		require.Len(t, armed, 1, "one retry, one wait")
		assert.GreaterOrEqual(t, armed[0], h.cfg.RetryDelay, "the configured delay is the floor")
		assert.Less(t, armed[0], 2*h.cfg.RetryDelay, "and the jitter adds at most another of it")
		assert.Equal(t, []EventKind{EventAgentStarted, EventAgentRetried, EventFindings, EventAgentDone}, kinds)
	})

	t.Run("jitter separates agents that failed together", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.RetryDelay = time.Second
		f := h.finder(func(Event) {})

		seen := map[time.Duration]bool{}
		for range 50 {
			var d time.Duration
			f.cfg.Clock = &emocks.ClockMock{
				AfterFuncFunc: func(got time.Duration, fire func()) executor.Timer {
					d = got
					fire()
					return &emocks.TimerMock{StopFunc: func() bool { return true }, ResetFunc: func(time.Duration) bool { return true }}
				},
			}
			require.NoError(t, f.retryPause(context.Background()))
			seen[d] = true
		}
		assert.Greater(t, len(seen), 1, "a fixed delay would relaunch every agent into the same window")
	})

	t.Run("a non-positive delay relaunches without arming anything", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = retryThenDeliver(&attempts)

		clk := &emocks.ClockMock{
			NowFunc: func() time.Time { return time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC) },
			AfterFuncFunc: func(time.Duration, func()) executor.Timer {
				return &emocks.TimerMock{StopFunc: func() bool { return true }, ResetFunc: func(time.Duration) bool { return true }}
			},
		}
		h.cfg.Clock = clk

		f := h.finder(func(Event) {})
		res := f.runAgent(context.Background(), h.cfg.Roster[0], 0)
		require.True(t, res.ok())
		assert.Equal(t, int64(2), attempts.Load())
		assert.Empty(t, clk.AfterFuncCalls(), "nothing to wait on, so nothing armed")
	})

	t.Run("a cancellation while waiting stops the retry and reports none", func(t *testing.T) {
		h := newHarness(t)
		var attempts atomic.Int64
		h.cfg.NewRunner = retryThenDeliver(&attempts)
		h.cfg.RetryDelay = time.Hour

		ctx, cancel := context.WithCancel(context.Background())
		var stopped atomic.Int64
		h.cfg.Clock = &emocks.ClockMock{
			NowFunc: func() time.Time { return time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC) },
			AfterFuncFunc: func(time.Duration, func()) executor.Timer {
				cancel() // the wait is armed, so this is the run being torn down mid-pause
				return &emocks.TimerMock{
					StopFunc:  func() bool { stopped.Add(1); return true },
					ResetFunc: func(time.Duration) bool { return true },
				}
			},
		}

		var kinds []EventKind
		f := h.finder(func(ev Event) { kinds = append(kinds, ev.Kind) })
		res := f.runAgent(ctx, h.cfg.Roster[0], 0)
		cancel()
		require.False(t, res.ok(), "the first attempt's fault is what degraded the source")
		require.ErrorContains(t, res.err, "exited 1")
		assert.Equal(t, int64(1), attempts.Load(), "the second launch never happened")
		assert.Equal(t, int64(1), stopped.Load(), "the abandoned timer is stopped")
		assert.Equal(t, []EventKind{EventAgentStarted, EventAgentDegraded}, kinds,
			"app/archive counts agent_retried entries as retries, so one that never relaunched must not be recorded")
	})
}

func TestFinder_run_concurrency(t *testing.T) {
	t.Run("result order follows the roster, not completion order", func(t *testing.T) {
		h := newHarness(t)
		second := make(chan struct{})
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				if strings.Contains(req.Prompt, "lens bugs") {
					<-second // the first roster entry finishes last
					return executor.Result{StructuredOutput: findingsJSON(
						`{"file":"a.go","line":1,"severity":"major","confidence":80,"title":"from bugs"}`)}, nil
				}
				defer close(second)
				return executor.Result{StructuredOutput: findingsJSON(
					`{"file":"b.go","line":2,"severity":"minor","confidence":60,"title":"from impl"}`)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		got, err := f.run(context.Background())
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "bugs", got[0].spec.Name, "roster order survives reversed completion")
		assert.Equal(t, "impl", got[1].spec.Name)
		assert.Equal(t, "bugs-1", got[0].findings[0].ID)
	})

	t.Run("the leader's first activity releases the rest", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error) {
				if strings.Contains(req.Prompt, "lens bugs") {
					sink.Emit(executor.Event{Kind: executor.EventActivity, Text: "tool: Read"})
				}
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		// the delay never fires, so the run can only complete through the leader's activity
		s, _ := newHeldStagger(time.Hour, 0)
		f := &finder{cfg: h.cfg, save: h.save, emit: func(Event) {}, stagger: s}
		got, err := f.run(context.Background())
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.True(t, got[1].ok(), "the follower ran without waiting out the delay")
	})

	t.Run("only the leader's sink carries the release callback", func(t *testing.T) {
		h := newHarness(t)
		var mu sync.Mutex
		carriers := map[string]bool{}
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, _ executor.Request, es executor.EventSink) (executor.Result, error) {
				s, ok := es.(*sink)
				require.True(t, ok, "the executor gets the pipeline's own adapter")
				mu.Lock()
				carriers[s.agent] = s.first != nil
				mu.Unlock()
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		f := h.finder(func(Event) {})
		_, err := f.run(context.Background())
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, map[string]bool{"bugs": true, "impl": false}, carriers,
			"a follower opening the gate would release the roster before the leader proved auth")
	})

	t.Run("the leader's process starting does not release the rest", func(t *testing.T) {
		h := newHarness(t)
		started, hold := make(chan struct{}), make(chan struct{})
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error) {
				if strings.Contains(req.Prompt, "lens bugs") {
					// what proc emits the instant the fork succeeds, before a byte has been read
					sink.Emit(executor.Event{Kind: executor.EventInfo, Text: "model: opus"})
					close(started)
					<-hold
				}
				return executor.Result{StructuredOutput: findingsJSON(``)}, nil
			}}
		}

		s, fire := newHeldStagger(time.Hour, 0)
		f := &finder{cfg: h.cfg, save: h.save, emit: func(Event) {}, stagger: s}
		done := make(chan []sourceResult, 1)
		go func() {
			got, err := f.run(context.Background())
			assert.NoError(t, err)
			done <- got
		}()

		<-started
		assert.False(t, s.open(), "a launched process has proved nothing, so the gate must still be shut")
		fire()
		close(hold)
		assert.Len(t, <-done, 2)
	})

	t.Run("the delay releases the roster when the leader never emits", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(``)}, "impl": {StructuredOutput: findingsJSON(``)},
		})

		s, fire := newHeldStagger(time.Hour, 0)
		f := &finder{cfg: h.cfg, save: h.save, emit: func(Event) {}, stagger: s}
		done := make(chan []sourceResult, 1)
		go func() {
			got, err := f.run(context.Background())
			assert.NoError(t, err)
			done <- got
		}()

		fire() // nothing emits activity, so only the delay can release the follower
		assert.Len(t, <-done, 2)
	})
}

func TestFinder_parse(t *testing.T) {
	spec := prompt.AgentSpec{Name: "bugs+impl", Lenses: []string{"bugs", "impl"}}
	f := &finder{}

	t.Run("discards model-supplied sources, including a self-named pair", func(t *testing.T) {
		raw := findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":80,"title":"x","sources":["bugs+impl","bugs+impl"]}`)
		got, err := f.parse(spec, raw)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, []string{"bugs+impl"}, got[0].Sources, "one agent cannot corroborate itself")
	})

	t.Run("rewrites ids so two agents cannot collide", func(t *testing.T) {
		raw := findingsJSON(
			`{"id":"1","file":"a.go","line":1,"severity":"major","confidence":80,"title":"x"}`,
			`{"id":"1","file":"b.go","line":2,"severity":"minor","confidence":60,"title":"y"}`,
		)
		first, err := f.parse(prompt.AgentSpec{Name: "bugs", Lenses: []string{"bugs"}}, raw)
		require.NoError(t, err)
		second, err := f.parse(prompt.AgentSpec{Name: "impl", Lenses: []string{"impl"}}, raw)
		require.NoError(t, err)

		assert.Equal(t, []string{"bugs-1", "bugs-2"}, ids(first))
		assert.Equal(t, []string{"impl-1", "impl-2"}, ids(second))
		for _, id := range ids(first) {
			assert.NotContains(t, ids(second), id, "two agents both answering id 1 must not collide")
		}
	})

	t.Run("keeps only lenses the agent carries", func(t *testing.T) {
		raw := findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":80,"title":"x","lenses":["bugs","architecture"]}`)
		got, err := f.parse(spec, raw)
		require.NoError(t, err)
		assert.Equal(t, []string{"bugs"}, got[0].Lenses)
	})

	t.Run("falls back to the agent's full lens set", func(t *testing.T) {
		raw := findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":80,"title":"x","lenses":["architecture"]}`)
		got, err := f.parse(spec, raw)
		require.NoError(t, err)
		assert.Equal(t, []string{"bugs", "impl"}, got[0].Lenses)
	})

	t.Run("sources and lenses stay separate fields", func(t *testing.T) {
		raw := findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":80,"title":"x","lenses":["impl"]}`)
		got, err := f.parse(spec, raw)
		require.NoError(t, err)
		assert.Equal(t, []string{"bugs+impl"}, got[0].Sources)
		assert.Equal(t, []string{"impl"}, got[0].Lenses)
	})

	t.Run("an empty findings array is a valid answer", func(t *testing.T) {
		got, err := f.parse(spec, findingsJSON())
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("no structured output", func(t *testing.T) {
		_, err := f.parse(spec, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no structured output")
	})

	t.Run("malformed structured output", func(t *testing.T) {
		_, err := f.parse(spec, json.RawMessage(`{"findings":`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode findings")
	})

	t.Run("an object of another shape is not an empty answer", func(t *testing.T) {
		// codex has no --json-schema and extraction takes the first object in its prose, so a decodable
		// object carrying anything else must degrade the source rather than pass for zero findings
		for _, raw := range []string{`{"error":"no scope file"}`, `{}`, `{"verdicts":[]}`} {
			_, err := f.parse(spec, json.RawMessage(raw))
			require.Error(t, err, raw)
			assert.Contains(t, err.Error(), "no findings object", raw)
		}
	})
}

func TestFinder_promptName(t *testing.T) {
	f := &finder{}
	assert.Equal(t, "prompts/agents/bugs+impl.md", f.promptName(prompt.AgentSpec{Name: "bugs+impl"}))
}

func TestFinder_rawName(t *testing.T) {
	tests := []struct {
		name    string
		spec    prompt.AgentSpec
		attempt int
		want    string
	}{
		{name: "claude stream-json", spec: prompt.AgentSpec{Name: "bugs", Executor: "claude"}, want: "agents/bugs.jsonl"},
		{name: "codex prose", spec: prompt.AgentSpec{Name: "codex", Executor: "codex"}, want: "agents/codex.log"},
		{name: "an agent named events cannot collide", spec: prompt.AgentSpec{Name: "events", Executor: "claude"}, want: "agents/events.jsonl"},
		{name: "a claude retry keeps both attempts", spec: prompt.AgentSpec{Name: "bugs", Executor: "claude"}, attempt: 1, want: "agents/bugs.retry.jsonl"},
		{name: "a codex retry keeps both attempts", spec: prompt.AgentSpec{Name: "codex", Executor: "codex"}, attempt: 1, want: "agents/codex.retry.log"},
	}

	f := &finder{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, f.rawName(tt.spec, tt.attempt))
		})
	}
}

func TestFinder_report(t *testing.T) {
	f := &finder{}
	sources := []sourceResult{
		{
			spec:     prompt.AgentSpec{Name: "bugs"},
			stat:     finding.SourceStat{Name: "bugs", Tokens: 100, ActualModel: "claude-opus-5"},
			findings: []finding.Finding{{ID: "bugs-1", Title: "one"}},
		},
		{
			spec: prompt.AgentSpec{Name: "impl"},
			stat: finding.SourceStat{Name: "impl", Tokens: 40},
			err:  errors.New("stalled"),
		},
	}

	rep := f.report(sources)
	assert.Equal(t, 2, rep.Sources.Expected)
	assert.Equal(t, 1, rep.Sources.Reported)
	assert.Equal(t, []string{"impl"}, rep.Sources.DegradedSources)
	assert.True(t, rep.Sources.Degraded())
	assert.Equal(t, 140, rep.Stats.Tokens, "a degraded source still spent tokens")
	require.Len(t, rep.Findings, 1)
	assert.Equal(t, "bugs-1", rep.Findings[0].ID)
	require.Len(t, rep.Sources.Agents, 2)
	assert.False(t, rep.Sources.Agents[0].Degraded)
	assert.True(t, rep.Sources.Agents[1].Degraded)
	assert.Equal(t, 1, rep.Sources.Agents[0].Raised)
	assert.Equal(t, 0, rep.Sources.Agents[1].Raised, "a degraded source raised nothing")
}

// an agent whose tools are broken answers {"findings":[]} and is not degraded, so raised is the only
// field that separates it from one that looked and found nothing.
func TestFinder_reportRaisedNothing(t *testing.T) {
	f := &finder{}
	rep := f.report([]sourceResult{{
		spec: prompt.AgentSpec{Name: "cost"},
		stat: finding.SourceStat{Name: "cost", Tokens: 900},
	}})

	require.Len(t, rep.Sources.Agents, 1)
	assert.Equal(t, 0, rep.Sources.Agents[0].Raised)
	assert.False(t, rep.Sources.Agents[0].Degraded, "it answered, so it is not a degraded source")
	assert.Equal(t, 1, rep.Sources.Reported)
	assert.False(t, rep.Sources.Degraded())
}

func TestSourceResult_ok(t *testing.T) {
	assert.True(t, sourceResult{}.ok())
	assert.False(t, sourceResult{err: errors.New("boom")}.ok())
}

func ids(list []finding.Finding) []string {
	out := make([]string, 0, len(list))
	for _, f := range list {
		out = append(out, f.ID)
	}
	return out
}
