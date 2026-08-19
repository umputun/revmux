package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline/mocks"
	"github.com/umputun/revmux/app/task"
)

// in the archive-path subtest both sides of the comparison are the constants' own runtime values,
// so it cannot see a literal that happens to spell one of them correctly — what it catches is an
// archived name under none of the seven roots, and a root nothing wrote to. The literal values are
// pinned in app/archive/archive_test.go
func TestPipeline_Run_artifacts(t *testing.T) {
	t.Run("every composed prompt is archived byte for byte", func(t *testing.T) {
		h, seen := artifactHarness(t)

		p := New(h.cfg)
		drain(p)
		rep, err := p.Run(context.Background())
		require.NoError(t, err)
		require.Len(t, rep.Findings, 4)

		archived := []string{
			"prompts/agents/bugs.md", "prompts/agents/impl.md",
			"prompts/stages/synthesis.md",
			"prompts/stages/verify-app-executor.md", "prompts/stages/verify-app-ui.md",
		}
		for _, name := range archived {
			t.Run(name, func(t *testing.T) {
				got := h.get(name)
				require.NotNil(t, got, "no prompt was archived under this name")
				assert.Contains(t, seen(), got.String(),
					"the archived prompt must be the bytes a process received, not a paraphrase of them")
			})
		}
	})

	t.Run("an agent prompt and a stage prompt of the same name coexist", func(t *testing.T) {
		h := newHarnessWith(t, map[string]string{
			"prompts/profiles/collide.md": collidingProfile,
		})
		profile, err := h.cfg.Set.Profile("collide")
		require.NoError(t, err)
		roster, err := profile.Roster(nil, h.cfg.Set.LensNames())
		require.NoError(t, err)

		h.cfg.Profile, h.cfg.Roster = profile, roster
		h.cfg.NoVerify = false
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(
				`{"file":"app/executor/proc.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`)},
			stageVerify: {StructuredOutput: json.RawMessage(`{"verdicts":[{"id":"verify-1","verdict":"confirmed"}]}`)},
		})

		p := New(h.cfg)
		drain(p)
		_, err = p.Run(context.Background())
		require.NoError(t, err)

		agent := h.get("prompts/agents/verify.md")
		require.NotNil(t, agent, "the roster agent named verify keeps its own file")
		assert.Contains(t, agent.String(), "lens bugs")

		stage := h.get("prompts/stages/verify-app-executor.md")
		require.NotNil(t, stage, "and the stage prompt is not overwritten by it")
		assert.Contains(t, stage.String(), verifyMarker)
	})

	t.Run("a codex prompt is archived with the contract its executor appends", func(t *testing.T) {
		h := newHarnessWith(t, map[string]string{"prompts/profiles/peer.md": codexPeerProfile})
		profile, err := h.cfg.Set.Profile("peer")
		require.NoError(t, err)
		roster, err := profile.Roster(nil, h.cfg.Set.LensNames())
		require.NoError(t, err)
		h.cfg.Profile, h.cfg.Roster = profile, roster

		var got executor.Request
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				got = req // the roster holds one agent, so p.Run joining it is the barrier on this write
				return executor.Result{StructuredOutput: findingsJSON()}, nil
			}}
		}

		p := New(h.cfg)
		drain(p)
		_, err = p.Run(context.Background())
		require.NoError(t, err)

		archived := h.get("prompts/agents/codex.md")
		require.NotNil(t, archived, "no prompt was archived for the codex entry")
		assert.Equal(t, got.Prompt+executor.CodexOutputContract(got.Schema), archived.String(),
			"codex appends its output contract after dispatch, so an archive without it describes a different run")
		assert.Contains(t, archived.String(), "Return ONLY a JSON object",
			"the one instruction asking codex for JSON at all cannot be the missing part")
	})

	t.Run("an agy prompt is archived bare, because its Run appends nothing", func(t *testing.T) {
		schema := finding.FinderSchema()
		assert.Equal(t, "composed", archivedPrompt(executorAgy, "composed", schema),
			"agy's Run sends the composed text as-is, so an archive carrying a contract describes bytes the model never saw")
		assert.Equal(t, "composed"+executor.ClaudeNarrationContract(schema), archivedPrompt("claude", "composed", schema))
		assert.Equal(t, "composed"+executor.CodexOutputContract(schema), archivedPrompt(executorCodex, "composed", schema))
	})

	t.Run("an agent named events keeps its stream out of the event log", func(t *testing.T) {
		h := newHarnessWith(t, map[string]string{
			"prompts/profiles/collide.md": collidingProfile,
		})
		profile, err := h.cfg.Set.Profile("collide")
		require.NoError(t, err)
		roster, err := profile.Roster(nil, h.cfg.Set.LensNames())
		require.NoError(t, err)

		h.cfg.Profile, h.cfg.Roster = profile, roster
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				_, _ = req.RawOutput.Write([]byte(`{"type":"system"}`))
				return executor.Result{StructuredOutput: findingsJSON()}, nil
			}}
		}

		p := New(h.cfg)
		drain(p)
		_, err = p.Run(context.Background())
		require.NoError(t, err)

		//nolint:testifylint // byte-exactness is the assertion; JSONEq would pass on reformatted bytes
		assert.Equal(t, `{"type":"system"}`, h.get("agents/events.jsonl").String())
		for _, line := range h.events.lines() {
			var ev Event
			require.NoError(t, json.Unmarshal([]byte(line), &ev), "the event log holds only events")
		}
	})

	t.Run("each stage snapshots its findings exactly once", func(t *testing.T) {
		h, _ := artifactHarness(t)

		p := New(h.cfg)
		drain(p)
		_, err := p.Run(context.Background())
		require.NoError(t, err)

		counts := map[string]int{task.FoundFile: 4, task.SynthesizedFile: 4, task.VerifiedFile: 4}
		for name, want := range counts {
			t.Run(name, func(t *testing.T) {
				got := h.get(name)
				require.NotNil(t, got)

				var rep finding.Report
				require.NoError(t, json.Unmarshal([]byte(got.String()), &rep),
					"a stage written twice leaves two documents in one file")
				assert.Len(t, rep.Findings, want)
			})
		}

		assert.Empty(t, verdictIn(t, h.get(task.SynthesizedFile)),
			"the pre-verify snapshot is what shows whether verify dropped something real")
		assert.Equal(t, finding.Confirmed, verdictIn(t, h.get(task.VerifiedFile)))
	})

	t.Run("a skipped stage snapshots nothing", func(t *testing.T) {
		h := newHarness(t) // defaults to --no-synthesis and --no-verify
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON()}, "impl": {StructuredOutput: findingsJSON()},
		})

		p := New(h.cfg)
		drain(p)
		_, err := p.Run(context.Background())
		require.NoError(t, err)

		assert.NotNil(t, h.get(task.FoundFile))
		assert.Nil(t, h.get(task.SynthesizedFile), "a stage that did not run has no findings to snapshot")
		assert.Nil(t, h.get(task.VerifiedFile))
		assert.Nil(t, h.get("prompts/stages/synthesis.md"))
	})

	t.Run("the event log holds the decisions no agent stream contains", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
				if strings.Contains(req.Prompt, "lens impl") {
					return executor.Result{IdleTimedOut: true}, nil
				}
				return executor.Result{StructuredOutput: findingsJSON()}, nil
			}}
		}

		p := New(h.cfg)
		drain(p)
		_, err := p.Run(context.Background())
		require.NoError(t, err)

		kinds := map[EventKind][]string{}
		for _, line := range h.events.lines() {
			var ev Event
			require.NoError(t, json.Unmarshal([]byte(line), &ev))
			kinds[ev.Kind] = append(kinds[ev.Kind], ev.Agent)
			assert.False(t, ev.At.IsZero(), "an undated decision cannot be placed in the run")
		}

		assert.Equal(t, []string{"impl"}, kinds[EventAgentRetried], "the stall and its retry")
		assert.Equal(t, []string{"impl"}, kinds[EventAgentDegraded])
		assert.NotEmpty(t, kinds[EventStage], "stage transitions are decisions too")
	})

	t.Run("every archive path lands under an app/task constant", func(t *testing.T) {
		h, _ := artifactHarness(t)

		p := New(h.cfg)
		drain(p)
		_, err := p.Run(context.Background())
		require.NoError(t, err)

		files := []string{task.EventsFile, task.FoundFile, task.SynthesizedFile, task.VerifiedFile}
		dirs := []string{task.AgentsDir, task.AgentPromptDir, task.StagePromptDir}

		loose := []string{}
		for _, name := range h.names() {
			under := slices.ContainsFunc(dirs, func(d string) bool { return strings.HasPrefix(name, d+"/") })
			if under || slices.Contains(files, name) {
				continue
			}
			loose = append(loose, name)
		}
		assert.Empty(t, loose, "an archived name under no app/task root is one this package named itself")

		for _, name := range files {
			assert.NotNil(t, h.get(name), "%s is an app/task constant nothing wrote to", name)
		}
		for _, dir := range dirs {
			assert.True(t, slices.ContainsFunc(h.names(), func(n string) bool { return strings.HasPrefix(n, dir+"/") }),
				"%s is an app/task constant nothing wrote under", dir)
		}
	})

	t.Run("a prompt that cannot be archived fails the whole run", func(t *testing.T) {
		h := newHarness(t)
		h.archived["prompts/agents/bugs.md"] = &syncBuffer{writeErr: errors.New("disk full")}
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON()}, "impl": {StructuredOutput: findingsJSON()},
		})

		p := New(h.cfg)
		drain(p)
		rep, err := p.Run(context.Background())
		require.Error(t, err, "an unauditable run is a tool error, not a warning")
		assert.Contains(t, err.Error(), "prompts/agents/bugs.md")
		assert.Empty(t, rep.Findings, "no report may ship next to a half-written archive")
	})

	t.Run("a stage snapshot that cannot be archived fails the whole run", func(t *testing.T) {
		h := newHarness(t)
		h.archived[task.FoundFile] = &syncBuffer{closeErr: errors.New("disk full")}
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON()}, "impl": {StructuredOutput: findingsJSON()},
		})

		p := New(h.cfg)
		drain(p)
		_, err := p.Run(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), task.FoundFile)
	})
}

