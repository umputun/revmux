package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
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
	emocks "github.com/umputun/revmux/app/executor/mocks"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

func TestPipeline_Run(t *testing.T) {
	h := newHarness(t)
	h.cfg.NewRunner = h.runner(map[string]executor.Result{
		"bugs": {StructuredOutput: findingsJSON(`{"file":"a.go","line":10,"severity":"major","confidence":90,"title":"leak","lenses":["bugs"]}`), Tokens: 100, ActualModel: "claude-opus-5"},
		"impl": {StructuredOutput: findingsJSON(`{"file":"b.go","line":4,"severity":"minor","confidence":70,"title":"naming","lenses":["impl"]}`), Tokens: 40, ActualModel: "claude-opus-5"},
	})

	p := New(h.cfg)
	events := drain(p)
	rep, err := p.Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, finding.Scope{Task: "pr-1", Run: "round-1", ScopePath: "/abs/scope.md"}, rep.Scope)
	require.Len(t, rep.Findings, 2)
	assert.Equal(t, "bugs-1", rep.Findings[0].ID)
	assert.Equal(t, []string{"bugs"}, rep.Findings[0].Sources)
	assert.Equal(t, "impl-1", rep.Findings[1].ID)
	assert.Equal(t, []string{"impl"}, rep.Findings[1].Sources)

	assert.Equal(t, 2, rep.Sources.Expected)
	assert.Equal(t, 2, rep.Sources.Reported)
	assert.False(t, rep.Sources.Degraded())
	assert.Equal(t, 140, rep.Stats.Tokens, "run total is the sum of the per-agent counts")

	seen := <-events
	require.NotEmpty(t, seen)
	assert.Equal(t, EventStage, seen[0].Kind, "the stage change opens the run")

	// the roster runs concurrently, so agents interleave; what is fixed is each agent's own sequence
	byAgent := map[string][]EventKind{}
	for _, ev := range seen[1:] {
		byAgent[ev.Agent] = append(byAgent[ev.Agent], ev.Kind)
	}
	assert.Equal(t, map[string][]EventKind{
		"bugs": {EventAgentStarted, EventFindings, EventAgentDone},
		"impl": {EventAgentStarted, EventFindings, EventAgentDone},
	}, byAgent)
}

func TestPipeline_Run_stats(t *testing.T) {
	h := newHarness(t)
	h.cfg.NewRunner = h.runner(map[string]executor.Result{
		"bugs": {StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"minor","confidence":50,"title":"x"}`), Tokens: 7},
		"impl": {StructuredOutput: findingsJSON(``), Tokens: 3},
	})

	p := New(h.cfg)
	drain(p)
	rep, err := p.Run(context.Background())
	require.NoError(t, err)

	assert.False(t, rep.Stats.StartedAt.IsZero())
	assert.False(t, rep.Stats.FinishedAt.IsZero())
	assert.True(t, rep.Stats.FinishedAt.After(rep.Stats.StartedAt))
	assert.Positive(t, rep.Stats.DurationMS)
	require.Len(t, rep.Stats.Stages, 1, "one timing per stage that ran")
	assert.Equal(t, stageFind, rep.Stats.Stages[0].Name)
	assert.Positive(t, rep.Stats.Stages[0].DurationMS)
	assert.Empty(t, rep.Stats.Stages[0].Executor, "find has one runner per roster entry, not one of its own")
	assert.Equal(t, 10, rep.Stats.Tokens)
	assert.Equal(t, 10, rep.Stats.Stages[0].Tokens, "find carries its agents' sum, 7 + 3")
	assertStageTokensSum(t, rep)
}

// a profile can override which binary runs a stage, so a finished round that does not record the
// resolution cannot say which binary produced it — the archived prompt carries the path, not the model
func TestPipeline_Run_recordsTheStageRunner(t *testing.T) {
	h := newHarnessWith(t, map[string]string{"prompts/profiles/testprof.md": codexStageProfile})
	h.cfg.NoSynthesis, h.cfg.NoVerify = false, false
	h.cfg.NewRunner = h.runner(map[string]executor.Result{
		"bugs":         {StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"major","confidence":90,"title":"x"}`), Tokens: 100},
		"impl":         {StructuredOutput: findingsJSON(``), Tokens: 40},
		stageSynthesis: {StructuredOutput: json.RawMessage(`{"findings":[{"merged_ids":["bugs-1"],"file":"a.go","line":1,"severity":"major","confidence":90,"title":"x","body":"b"}]}`), Tokens: 200},
		stageVerify:    {StructuredOutput: json.RawMessage(`{"verdicts":[{"id":"bugs-1","verdict":"confirmed"}]}`), Tokens: 25},
	})

	p := New(h.cfg)
	drain(p)
	rep, err := p.Run(context.Background())
	require.NoError(t, err)

	byName := map[string]finding.StageRun{}
	for _, st := range rep.Stats.Stages {
		byName[st.Name] = st
	}
	require.Contains(t, byName, stageSynthesis)
	require.Contains(t, byName, stageVerify)
	assert.Equal(t, "codex", byName[stageSynthesis].Executor)
	assert.Equal(t, "gpt-5.6-sol", byName[stageSynthesis].Model)
	assert.Equal(t, "high", byName[stageSynthesis].Effort)
	assert.Equal(t, "codex", byName[stageVerify].Executor)
	assert.Equal(t, "gpt-5.6-sol", byName[stageVerify].Model)

	assert.Equal(t, map[string]int{stageFind: 140, stageSynthesis: 200, stageVerify: 25}, stageTokens(rep),
		"each stage carries what it spent, never a running total")
	assertStageTokensSum(t, rep)
}

