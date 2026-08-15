package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
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

func TestVerifier_groupByDir(t *testing.T) {
	t.Run("one directory is one group", func(t *testing.T) {
		v := &verifier{}
		groups := v.groupByDir(dirFindings("app/executor", 3))
		require.Len(t, groups, 1)
		assert.Equal(t, []string{"app/executor"}, groups[0].dirs)
		assert.Len(t, groups[0].findings, 3)
	})

	t.Run("each directory holding enough findings gets its own verifier", func(t *testing.T) {
		v := &verifier{}
		groups := v.groupByDir(slices.Concat(
			dirFindings("app/ui", 2), dirFindings("app/executor", 4), dirFindings("app/prompt", 2)))
		require.Len(t, groups, 3)
		assert.Equal(t, [][]string{{"app/executor"}, {"app/prompt"}, {"app/ui"}},
			[][]string{groups[0].dirs, groups[1].dirs, groups[2].dirs}, "directory order is stable across runs")
	})

	t.Run("thin directories are merged rather than each getting a process", func(t *testing.T) {
		v := &verifier{}
		groups := v.groupByDir(slices.Concat(
			dirFindings("app/executor", 3), dirFindings("app/ui", 1), dirFindings("docs", 1), dirFindings(".", 1)))
		require.Len(t, groups, 2)
		assert.Equal(t, []string{"app/executor"}, groups[0].dirs)
		assert.Equal(t, []string{".", "app/ui", "docs"}, groups[1].dirs, "the thin ones share one verifier")
		assert.Len(t, groups[1].findings, 3)
	})

	t.Run("the group count is capped and nothing is lost to the merge", func(t *testing.T) {
		v := &verifier{cfg: Config{VerifyGroups: 2}}
		groups := v.groupByDir(slices.Concat(
			dirFindings("a", 2), dirFindings("b", 3), dirFindings("c", 4), dirFindings("d", 5)))
		require.Len(t, groups, 2)

		total, seen := 0, []string{}
		for _, g := range groups {
			total += len(g.findings)
			seen = append(seen, g.dirs...)
		}
		assert.Equal(t, 14, total, "capping merges groups, it never drops findings")
		slices.Sort(seen)
		assert.Equal(t, []string{"a", "b", "c", "d"}, seen)
		assert.Equal(t, []string{"a", "b", "c"}, groups[0].dirs, "the smallest directories merge first")
	})

	t.Run("a cap larger than the directory count changes nothing", func(t *testing.T) {
		v := &verifier{cfg: Config{VerifyGroups: 6}}
		groups := v.groupByDir(slices.Concat(dirFindings("a", 2), dirFindings("b", 2)))
		require.Len(t, groups, 2)
	})

	t.Run("no findings, no verifiers", func(t *testing.T) {
		v := &verifier{}
		assert.Empty(t, v.groupByDir(nil))
	})

	t.Run("a file-level finding lands in the root group", func(t *testing.T) {
		v := &verifier{}
		groups := v.groupByDir([]finding.Finding{
			{ID: "a-1", File: "", Sources: []string{"bugs"}, Title: "no file"},
			{ID: "a-2", File: "main.go", Sources: []string{"impl"}, Title: "root file"},
		})
		require.Len(t, groups, 1, "the source a production finding always carries changes nothing in dir mode")
		assert.Equal(t, []string{"."}, groups[0].dirs)
	})
}

func TestVerifier_groupBySource(t *testing.T) {
	src := Config{VerifyGroupBy: groupBySource}

	t.Run("a panel of one argument each gets a verifier each", func(t *testing.T) {
		v := &verifier{cfg: src}
		groups := v.groupByDir(slices.Concat(agentFindings("facts", 1), agentFindings("thesis", 1),
			agentFindings("antithesis", 1), agentFindings("cost", 1)))
		require.Len(t, groups, 4, "the thin merge would put a case and its rebuttal in front of one verifier")
		assert.Equal(t, [][]string{{"antithesis"}, {"cost"}, {"facts"}, {"thesis"}},
			[][]string{groups[0].dirs, groups[1].dirs, groups[2].dirs, groups[3].dirs}, "agent order is stable across runs")
	})

	t.Run("a located argument groups by its agent rather than its directory", func(t *testing.T) {
		v := &verifier{cfg: src}
		located := finding.Finding{ID: "facts-2", File: "app/prompt/load.go", Sources: []string{"facts"}}
		groups := v.groupByDir(slices.Concat(agentFindings("facts", 1), []finding.Finding{located}, agentFindings("cost", 1)))
		require.Len(t, groups, 2)
		assert.Equal(t, []string{"cost"}, groups[0].dirs)
		assert.Equal(t, []string{"facts"}, groups[1].dirs)
		assert.Len(t, groups[1].findings, 2, "the grounding lens citing code stays with the rest of its own case")
	})

	t.Run("the group count is still capped", func(t *testing.T) {
		v := &verifier{cfg: Config{VerifyGroupBy: groupBySource, VerifyGroups: 2}}
		groups := v.groupByDir(slices.Concat(agentFindings("facts", 1), agentFindings("thesis", 2),
			agentFindings("antithesis", 3), agentFindings("cost", 4)))
		require.Len(t, groups, 2)

		total := 0
		for _, g := range groups {
			total += len(g.findings)
		}
		assert.Equal(t, 10, total, "capping merges groups, it never drops arguments")
	})

	t.Run("an unattributed finding lands in the root bucket", func(t *testing.T) {
		v := &verifier{cfg: src}
		groups := v.groupByDir([]finding.Finding{{ID: "x-1", File: "app/ui/model.go"}})
		require.Len(t, groups, 1)
		assert.Equal(t, []string{"."}, groups[0].dirs)
	})

	t.Run("a synthesized finding lands in its first source's bucket", func(t *testing.T) {
		v := &verifier{cfg: src}
		merged := finding.Finding{ID: "thesis-1", Sources: []string{"thesis", "antithesis"}}
		groups := v.groupByDir(slices.Concat([]finding.Finding{merged}, agentFindings("antithesis", 1)))
		require.Len(t, groups, 2)
		assert.Equal(t, []string{"antithesis"}, groups[0].dirs)
		assert.Len(t, groups[0].findings, 1, "a merged finding is judged once, not once per source that raised it")
		assert.Equal(t, []string{"thesis"}, groups[1].dirs)
		assert.Equal(t, []string{"thesis-1"}, []string{groups[1].findings[0].ID})
	})
}