// artifactHarness runs both optional stages over two findings per directory, so verify fans out to one
// group per directory rather than merging them, and returns what prompts the runner actually received.
func artifactHarness(t *testing.T) (*harness, func() []string) {
	t.Helper()
	h := newHarness(t)
	h.cfg.NoSynthesis, h.cfg.NoVerify = false, false
	h.cfg.NewRunner = h.runner(map[string]executor.Result{
		"bugs": {StructuredOutput: findingsJSON(
			`{"file":"app/executor/proc.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`,
			`{"file":"app/executor/codex.go","line":20,"severity":"minor","confidence":70,"title":"stderr","lenses":["bugs"]}`)},
		"impl": {StructuredOutput: findingsJSON(
			`{"file":"app/ui/model.go","line":4,"severity":"minor","confidence":60,"title":"naming","lenses":["impl"]}`,
			`{"file":"app/ui/view.go","line":8,"severity":"minor","confidence":65,"title":"width","lenses":["impl"]}`)},
		stageSynthesis: {StructuredOutput: synthesizedAll()},
		stageVerify:    {StructuredOutput: confirmedAll()},
	})

	var mu sync.Mutex
	var prompts []string
	inner := h.cfg.NewRunner
	h.cfg.NewRunner = func(spec RunnerSpec) Runner {
		r := inner(spec)
		return &mocks.RunnerMock{
			RunFunc: func(ctx context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error) {
				// the real executors append to the prompt inside Run — codex its output contract,
				// claude its narration contract, agy nothing at all — and this mock stands in for
				// one of them. Recording the prompt as handed over would make "the archived prompt
				// is what a process received" pass against bytes no process would ever receive.
				prompt := req.Prompt + executor.ClaudeNarrationContract(req.Schema)
				switch spec.Executor {
				case executorCodex:
					prompt = req.Prompt + executor.CodexOutputContract(req.Schema)
				case executorAgy:
					prompt = req.Prompt
				}
				mu.Lock()
				prompts = append(prompts, prompt)
				mu.Unlock()
				return r.Run(ctx, req, sink)
			},
		}
	}

	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(prompts)
	}
}