func TestNew_stagger(t *testing.T) {
	t.Run("a configured delay holds the gate shut", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.StaggerDelay = time.Minute
		p := New(h.cfg)
		require.NotNil(t, p.stagger, "the pipeline owns the instance so a later stage can reuse it")
		assert.False(t, p.stagger.open(), "followers wait for the leader or the delay")
	})

	t.Run("no delay means no stagger", func(t *testing.T) {
		h := newHarness(t)
		p := New(h.cfg)
		assert.True(t, p.stagger.open())
	})

	t.Run("the parallel cap comes from config", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.MaxParallel = 3
		p := New(h.cfg)
		assert.Equal(t, 3, cap(p.stagger.sem))
	})
}

func TestPipeline_Run_degrade(t *testing.T) {
	t.Run("one dead source degrades and the run continues", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(
				`{"file":"a.go","line":10,"severity":"major","confidence":90,"title":"leak"}`), Tokens: 100},
		})

		p := New(h.cfg)
		events := drain(p)
		rep, err := p.Run(context.Background())
		require.NoError(t, err, "one flaky agent must not waste the others' work")

		assert.Equal(t, 2, rep.Sources.Expected)
		assert.Equal(t, 1, rep.Sources.Reported)
		assert.True(t, rep.Sources.Degraded())
		assert.Equal(t, []string{"impl"}, rep.Sources.DegradedSources)
		require.Len(t, rep.Findings, 1, "the surviving source still reports")

		kinds := map[EventKind]int{}
		for _, ev := range <-events {
			kinds[ev.Kind]++
		}
		assert.Equal(t, 1, kinds[EventAgentRetried], "the dead source was retried once")
		assert.Equal(t, 1, kinds[EventAgentDegraded])
	})

	t.Run("a degraded source is loud in the JSON and in the markdown banner", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"minor","confidence":50,"title":"x"}`)},
		})

		p := New(h.cfg)
		drain(p)
		rep, err := p.Run(context.Background())
		require.NoError(t, err)

		out := &bytes.Buffer{}
		require.NoError(t, rep.JSON(out))
		var wire struct {
			Sources struct {
				Degraded []string `json:"degraded"`
			} `json:"sources"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &wire))
		assert.Equal(t, []string{"impl"}, wire.Sources.Degraded)

		md := &bytes.Buffer{}
		require.NoError(t, rep.Markdown(md))
		assert.Contains(t, md.String(), "Degraded run")
		assert.Contains(t, md.String(), "impl")
	})

	t.Run("every source degraded is a tool error, never an empty report", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				return executor.Result{}, errors.New("stalled")
			}}
		}

		p := New(h.cfg)
		drain(p)
		rep, err := p.Run(context.Background())
		require.ErrorIs(t, err, errNoSources)
		assert.Empty(t, rep.Findings)
	})
}