func TestVerifier_promptName(t *testing.T) {
	v := &verifier{}
	tests := []struct {
		name string
		dirs []string
		want string
	}{
		{name: "one directory", dirs: []string{"app/executor"}, want: "prompts/stages/verify-app-executor.md"},
		{name: "merged", dirs: []string{"app/ui", "app/pipeline"}, want: "prompts/stages/verify-app-ui+app-pipeline.md"},
		{name: "repository root", dirs: []string{"."}, want: "prompts/stages/verify-root.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := []verifyGroup{{dirs: tt.dirs}}
			v.name(groups) // compose resolves the name before any filename is built from it
			got := v.promptName(groups[0])
			assert.Equal(t, tt.want, got)
			assert.Equal(t, 2, strings.Count(got, "/"), "the label is a filename, never another directory level")
		})
	}
}

func TestVerifier_name(t *testing.T) {
	v := &verifier{}

	t.Run("distinct labels are left alone", func(t *testing.T) {
		groups := []verifyGroup{{dirs: []string{"app/ui"}}, {dirs: []string{"app/pipeline"}}}
		v.name(groups)
		assert.Equal(t, "app-ui", groups[0].name)
		assert.Equal(t, "app-pipeline", groups[1].name)
	})

	// grouping buckets on the exact path, so these are two groups that slug to one label. Sharing it
	// would make one archived prompt overwrite the other and give both the same event agent name
	t.Run("colliding labels are suffixed rather than shared", func(t *testing.T) {
		groups := []verifyGroup{{dirs: []string{"app/executor"}}, {dirs: []string{"app-executor"}}, {dirs: []string{"app executor"}}}
		v.name(groups)

		assert.Equal(t, "app-executor", groups[0].name)
		assert.Equal(t, "app-executor-2", groups[1].name)
		assert.Equal(t, "app-executor-3", groups[2].name)

		seen := map[string]bool{}
		for _, g := range groups {
			p := v.promptName(g)
			assert.False(t, seen[p], "every group needs its own archive filename, got %s twice", p)
			seen[p] = true
		}
		assert.Len(t, seen, len(groups))
	})

	// a real directory can already be named what the suffix generator would produce, so the generated
	// candidate has to be checked against what is taken, not just the base label
	t.Run("a directory named like the generated suffix does not collide with it", func(t *testing.T) {
		groups := []verifyGroup{
			{dirs: []string{"app/foo"}}, {dirs: []string{"app-foo"}}, {dirs: []string{"app/foo-2"}},
		}
		v.name(groups)

		seen := map[string]bool{}
		for _, g := range groups {
			p := v.promptName(g)
			assert.False(t, seen[p], "every group needs its own archive filename, got %s twice", p)
			seen[p] = true
		}
		assert.Len(t, seen, len(groups))
	})
}

func TestVerifyGroup_label(t *testing.T) {
	tests := []struct {
		name string
		dirs []string
		want string
	}{
		{"nested directory", []string{"app/executor"}, "app-executor"},
		{"deeply nested", []string{"app/ui/panes/inner"}, "app-ui-panes-inner"},
		{"repository root", []string{"."}, "root"},
		{"no directory at all", nil, "root"},
		{"merged pair", []string{"app/executor", "app/ui"}, "app-executor+app-ui"},
		{"dot directory", []string{".github/workflows"}, "github-workflows"},
		{"escape attempt", []string{"../etc"}, "etc"},
		{"spaces and separators", []string{"some dir/sub"}, "some-dir-sub"},
		{"more directories than the label spells out", []string{"a", "b", "c", "d", "e"}, "a+b+c+2-more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verifyGroup{dirs: tt.dirs}.label()
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "/", "promptName builds a filename from this")
			assert.NotContains(t, got, "\\")
			assert.False(t, strings.HasPrefix(got, "."), "a leading dot is rejected wherever a name becomes a path")
		})
	}
}

