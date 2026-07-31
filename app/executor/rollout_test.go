package executor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordSink is a hand-written EventSink rather than the moq one, and that is forced rather than
// chosen: these tests exercise unexported functions so they must live in package executor, while the
// generated mock lives in a package that imports executor — using it here would be an import cycle.
type recordSink struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordSink) Emit(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordSink) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func TestCodexStderr_sessionID(t *testing.T) {
	s := &codexStderr{}
	tests := []struct {
		name, line, want string
	}{
		{"the banner line", "session id: 019fa071-5114-7573-911c-f6f7b82b3473", "019fa071-5114-7573-911c-f6f7b82b3473"},
		{"case and spacing vary", "  Session ID:   019fa071-5114-7573-911c-f6f7b82b3473  ", "019fa071-5114-7573-911c-f6f7b82b3473"},
		{"embedded in a longer line", "codex v1 session id: 019fa071-5114-7573-911c-f6f7b82b3473 ready", "019fa071-5114-7573-911c-f6f7b82b3473"},
		{"a header line is not one", "model: gpt-5.6-sol", ""},
		{"prose mentioning a session is not one", "the session id is printed somewhere", ""},
		{"a truncated uuid is not one", "session id: 019fa071-5114", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, s.sessionID(tt.line))
		})
	}
}

func TestCodex_rolloutLine(t *testing.T) {
	c := NewCodex(nil, Opts{})
	tests := []struct {
		name, line string
		wantKind   EventKind
		wantText   string
	}{
		{
			// the shape codex actually writes, taken from a real rollout
			name:     "a reasoning headline is activity",
			line:     `{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**Planning scoped diff inspection**"}]}}`,
			wantKind: EventActivity, wantText: "Planning scoped diff inspection",
		},
		{
			name:     "only the headline, never the paragraph behind it",
			line:     `{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**Title**\nthen a whole paragraph that would flood the log"}]}}`,
			wantKind: EventActivity, wantText: "Title",
		},
		{
			// "exec_command" alone says only that codex called a tool, the same nothing a bare "Read"
			// said on the claude side. The command is in the payload, so it is what gets shown.
			name: "a function call reports the command it ran, not the tool's name",
			line: `{"type":"response_item","payload":{"type":"function_call","name":"exec_command",` +
				`"arguments":"{\"cmd\":\"cat app/executor/*.go\",\"workdir\":\"/repo\"}"}}`,
			wantKind: EventProgress, wantText: "cat app/executor/*.go",
		},
		{
			// this one's input is a JavaScript snippet rather than JSON, so the command is matched out
			name: "a custom tool call digs the command out of its snippet",
			line: `{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec",` +
				`"input":"const r = await Promise.all([\n  tools.exec_command({cmd:\"nl -ba app/executor/rollout.go\",workdir:\"/repo\"})"}}`,
			wantKind: EventProgress, wantText: "nl -ba app/executor/rollout.go",
		},
		{
			name:     "a tool call carrying no command falls back to its name",
			line:     `{"type":"response_item","payload":{"type":"function_call","name":"apply_patch"}}`,
			wantKind: EventProgress, wantText: "apply_patch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := c.rolloutLine([]byte(tt.line))
			require.NotNil(t, ev)
			assert.Equal(t, tt.wantKind, ev.Kind)
			assert.Equal(t, tt.wantText, ev.Text)
		})
	}

	t.Run("records carrying nothing to show are dropped", func(t *testing.T) {
		for _, line := range []string{
			`{"type":"session_meta","payload":{}}`,
			`{"type":"event_msg","payload":{"type":"token_count"}}`,
			`{"type":"response_item","payload":{"type":"function_call"}}`, // no name
			`{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"   "}]}}`,
			// a shell tool's name reports nothing on its own, so a call whose command cannot be
			// recovered is dropped rather than shown. The first is the shape that put a bare "exec"
			// on screen for minutes: codex composes several commands as a JS array with no cmd key.
			`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":` +
				`"const cmds = [\n  [\"index\", \"gh api 'repos/o/r/contents/a.ts' --jq '.content'\"],\n` +
				`  [\"validate\", \"rg -n 'roots' src/session.ts\"],\n];"}}`,
			`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"not json"}}`,
			`{"type":"response_item","payload":{"type":"custom_tool_call","name":"shell"}}`,
			// codex writes this alongside every response_item/reasoning; handling both reported each
			// step twice, which is the duplicate this case exists to keep out
			`{"type":"event_msg","payload":{"type":"agent_reasoning","text":"**Planning**"}}`,
			`not json at all`,
			``,
		} {
			assert.Nil(t, c.rolloutLine([]byte(line)), "line %q", line)
		}
	})
}