func TestPipeline_Run_archiveFailures(t *testing.T) {
	t.Run("events writer cannot be opened", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.Archive = &mocks.ArchiverMock{WriterFunc: func(name string) (io.WriteCloser, error) {
			if name == task.EventsFile {
				return nil, errors.New("disk full")
			}
			return &syncBuffer{}, nil
		}}
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(``)}, "impl": {StructuredOutput: findingsJSON(``)},
		})

		p := New(h.cfg)
		drain(p)
		rep, err := p.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disk full")
		assert.Empty(t, rep.Findings, "no report may ship next to a broken archive")
	})

	t.Run("event write fails", func(t *testing.T) {
		h := newHarness(t)
		h.events.writeErr = errors.New("write failed")
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(``)}, "impl": {StructuredOutput: findingsJSON(``)},
		})

		p := New(h.cfg)
		drain(p)
		_, err := p.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "archive event")
	})

	t.Run("close failure is not swallowed by the defer", func(t *testing.T) {
		h := newHarness(t)
		h.events.closeErr = errors.New("close failed")
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(`{"file":"a.go","line":1,"severity":"minor","confidence":50,"title":"x"}`)},
			"impl": {StructuredOutput: findingsJSON(``)},
		})

		p := New(h.cfg)
		drain(p)
		rep, err := p.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close events.jsonl")
		assert.Empty(t, rep.Findings)
	})
}

func TestPipeline_emit(t *testing.T) {
	t.Run("a dropped event is still archived", func(t *testing.T) {
		h := newHarness(t)
		p := New(h.cfg)
		p.openEvents()

		const emitted = eventBuffer + 44
		for range emitted {
			p.emit(Event{Kind: EventAgentActivity, Agent: "bugs", Text: "tool: Read"})
		}
		p.closeEvents()

		assert.Len(t, p.events, eventBuffer, "the channel sheds under load")
		assert.Len(t, h.events.lines(), emitted, "a slow renderer must not cost the audit record")
	})

	t.Run("stamps the clock when the caller did not", func(t *testing.T) {
		h := newHarness(t)
		p := New(h.cfg)
		p.emit(Event{Kind: EventStage, Stage: stageFind})
		ev := <-p.events
		assert.False(t, ev.At.IsZero())
	})

	t.Run("keeps a caller-supplied timestamp", func(t *testing.T) {
		h := newHarness(t)
		at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		p := New(h.cfg)
		p.emit(Event{Kind: EventStage, Stage: stageFind, At: at})
		ev := <-p.events
		assert.Equal(t, at, ev.At)
	})

	t.Run("concurrent emit is race free", func(t *testing.T) {
		h := newHarness(t)
		p := New(h.cfg)
		p.openEvents()

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 20 {
					p.emit(Event{Kind: EventAgentActivity, Agent: "bugs", Text: "tool: Read"})
				}
			})
		}
		wg.Wait()
		p.closeEvents()
		assert.Len(t, h.events.lines(), 160)
	})
}

func TestPipeline_Events_singleReader(t *testing.T) {
	h := newHarness(t)
	h.cfg.NewRunner = h.runner(map[string]executor.Result{
		"bugs": {StructuredOutput: findingsJSON(``)}, "impl": {StructuredOutput: findingsJSON(``)},
	})

	p := New(h.cfg)
	events := drain(p)
	_, err := p.Run(context.Background())
	require.NoError(t, err)

	seen := <-events
	assert.NotEmpty(t, seen, "the reader gets the events and the channel is closed when Run returns")
	_, open := <-p.Events()
	assert.False(t, open, "Run closes the channel so the renderer goroutine terminates")
}