func TestVerifier_run_groupIsolation(t *testing.T) {
	h := newHarness(t)
	rec := &promptRecorder{}
	h.cfg.NewRunner = rec.factory(executor.Result{StructuredOutput: json.RawMessage(`{"verdicts":[]}`)}, nil)

	v := h.verifier(func(Event) {})
	_, err := v.run(context.Background(), judgedReport())
	require.NoError(t, err)

	prompts := rec.seen()
	require.Len(t, prompts, 2, "one verifier per directory")
	slices.Sort(prompts)

	assert.Contains(t, prompts[0], `"id": "bugs-1"`)
	assert.Contains(t, prompts[0], `"id": "bugs-2"`)
	assert.NotContains(t, prompts[0], "impl-1", "a verifier holding the neighbours' findings anchors on them")
	assert.NotContains(t, prompts[0], "impl-2")

	assert.Contains(t, prompts[1], `"id": "impl-1"`)
	assert.NotContains(t, prompts[1], "bugs-1")
	for _, p := range prompts {
		assert.NotContains(t, p, "bugs-3", "an open question is never verified")
	}
}

// the codex path has no --json-schema, so a refined verdict can name a severity outside the enum:
// letting it through replaces a schema-checked value with one nothing validated
func TestVerifier_run_verdicts(t *testing.T) {
	t.Run("every verdict routes its finding", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{
			"bugs-1": `{"id":"bugs-1","verdict":"confirmed"}`,
			"bugs-2": `{"id":"bugs-2","verdict":"rejected"}`,
			"impl-1": `{"id":"impl-1","verdict":"immaterial"}`,
			"impl-2": `{"id":"impl-2","verdict":"pre_existing"}`,
		})

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		require.Len(t, rep.Findings, 1, "rejected is dropped, the two reclassified move out")
		assert.Equal(t, "bugs-1", rep.Findings[0].ID)
		assert.Equal(t, finding.Confirmed, rep.Findings[0].Verdict)

		require.Len(t, rep.Immaterial, 1)
		assert.Equal(t, "impl-1", rep.Immaterial[0].ID)
		require.Len(t, rep.PreExisting, 1)
		assert.Equal(t, "impl-2", rep.PreExisting[0].ID)
		require.Len(t, rep.OpenQuestions, 1, "open questions pass through untouched")
	})

	t.Run("a refined verdict rewrites only what it returned", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{
			"bugs-1": `{"id":"bugs-1","verdict":"refined","line":14,"end_line":20,"severity":"critical",` +
				`"confidence":95,"title":"leak on the error path","fix":"close it"}`,
		})

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		got, ok := byID(rep.Findings, "bugs-1")
		require.True(t, ok)
		assert.Equal(t, finding.Refined, got.Verdict)
		assert.Equal(t, 14, got.Line)
		assert.Equal(t, 20, got.EndLine)
		assert.Equal(t, finding.Critical, got.Severity)
		assert.Equal(t, 95, got.Confidence)
		assert.Equal(t, "leak on the error path", got.Title)
		assert.Equal(t, "close it", got.Fix)
		assert.Equal(t, "app/executor/proc.go", got.File, "an omitted field keeps its original value")
		assert.Equal(t, []string{"bugs"}, got.Sources, "verification does not touch attribution")
	})

	t.Run("a refined verdict with an unknown severity keeps the original", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{
			"bugs-1": `{"id":"bugs-1","verdict":"refined","severity":"blocker","title":"still refined"}`,
		})

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		got, ok := byID(rep.Findings, "bugs-1")
		require.True(t, ok)
		assert.Equal(t, finding.Major, got.Severity, "an unrecognized severity is not a judgment")
		assert.Equal(t, finding.Refined, got.Verdict, "the verdict itself is still from the enum")
		assert.Equal(t, "still refined", got.Title, "the rest of the correction still applies")
	})

	t.Run("a corrected value outside a refined verdict is ignored", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{
			"bugs-1": `{"id":"bugs-1","verdict":"confirmed","severity":"minor","confidence":10,"title":"rewritten"}`,
		})

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		got, ok := byID(rep.Findings, "bugs-1")
		require.True(t, ok)
		assert.Equal(t, finding.Major, got.Severity)
		assert.Equal(t, 80, got.Confidence)
		assert.Equal(t, "leak", got.Title)
	})

	t.Run("a finding the verifier said nothing about survives as unverified", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{"bugs-1": `{"id":"bugs-1","verdict":"confirmed"}`})

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		require.Len(t, rep.Findings, 4, "silence is not a rejection")
		got, ok := byID(rep.Findings, "bugs-2")
		require.True(t, ok)
		assert.Equal(t, finding.Unverified, got.Verdict)
	})

	t.Run("a verdict outside the enum is not a judgment", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{"bugs-1": `{"id":"bugs-1","verdict":"probably fine"}`})

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		got, ok := byID(rep.Findings, "bugs-1")
		require.True(t, ok, "an unrecognized word neither keeps nor drops the finding on its own")
		assert.Equal(t, finding.Unverified, got.Verdict, "the codex path has no schema to enforce the enum")
	})

	t.Run("a verdict for a finding the group never held is ignored", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = fixedRunner(executor.Result{StructuredOutput: json.RawMessage(
			`{"verdicts":[{"id":"ghost-9","verdict":"confirmed"}]}`)}, nil)

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		require.Len(t, rep.Findings, 4)
		for _, f := range rep.Findings {
			assert.Equal(t, finding.Unverified, f.Verdict)
		}
	})

	t.Run("tokens from every verifier count toward the run", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{"bugs-1": `{"id":"bugs-1","verdict":"confirmed"}`})

		v := h.verifier(func(Event) {})
		rep := judgedReport()
		rep.Stats.Tokens = 500
		out, err := v.run(context.Background(), rep)
		require.NoError(t, err)
		assert.Equal(t, 520, out.Stats.Tokens, "both groups reported 10")
	})

	t.Run("the runner spec and the schema come from the stage", func(t *testing.T) {
		h := newHarness(t)
		var spec RunnerSpec
		var req executor.Request
		var mu sync.Mutex
		h.cfg.NewRunner = func(rs RunnerSpec) Runner {
			mu.Lock()
			spec = rs
			mu.Unlock()
			return &mocks.RunnerMock{RunFunc: func(_ context.Context, r executor.Request, _ executor.EventSink) (executor.Result, error) {
				mu.Lock()
				req = r
				mu.Unlock()
				return executor.Result{StructuredOutput: json.RawMessage(`{"verdicts":[]}`)}, nil
			}}
		}

		v := h.verifier(func(Event) {})
		_, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		assert.Equal(t, RunnerSpec{Executor: "claude", Model: "opus", Effort: "high"}, spec)
		assert.JSONEq(t, string(finding.VerifySchema()), string(req.Schema))
	})

	t.Run("a profile naming the stage overrides that front matter", func(t *testing.T) {
		h := newHarnessWith(t, map[string]string{"prompts/profiles/testprof.md": codexStageProfile})
		var spec RunnerSpec
		var mu sync.Mutex
		h.cfg.NewRunner = func(rs RunnerSpec) Runner {
			mu.Lock()
			spec = rs
			mu.Unlock()
			return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
				return executor.Result{StructuredOutput: json.RawMessage(`{"verdicts":[]}`)}, nil
			}}
		}

		v := h.verifier(func(Event) {})
		_, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)
		assert.Equal(t, RunnerSpec{Executor: "codex", Model: "gpt-5.6-sol", Effort: "high"}, spec,
			"the shipped verify.md declares claude and opus, and the profile replaces both")
	})

	t.Run("nothing to verify dispatches nothing", func(t *testing.T) {
		h := newHarness(t)
		calls := 0
		h.cfg.NewRunner = func(RunnerSpec) Runner {
			calls++
			return &mocks.RunnerMock{}
		}

		v := h.verifier(func(Event) {})
		rep, err := v.run(context.Background(), finding.Report{
			OpenQuestions: []finding.Finding{{ID: "bugs-3", File: "a.go", Title: "why"}}})
		require.NoError(t, err)
		assert.Zero(t, calls)
		assert.Len(t, rep.OpenQuestions, 1)
	})
}