func TestCodex_readRollout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	c := NewCodex(nil, Opts{})

	write := func(t *testing.T, lines string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path from t.TempDir
		require.NoError(t, err)
		_, err = f.WriteString(lines)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	sink := &recordSink{}
	write(t, `{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**First step**"}]}}`+"\n")
	offset := c.readRollout(path, 0, sink)
	require.Positive(t, offset)
	require.Len(t, sink.all(), 1)
	assert.Equal(t, "First step", sink.all()[0].Text)

	t.Run("a second pass reports only what was appended since", func(t *testing.T) {
		write(t, `{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**Second step**"}]}}`+"\n")
		next := c.readRollout(path, offset, sink)
		assert.Greater(t, next, offset)
		require.Len(t, sink.all(), 2, "the first record must not be replayed")
		assert.Equal(t, "Second step", sink.all()[1].Text)
	})

	t.Run("a missing file is not a failure, it is just quiet", func(t *testing.T) {
		assert.Equal(t, int64(7), c.readRollout(filepath.Join(dir, "absent.jsonl"), 7, &recordSink{}))
	})
}

func TestCodex_tailRollout(t *testing.T) {
	t.Run("an unknown session reports nothing and stops when the run does", func(t *testing.T) {
		c := NewCodex(nil, Opts{})
		sink := &recordSink{}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.tailRollout(ctx, "no-such-session-id", sink, nil)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the tail must end with the run rather than outlive it")
		}
		assert.Empty(t, sink.all(), "a session with no rollout reports nothing at all")
	})

	// codex prints the session id before it creates the rollout, so the file is not there for the first
	// glob. Giving up at that one look silenced a real codex source for a whole review while its rollout
	// filled beside it, which is why the tail keeps looking rather than returning.
	t.Run("a rollout created after the tail starts is still picked up, from its first record", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		id := "019fa762-cebb-7703-a1f6-c1d1832b3bef"
		dir := filepath.Join(home, "sessions", "2026", "07", "28")
		require.NoError(t, os.MkdirAll(dir, 0o750))

		c := NewCodex(nil, Opts{})
		require.Empty(t, c.rolloutPath(id), "the file must be absent when the tail starts, or this proves nothing")

		sink := &recordSink{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.tailRollout(ctx, id, sink, nil)
		}()

		// **this is the assertion that catches a tail giving up at its first look, and it has to come
		// before the file is written.** Asserting only that the records arrive races the goroutine's
		// first glob against this test's own write: a tail that returns immediately still passes
		// whenever the write wins, which is most of the time. Waiting on the tail NOT returning is the
		// half that cannot be won by luck — a single-glob tail is finished within microseconds here.
		select {
		case <-done:
			t.Fatal("the tail gave up at its first look instead of waiting for a file codex had not created yet")
		case <-time.After(2 * rolloutPollInterval):
		}

		body := `{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**First**"}]}}` + "\n" +
			`{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**Second**"}]}}` + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-07-28T01-21-38-"+id+".jsonl"), []byte(body), 0o600))

		require.Eventually(t, func() bool { return len(sink.all()) == 2 }, 5*time.Second, 20*time.Millisecond,
			"both records must be reported, and from offset zero rather than from wherever the file was found")
		cancel()
		<-done
		assert.Equal(t, "First", sink.all()[0].Text, "the record written before the file was found is not skipped")
		assert.Equal(t, "Second", sink.all()[1].Text)
	})

	t.Run("an empty id never globs", func(t *testing.T) {
		c := NewCodex(nil, Opts{})
		assert.Empty(t, c.rolloutPath(""))
	})
}