// harness builds a pipeline Config over a temporary prompt tree, a recording archive and a clock
// that advances on every read, so timings are deterministic without a wall-clock wait.
type harness struct {
	cfg      Config
	events   *syncBuffer
	archived map[string]*syncBuffer
	mu       sync.Mutex
}

func newHarness(t *testing.T) *harness { return newHarnessWith(t, nil) }

// newHarnessWith overlays extra prompt-tree files on the default one, for the cases that need a
// broken or customized override in the project layer.
func newHarnessWith(t *testing.T, extra map[string]string) *harness {
	t.Helper()
	set, profile := testTree(t, extra)
	roster, err := profile.Roster(nil, set.LensNames())
	require.NoError(t, err)

	h := &harness{events: &syncBuffer{}, archived: map[string]*syncBuffer{}}
	h.archived[task.EventsFile] = h.events

	h.cfg = Config{
		Archive: &mocks.ArchiverMock{WriterFunc: h.writer},
		Clock:   tickingClock(),
		Set:     set, Profile: profile, Roster: roster,
		// most tests here are about the find stage; the synthesis and verify ones turn their stage back on
		NoSynthesis: true, NoVerify: true,
		Vars:      prompt.Vars{"SCOPE": "/abs/scope.md", "GOAL": "none provided", "PROFILE": "none provided", "CONTEXT": "none provided", "WORKDIR": "/repo"},
		Task:      "pr-1",
		Run:       "round-1",
		ScopePath: "/abs/scope.md",
	}
	return h
}

func (h *harness) writer(name string) (io.WriteCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if b, ok := h.archived[name]; ok {
		return b, nil
	}
	b := &syncBuffer{}
	h.archived[name] = b
	return b, nil
}

// finder builds a find stage over the harness config with an already-open stagger, so a test that is
// not about release ordering never has to drive one.
func (h *harness) finder(emit func(Event)) *finder {
	return &finder{cfg: h.cfg, emit: emit, save: h.save, stagger: newStagger(0, h.cfg.MaxParallel, h.cfg.Clock)}
}

// verifier builds a verify stage over the harness config with an already-open stagger, for the same
// reason finder does: a test that is not about release ordering never has to drive one.
func (h *harness) verifier(emit func(Event)) *verifier {
	return &verifier{cfg: h.cfg, emit: emit, save: h.save, stagger: newStagger(0, h.cfg.MaxParallel, h.cfg.Clock)}
}

// save is the stages' whole-artifact writer, recording into the same map the streaming writers use so
// a test can read back the composed prompt a stage archived.
func (h *harness) save(name string, data []byte) {
	w, err := h.writer(name)
	if err != nil {
		return
	}
	_, _ = w.Write(data)
	_ = w.Close()
}

func (h *harness) get(name string) *syncBuffer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.archived[name]
}

// names lists every artifact the run opened a writer for, sorted so an assertion on the whole set
// does not depend on which agent finished first.
func (h *harness) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Sorted(maps.Keys(h.archived))
}

// runner returns a factory whose runner answers per agent, keyed off the composed prompt, since the
// spec a factory receives names the executor rather than the agent. A fixture under the synthesis
// stage's own name answers that stage, so one map drives a whole run.
func (h *harness) runner(byAgent map[string]executor.Result) func(RunnerSpec) Runner {
	return func(RunnerSpec) Runner {
		return &mocks.RunnerMock{
			RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				if res, ok := byAgent[stageSynthesis]; ok && strings.Contains(req.Prompt, synthesisMarker) {
					return res, nil
				}
				if res, ok := byAgent[stageVerify]; ok && strings.Contains(req.Prompt, verifyMarker) {
					return res, nil
				}
				for name, res := range byAgent {
					if strings.Contains(req.Prompt, "lens "+name) {
						return res, nil
					}
				}
				return executor.Result{}, errors.New("no fixture for prompt")
			},
		}
	}
}