func TestVerifier_run_failures(t *testing.T) {
	tests := []struct {
		name string
		res  executor.Result
		err  error
	}{
		{"the verifier died", executor.Result{Tokens: 7}, errors.New("stalled twice")},
		{"no structured output came back", executor.Result{Tokens: 7}, nil},
		{"the verdicts are malformed", executor.Result{StructuredOutput: json.RawMessage(`{"verdicts":`), Tokens: 7}, nil},
		// an object of another shape decodes to zero verdicts, which would look like a group that judged
		// nothing rather than one that answered something else entirely
		{"the verdicts key is absent", executor.Result{StructuredOutput: json.RawMessage(`{"ok":true}`), Tokens: 7}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.NewRunner = fixedRunner(tt.res, tt.err)

			var seen []Event
			var mu sync.Mutex
			v := h.verifier(func(ev Event) { mu.Lock(); seen = append(seen, ev); mu.Unlock() })
			rep, err := v.run(context.Background(), judgedReport())
			require.NoError(t, err, "a dead verifier must not discard a review find and synthesis already paid for")

			require.Len(t, rep.Findings, 4)
			for _, f := range rep.Findings {
				assert.Equal(t, finding.Unverified, f.Verdict, "unverified says what happened, an empty verdict does not")
			}

			degraded := 0
			for _, ev := range seen {
				if ev.Kind == EventAgentDegraded {
					degraded++
					assert.True(t, strings.HasPrefix(ev.Agent, stageVerify+" "), "the failing group is named")
				}
			}
			assert.Equal(t, 2, degraded, "a failed verifier is loud rather than looking like a group that confirmed everything")
			assert.Equal(t, 14, rep.Stats.Tokens, "a group that failed still spent what it spent, and the run total is what was billed")
		})
	}
}