func TestCodex_readRollout_partialTrailingLine(t *testing.T) {
	// the record codex is midway through writing. Counting it would advance the offset past bytes
	// that do not exist yet, so the completed record is skipped when it finally lands - and the half
	// of it that was read fails to parse and is lost. Both halves of that bug are what this pins.
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	c := NewCodex(nil, Opts{})

	whole := `{"type":"response_item","payload":{"type":"reasoning","summary":[{"text":"**Done**"}]}}` + "\n"
	half := `{"type":"response_item","payload":{"type":"reason`
	require.NoError(t, os.WriteFile(path, []byte(whole+half), 0o600))

	sink := &recordSink{}
	offset := c.readRollout(path, 0, sink)
	require.Len(t, sink.all(), 1, "only the complete record is reported")
	assert.Equal(t, "Done", sink.all()[0].Text)
	assert.Equal(t, int64(len(whole)), offset, "the offset stops at the last newline, not past the partial line")

	t.Run("the rest of that record is reported once it lands", func(t *testing.T) {
		rest := `ing","summary":[{"text":"**Finished**"}]}}` + "\n"
		require.NoError(t, os.WriteFile(path, []byte(whole+half+rest), 0o600))

		next := c.readRollout(path, offset, sink)
		require.Len(t, sink.all(), 2, "the record completed since the last pass must not be skipped")
		assert.Equal(t, "Finished", sink.all()[1].Text)
		assert.Equal(t, int64(len(whole+half+rest)), next)
	})
}

func TestCodex_codexHome(t *testing.T) {
	c := NewCodex(nil, Opts{})

	t.Run("CODEX_HOME wins when set", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "/somewhere/else")
		assert.Equal(t, "/somewhere/else", c.codexHome())
		assert.Empty(t, c.rolloutPath("019fa071-5114-7573-911c-f6f7b82b3473"),
			"and the glob follows it, finding nothing there rather than falling back to the default")
	})

	t.Run("the default otherwise", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".codex"), c.codexHome())
	})
}

func TestCodex_rolloutCmd_quotedKey(t *testing.T) {
	// a custom_tool_call named "exec" carries JS in its input, and the cmd key inside it is quoted:
	// {"cmd":"..."}. A pattern expecting an unquoted cmd: "..." misses it, rolloutCmd returns nothing
	// and the line degrades to the bare tool name — 26 lines reading just "exec" in one observed run.
	c := NewCodex(nil, Opts{})

	tests := []struct {
		name, input, want string
	}{
		{"quoted key, as a custom_tool_call actually writes it",
			`const r = await tools.exec_command({"cmd":"sed -n '1,240p' scope.md","workdir":"/repo"})`,
			`sed -n '1,240p' scope.md`},
		{"unquoted key still works", `exec_command(cmd: "go test ./...")`, "go test ./..."},
		{"escaped quotes inside the command survive",
			`{"cmd":"rg -n \"foo|bar\" ."}`, `rg -n \"foo|bar\" .`},
		{"a key merely ending in cmd is not the cmd key", `{"subcmd":"nope"}`, ""},
		{"no command at all", `{"workdir":"/repo"}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, c.rolloutCmd(rolloutPayload{Input: tc.input}))
		})
	}
}

func TestCodex_tailRollout_touchesOnlyOnAdvance(t *testing.T) {
	// the guard's other direction, and the one that matters most: a poll finding no new bytes must
	// touch nothing. Every existing test asserts the timer IS reset, so a tail that touched on every
	// poll would pass them all — and a watchdog fed by its own polling never fires at all, which is
	// the failure that cannot be seen from outside.
	tests := []struct {
		name    string
		content string
		want    int32
	}{
		{"an empty rollout has not advanced, so nothing is touched", "", 0},
		{"a record advances the file and is life", `{"type":"response_item","payload":` +
			`{"type":"reasoning","summary":[{"text":"**Reading proc.go**"}]}}` + "\n", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CODEX_HOME", dir)
			sessions := filepath.Join(dir, "sessions", "2026", "07", "26")
			require.NoError(t, os.MkdirAll(sessions, 0o750))
			const id = "019fa0ca-0000-1111-2222-333344445555"
			require.NoError(t, os.WriteFile(filepath.Join(sessions, "rollout-2026-07-26T20-00-00-"+id+".jsonl"),
				[]byte(tc.content), 0o600))

			var touches atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			c := NewCodex(nil, Opts{})

			done := make(chan struct{})
			go func() {
				defer close(done)
				c.tailRollout(ctx, id, &recordSink{}, func() { touches.Add(1) })
			}()

			// cancel as soon as the first pass has been made, so the count is the first pass's alone and
			// the case does not depend on how many poll intervals elapse
			cancel()
			<-done
			assert.Equal(t, tc.want, touches.Load())
		})
	}
}