// synthesizedAll passes all four findings through the merge, each keeping its own attribution.
func synthesizedAll() json.RawMessage {
	items := []string{
		`{"merged_ids":["bugs-1"],"file":"app/executor/proc.go","line":10,"severity":"major","confidence":80,"title":"leak"}`,
		`{"merged_ids":["bugs-2"],"file":"app/executor/codex.go","line":20,"severity":"minor","confidence":70,"title":"stderr"}`,
		`{"merged_ids":["impl-1"],"file":"app/ui/model.go","line":4,"severity":"minor","confidence":60,"title":"naming"}`,
		`{"merged_ids":["impl-2"],"file":"app/ui/view.go","line":8,"severity":"minor","confidence":65,"title":"width"}`,
	}
	return json.RawMessage(`{"findings":[` + strings.Join(items, ",") + `],"open_questions":[],"pre_existing":[]}`)
}

func confirmedAll() json.RawMessage {
	ids := []string{"bugs-1", "bugs-2", "impl-1", "impl-2"}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, `{"id":"`+id+`","verdict":"confirmed"}`)
	}
	return json.RawMessage(`{"verdicts":[` + strings.Join(out, ",") + `]}`)
}

// verdictIn reads the verdict off the first finding of an archived stage snapshot.
func verdictIn(t *testing.T, b *syncBuffer) finding.Verdict {
	t.Helper()
	require.NotNil(t, b)
	var rep finding.Report
	require.NoError(t, json.Unmarshal([]byte(b.String()), &rep))
	require.NotEmpty(t, rep.Findings)
	return rep.Findings[0].Verdict
}

// codexPeerProfile is one codex entry alone, so the archived prompt a test reads back belongs to the
// executor that appends its own output contract to it.
const codexPeerProfile = `---
description: one codex peer
agents:
  - {name: codex, lenses: [bugs], model: codex/gpt-5.6-sol}
---
review the change`

// collidingProfile names its agents after the two fixed stage prompts and the event log, which is why
// per-agent artifacts live in their own directories.
const collidingProfile = `---
description: agents named after fixed artifacts
model: claude/opus
agents:
  - {name: verify, lenses: [bugs]}
  - {name: events, lenses: [impl]}
---
review the change`