func TestVerifier_run_composeFailure(t *testing.T) {
	// a stage prompt naming a variable this stage does not supply is a bug in the prompt tree, and
	// every group would hit it identically — so it fails the run rather than degrading each group
	h := newHarnessWith(t, map[string]string{
		"prompts/verify.md": "---\ndescription: broken override\n---\nverify {{FINDINGS}} against {{SOURCES}}",
	})
	calls := 0
	h.cfg.NewRunner = func(RunnerSpec) Runner {
		calls++
		return &mocks.RunnerMock{}
	}

	v := h.verifier(func(Event) {})
	_, err := v.run(context.Background(), judgedReport())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved variable {{SOURCES}}")
	assert.Zero(t, calls, "nothing is dispatched when the prompt tree is broken")
}

func TestVerifier_stagger(t *testing.T) {
	t.Run("a gate the find stage already opened costs nothing", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{"bugs-1": `{"id":"bugs-1","verdict":"confirmed"}`})

		clk, timers := countingClock()
		s := newStagger(time.Minute, 0, clk)
		s.leaderStarted() // the find stage's leader produced activity

		v := &verifier{cfg: h.cfg, save: h.save, emit: func(Event) {}, stagger: s}
		rep, err := v.run(context.Background(), judgedReport())
		require.NoError(t, err)

		got, ok := byID(rep.Findings, "bugs-1")
		require.True(t, ok)
		assert.Equal(t, finding.Confirmed, got.Verdict, "the groups dispatched without a second release")
		assert.Equal(t, 1, timers(), "the delay was armed once, by the stage that built the stagger")
	})

	t.Run("a stagger of its own would hold every group until the context died", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NewRunner = verdictRunner(map[string]string{"bugs-1": `{"id":"bugs-1","verdict":"confirmed"}`})

		clk, _ := countingClock()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// what a per-stage instance would produce: a gate nothing has opened
		v := &verifier{cfg: h.cfg, save: h.save, emit: func(Event) {}, stagger: newStagger(time.Minute, 0, clk)}
		rep, err := v.run(ctx, judgedReport())
		require.NoError(t, err)
		for _, f := range rep.Findings {
			assert.Equal(t, finding.Unverified, f.Verdict, "nothing was verified, which is the regression")
		}
	})
}

func TestPipeline_Run_verify(t *testing.T) {
	t.Run("the verify stage is announced and records its own timing", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NoSynthesis, h.cfg.NoVerify = true, false
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(
				`{"file":"app/executor/proc.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`)},
			"impl": {StructuredOutput: findingsJSON(
				`{"file":"app/ui/model.go","line":4,"severity":"minor","confidence":60,"title":"naming","lenses":["impl"]}`)},
			stageVerify: {StructuredOutput: json.RawMessage(
				`{"verdicts":[{"id":"bugs-1","verdict":"confirmed"},{"id":"impl-1","verdict":"rejected"}]}`), Tokens: 25},
		})

		p := New(h.cfg)
		events := drain(p)
		rep, err := p.Run(context.Background())
		require.NoError(t, err)

		require.Len(t, rep.Findings, 1, "the rejected finding is dropped")
		assert.Equal(t, "bugs-1", rep.Findings[0].ID)
		assert.Equal(t, finding.Confirmed, rep.Findings[0].Verdict)

		require.Len(t, rep.Stats.Stages, 2)
		assert.Equal(t, stageVerify, rep.Stats.Stages[1].Name)
		assert.Positive(t, rep.Stats.Stages[1].DurationMS)

		var stages []string
		for _, ev := range <-events {
			if ev.Kind == EventStage {
				stages = append(stages, ev.Stage)
			}
		}
		assert.Equal(t, []string{stageFind, stageVerify}, stages)
	})

	t.Run("--no-verify marks every finding unverified", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NoVerify = true
		h.cfg.NewRunner = h.runner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(
				`{"file":"a.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`)},
			"impl": {StructuredOutput: findingsJSON(
				`{"file":"b.go","line":4,"severity":"minor","confidence":60,"title":"naming","lenses":["impl"]}`)},
		})

		p := New(h.cfg)
		events := drain(p)
		rep, err := p.Run(context.Background())
		require.NoError(t, err)

		require.Len(t, rep.Findings, 2, "skipping the stage drops nothing")
		for _, f := range rep.Findings {
			assert.Equal(t, finding.Unverified, f.Verdict, "a skipped stage must not read as a clean bill of health")
		}
		require.Len(t, rep.Stats.Stages, 1, "the skipped stage records no timing")
		for _, ev := range <-events {
			assert.NotEqual(t, stageVerify, ev.Stage, "a skipped stage is not announced")
		}
	})

	t.Run("verify runs through the find stage's stagger", func(t *testing.T) {
		h := newHarness(t)
		h.cfg.NoVerify, h.cfg.StaggerDelay = false, time.Minute
		clk, timers := countingClock()
		h.cfg.Clock = clk
		h.cfg.NewRunner = activeRunner(map[string]executor.Result{
			"bugs": {StructuredOutput: findingsJSON(
				`{"file":"app/executor/proc.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`)},
			"impl": {StructuredOutput: findingsJSON(
				`{"file":"app/ui/model.go","line":4,"severity":"minor","confidence":60,"title":"naming","lenses":["impl"]}`)},
			stageVerify: {StructuredOutput: json.RawMessage(`{"verdicts":[{"id":"bugs-1","verdict":"confirmed"}]}`)},
		})

		p := New(h.cfg)
		drain(p)
		// the context bounds the regression a per-stage stagger would cause: its gate would never open
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rep, err := p.Run(ctx)
		require.NoError(t, err)

		got, ok := byID(rep.Findings, "bugs-1")
		require.True(t, ok)
		assert.Equal(t, finding.Confirmed, got.Verdict, "the verify groups dispatched on find's release")
		assert.Equal(t, 1, timers(), "one stagger for the whole run, so verify pays no second delay")
	})
}