// stageTokens maps each stage entry's name to its token count, so a test asserts every stage at once.
func stageTokens(rep finding.Report) map[string]int {
	out := make(map[string]int, len(rep.Stats.Stages))
	for _, st := range rep.Stats.Stages {
		out[st.Name] = st.Tokens
	}
	return out
}

// assertStageTokensSum checks the invariant the per-stage counts exist for: nothing spends tokens
// outside a stage, so the entries add up to the run total exactly.
func assertStageTokensSum(t *testing.T, rep finding.Report) {
	t.Helper()
	sum := 0
	for _, st := range rep.Stats.Stages {
		sum += st.Tokens
	}
	assert.Equal(t, rep.Stats.Tokens, sum, "stage tokens must add up to the run total")
}

// drain reads the whole event stream in the background and hands it back once the channel closes,
// so a test asserts on the sequence without racing the pipeline.
func drain(p *Pipeline) <-chan []Event {
	out := make(chan []Event, 1)
	go func() {
		var seen []Event
		for ev := range p.Events() {
			seen = append(seen, ev)
		}
		out <- seen
	}()
	return out
}

// testTree writes a minimal prompt tree and loads it as the project layer, so no test depends on
// the shipped defaults staying the shape it asserts.
func testTree(t *testing.T, extra map[string]string) (*prompt.Set, *prompt.Profile) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"lenses/bugs.md":               "---\ndescription: correctness\n---\nlens bugs: read {{SCOPE}} in {{WORKDIR}}",
		"lenses/impl.md":               "---\ndescription: implementation\n---\nlens impl: read {{SCOPE}}",
		"prompts/profiles/testprof.md": testProfile,
		"prompts/profiles/oneagent.md": oneAgentProfile,
	}
	maps.Copy(files, extra)
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	}

	set, err := prompt.Load(prompt.LoadOpts{ProjectDir: dir})
	require.NoError(t, err)
	profile, err := set.Profile("testprof")
	require.NoError(t, err)
	return set, profile
}

const testProfile = `---
description: two lens agents
model: claude/opus:high
agents:
  - {name: bugs, lenses: [bugs], color: cyan}
  - {name: impl, lenses: [impl], color: green}
---
review the change`

// codexStageProfile overrides testprof in place, so a harness built with it dispatches the same roster
// while both stages resolve to the runner the profile names rather than the stage file's own.
const codexStageProfile = `---
description: two lens agents, both stages on codex
model: codex/gpt-5.6-sol:high
agents:
  - {name: bugs, lenses: [bugs], color: cyan}
  - {name: impl, lenses: [impl], color: green}
---
review the change`

const oneAgentProfile = `---
description: a single agent
model: claude/opus
agents:
  - {name: solo, lenses: [bugs, impl]}
---
review the change`

// findingsJSON wraps zero or more finding objects in the finder schema's envelope.
func findingsJSON(items ...string) json.RawMessage {
	body := strings.Join(items, ",")
	return json.RawMessage(`{"findings":[` + body + `]}`)
}

// tickingClock advances one second per read, so a stage duration is non-zero without a sleep and
// concurrent readers stay race free.
func tickingClock() executor.Clock {
	base := time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)
	var ticks atomic.Int64
	return &emocks.ClockMock{
		NowFunc: func() time.Time { return base.Add(time.Duration(ticks.Add(1)) * time.Second) },
		AfterFuncFunc: func(time.Duration, func()) executor.Timer {
			return &emocks.TimerMock{StopFunc: func() bool { return true }, ResetFunc: func(time.Duration) bool { return true }}
		},
	}
}

// syncBuffer is one archived artifact. It is guarded because emit is reached from every agent
// goroutine, and it can be told to fail on write or on close.
type syncBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	writeErr error
	closeErr error
	closed   bool
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.writeErr != nil {
		return 0, b.writeErr
	}
	n, err := b.buf.Write(p)
	return n, err //nolint:wrapcheck // bytes.Buffer.Write never fails, and a test writer wrapping it says nothing
}

func (b *syncBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return b.closeErr
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) lines() []string {
	out := []string{}
	for l := range strings.SplitSeq(b.String(), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func (b *syncBuffer) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}