func TestPipeline_Run_verify_leaderNeverSignalled(t *testing.T) {
	// a single-agent roster never waits on the gate during find, so nothing opens it: the find stage
	// completing has to release the later stages, or verify blocks until the delay it should not pay
	h := newHarness(t)
	h.cfg.NoVerify, h.cfg.StaggerDelay = false, time.Minute
	clk, _ := countingClock() // the armed delay never fires
	h.cfg.Clock = clk

	profile, err := h.cfg.Set.Profile("oneagent")
	require.NoError(t, err)
	roster, err := profile.Roster(nil, h.cfg.Set.LensNames())
	require.NoError(t, err)
	h.cfg.Profile, h.cfg.Roster = profile, roster

	h.cfg.NewRunner = h.runner(map[string]executor.Result{
		"bugs": {StructuredOutput: findingsJSON(
			`{"file":"app/executor/proc.go","line":10,"severity":"major","confidence":80,"title":"leak","lenses":["bugs"]}`)},
		stageVerify: {StructuredOutput: json.RawMessage(`{"verdicts":[{"id":"solo-1","verdict":"confirmed"}]}`)},
	})

	p := New(h.cfg)
	drain(p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, err := p.Run(ctx)
	require.NoError(t, err)

	require.Len(t, rep.Findings, 1)
	assert.Equal(t, finding.Confirmed, rep.Findings[0].Verdict, "the verify group ran instead of waiting out the delay")
}

func TestVerifier_prompt_materiality(t *testing.T) {
	h := newHarness(t)
	stage, err := h.cfg.Set.Stage(stageVerify)
	require.NoError(t, err)

	v := h.verifier(func(Event) {})
	group := v.groupByDir(judgedReport().Findings)[0]
	text, err := stage.Compose(prompt.ComposeOpts{Vars: v.vars(group)})
	require.NoError(t, err)

	// the verdicts and the materiality test are executed by the model, so the composed prompt is the
	// only thing a Go test can assert
	for _, verdict := range []string{"confirmed", "refined", "rejected", "immaterial", "pre_existing"} {
		assert.Contains(t, text, verdict)
	}
	assert.Contains(t, text, "materiality test")
	assert.Contains(t, text, `"id": "bugs-1"`)
	assert.NotContains(t, text, "{{", "every variable resolved")
}

func TestVerifier_prompt_locationless(t *testing.T) {
	h := newHarness(t)
	stage, err := h.cfg.Set.Stage(stageVerify)
	require.NoError(t, err)

	v := h.verifier(func(Event) {})
	v.cfg.VerifyGroupBy = groupBySource
	group := v.groupByDir(agentFindings("thesis", 2))[0]
	text, err := stage.Compose(prompt.ComposeOpts{Vars: v.vars(group)})
	require.NoError(t, err)

	// a triage panel's arguments cite a thread rather than a line, so the carve-out for them has to
	// survive composition — without it the verifier applies a blast-radius test to a claim with no fix
	assert.Contains(t, text, "When a finding names no file")
	assert.Contains(t, text, "pre_existing does not apply")
	assert.Contains(t, text, "answers the first two questions only")
	assert.Contains(t, text, "the missing location is its defect",
		"a code review's finding that lost its location must not be read as a claim citing no code")
	assert.Contains(t, text, `"file": ""`, "the group the carve-out is written for reaches the model with no location")
}

// verifyMarker is a line only the verify prompt carries, so the harness runner can tell the stage
// apart from a roster agent.
const verifyMarker = "You are verifying findings"

// judgedReport is what synthesis hands over: findings in two directories, plus an open question the
// verify stage must leave alone.
func judgedReport() finding.Report {
	return finding.Report{
		Findings: []finding.Finding{
			{ID: "bugs-1", File: "app/executor/proc.go", Line: 10, Severity: finding.Major, Confidence: 80,
				Title: "leak", Body: "the reader is never closed", Sources: []string{"bugs"}, Lenses: []string{"bugs"}},
			{ID: "bugs-2", File: "app/executor/claude.go", Line: 42, Severity: finding.Minor, Confidence: 60,
				Title: "naming", Sources: []string{"bugs"}, Lenses: []string{"bugs"}},
			{ID: "impl-1", File: "app/ui/model.go", Line: 7, Severity: finding.Critical, Confidence: 90,
				Title: "panic", Sources: []string{"impl"}, Lenses: []string{"impl"}},
			{ID: "impl-2", File: "app/ui/status.go", Line: 3, Severity: finding.Minor, Confidence: 55,
				Title: "typo", Sources: []string{"impl"}, Lenses: []string{"impl"}},
		},
		OpenQuestions: []finding.Finding{{ID: "bugs-3", File: "app/executor/proc.go", Line: 1, Title: "why the retry"}},
	}
}

// dirFindings is n findings in one directory, for the grouping cases where only the shape matters.
func dirFindings(dir string, n int) []finding.Finding {
	out := make([]finding.Finding, 0, n)
	for i := range n {
		out = append(out, finding.Finding{
			ID: dir + "-" + string(rune('a'+i)), File: dir + "/f" + string(rune('a'+i)) + ".go", Line: i + 1,
		})
	}
	return out
}

// agentFindings is n location-less arguments from one agent, the shape a triage panel produces.
func agentFindings(agent string, n int) []finding.Finding {
	out := make([]finding.Finding, 0, n)
	for i := range n {
		out = append(out, finding.Finding{
			ID: agent + "-" + string(rune('a'+i)), Sources: []string{agent}, Title: agent + " argument",
		})
	}
	return out
}

func byID(list []finding.Finding, id string) (finding.Finding, bool) {
	for _, f := range list {
		if f.ID == id {
			return f, true
		}
	}
	return finding.Finding{}, false
}

// fixedRunner answers every request the same way. The groups run concurrently, so nothing here
// records the request — the recorder does that under a mutex.
func fixedRunner(res executor.Result, err error) func(RunnerSpec) Runner {
	return func(RunnerSpec) Runner {
		return &mocks.RunnerMock{RunFunc: func(context.Context, executor.Request, executor.EventSink) (executor.Result, error) {
			return res, err
		}}
	}
}

// verdictRunner answers each verify request with the verdicts whose finding id appears in that
// request's own prompt, so one map drives the whole fan-out without the test tracking which group
// received what.
func verdictRunner(byID map[string]string) func(RunnerSpec) Runner {
	return func(RunnerSpec) Runner {
		return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
			items := []string{}
			for _, id := range slices.Sorted(maps.Keys(byID)) {
				if strings.Contains(req.Prompt, `"id": "`+id+`"`) {
					items = append(items, byID[id])
				}
			}
			return executor.Result{
				StructuredOutput: json.RawMessage(`{"verdicts":[` + strings.Join(items, ",") + `]}`), Tokens: 10,
			}, nil
		}}
	}
}

// activeRunner answers per agent like the harness runner does, and emits one executor event first so
// the find stage's leader releases the gate on real activity rather than on the delay expiring.
func activeRunner(byAgent map[string]executor.Result) func(RunnerSpec) Runner {
	return func(RunnerSpec) Runner {
		return &mocks.RunnerMock{
			RunFunc: func(_ context.Context, req executor.Request, sink executor.EventSink) (executor.Result, error) {
				sink.Emit(executor.Event{Kind: executor.EventActivity, Text: "tool: Read"})
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

// promptRecorder collects every composed prompt a stage handed its runner, guarded because the
// groups run concurrently.
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (r *promptRecorder) factory(res executor.Result, err error) func(RunnerSpec) Runner {
	return func(RunnerSpec) Runner {
		return &mocks.RunnerMock{RunFunc: func(_ context.Context, req executor.Request, _ executor.EventSink) (executor.Result, error) {
			r.mu.Lock()
			r.prompts = append(r.prompts, req.Prompt)
			r.mu.Unlock()
			return res, err
		}}
	}
}

func (r *promptRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.prompts)
}

// countingClock reports how many delays were armed, which is how a test tells one stagger from two.
func countingClock() (executor.Clock, func() int) {
	base := time.Date(2026, 7, 26, 16, 2, 11, 0, time.UTC)
	var mu sync.Mutex
	armed, ticks := 0, 0
	clk := &emocks.ClockMock{
		NowFunc: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			ticks++
			return base.Add(time.Duration(ticks) * time.Second)
		},
		AfterFuncFunc: func(time.Duration, func()) executor.Timer {
			mu.Lock()
			defer mu.Unlock()
			armed++
			return &emocks.TimerMock{StopFunc: func() bool { return true }, ResetFunc: func(time.Duration) bool { return true }}
		},
	}
	return clk, func() int {
		mu.Lock()
		defer mu.Unlock()
		return armed
	}
}

func TestVerifyGroup_shortLabel(t *testing.T) {
	tests := []struct {
		name string
		dirs []string
		want string
	}{
		{"the last element, since the leading path repeats across groups", []string{"app/executor"}, "executor"},
		{"a nested one is still its own last element", []string{"app/prompt/defaults/prompts"}, "prompts"},
		{"a single element is itself", []string{"app"}, "app"},
		{"the repository root has a name of its own", []string{"."}, "root"},
		{"no directories at all is the root too", nil, "root"},
		{"only the first is used, however many were merged", []string{"app/ui", "app/executor", "app"}, "ui"},
		{"a dotted directory keeps its name without the dot", []string{".claude/rules"}, "rules"},
		{"a trailing separator does not produce an empty name", []string{"app/ui/"}, "ui"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := verifyGroup{dirs: tc.dirs}
			assert.Equal(t, tc.want, g.shortLabel())
			assert.NotEmpty(t, g.shortLabel(), "an empty name would leave a row and a tab unlabeled")
		})
	}
}

func TestVerifier_name_displayedNamesAreDistinct(t *testing.T) {
	// the displayed name is resolved in its own pass, so two groups whose first directories share a
	// last element must still be told apart — they each get a row, a tab and a status line
	v := &verifier{}
	groups := []verifyGroup{
		{dirs: []string{"app/ui"}}, {dirs: []string{"other/ui"}},
		{dirs: []string{"app/executor"}}, {dirs: []string{"."}},
	}
	v.name(groups)

	shown := map[string]bool{}
	for _, g := range groups {
		require.NotEmpty(t, g.shown, "every group is named")
		assert.False(t, shown[g.shown], "%q was used twice, so two rows would collide", g.shown)
		shown[g.shown] = true
		assert.Equal(t, stageVerify+" "+g.shown, g.agent(), "the agent name is what a reader sees")
	}

	assert.Equal(t, "ui", groups[0].shown)
	assert.NotEqual(t, "ui", groups[1].shown, "the second ui is suffixed rather than colliding")
	assert.Equal(t, "executor", groups[2].shown)
	assert.Equal(t, "root", groups[3].shown)

	t.Run("the archived filename stays descriptive, so a run is still auditable", func(t *testing.T) {
		// the short name is for a reader; the label spelling out the directories is what an auditor
		// opens, and the two must not be conflated
		assert.Equal(t, "app-ui", groups[0].name)
		assert.Equal(t, "other-ui", groups[1].name)
		assert.NotEqual(t, groups[0].shown, groups[0].name)
	})
}

func TestVerifier_adjusted(t *testing.T) {
	// "2 of 2 kept" is the whole story only if verification is a filter, and it is not: it rejects
	// almost nothing and moves severity a great deal, so a group whose count did not change still has
	// something to report
	v := &verifier{}
	sev := func(id string, s finding.Severity, verdict finding.Verdict) finding.Finding {
		return finding.Finding{ID: id, Severity: s, Verdict: verdict}
	}

	t.Run("a lowered severity is reported even when nothing was rejected", func(t *testing.T) {
		before := []finding.Finding{sev("a-1", finding.Major, "")}
		after := []finding.Finding{sev("a-1", finding.Minor, finding.Refined)}
		assert.Equal(t, ", 1 lowered, 1 refined", v.adjusted(before, after))
	})

	t.Run("a raised severity reads differently from a lowered one", func(t *testing.T) {
		before := []finding.Finding{sev("a-1", finding.Minor, "")}
		after := []finding.Finding{sev("a-1", finding.Critical, finding.Confirmed)}
		assert.Equal(t, ", 1 raised", v.adjusted(before, after))
	})

	t.Run("a group verification left alone says nothing extra", func(t *testing.T) {
		before := []finding.Finding{sev("a-1", finding.Major, "")}
		after := []finding.Finding{sev("a-1", finding.Major, finding.Confirmed)}
		assert.Empty(t, v.adjusted(before, after))
	})

	t.Run("a rejected finding is absent from after and counts as neither", func(t *testing.T) {
		before := []finding.Finding{sev("a-1", finding.Major, ""), sev("a-2", finding.Major, "")}
		after := []finding.Finding{sev("a-1", finding.Minor, finding.Refined)}
		assert.Equal(t, ", 1 lowered, 1 refined", v.adjusted(before, after))
	})
}

func TestVerifier_adjustedRanksSeverity(t *testing.T) {
	// finding.Severity has three values, so a move can skip minor entirely — comparing only against
	// minor at one end counted critical -> major as no change in either direction
	v := &verifier{}
	tbl := []struct {
		name     string
		from, to finding.Severity
		want     string
	}{
		{"critical down to major", finding.Critical, finding.Major, ", 1 lowered"},
		{"major up to critical", finding.Major, finding.Critical, ", 1 raised"},
		{"critical down to minor", finding.Critical, finding.Minor, ", 1 lowered"},
		{"minor up to major", finding.Minor, finding.Major, ", 1 raised"},
		{"unchanged", finding.Major, finding.Major, ""},
	}
	for _, tt := range tbl {
		t.Run(tt.name, func(t *testing.T) {
			before := []finding.Finding{{ID: "a-1", Severity: tt.from}}
			after := []finding.Finding{{ID: "a-1", Severity: tt.to}}
			assert.Equal(t, tt.want, v.adjusted(before, after))
		})
	}
}
