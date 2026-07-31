package executor

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// rolloutPollInterval is how often the tailer looks for new bytes. Codex writes a record per reasoning
// step or tool call, so anything finer just spins on an unchanged file.
const rolloutPollInterval = 500 * time.Millisecond

// sessionIDPattern matches the line codex prints in its stderr banner. That line is the ONLY thing
// stderr is read for here — the activity itself never appears on it.
var sessionIDPattern = regexp.MustCompile(`(?i)\bsession id:\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`)

// rolloutEvent is one line of a codex session rollout. Only what drives progress is decoded.
type rolloutEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type rolloutPayload struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	Arguments string `json:"arguments"` // function_call: a JSON string carrying cmd
	Input     string `json:"input"`     // custom_tool_call: a JS snippet carrying cmd inline
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

// cmdPattern digs the command out of a custom_tool_call's input, which is a JavaScript snippet rather
// than JSON — codex writes `tools.exec_command({cmd:"..."})` there instead of arguments.
var cmdPattern = regexp.MustCompile(`"?\bcmd"?\s*:\s*"((?:[^"\\]|\\.)*)"`)

// execTools run a shell command and carry it in the payload, so the name alone reports nothing a reader
// can act on. A call under one of these whose command cannot be recovered is dropped rather than shown:
// codex composes some multi-command snippets as a JS array with no `cmd` key at all, and those put a bare
// "exec" in the activity column for minutes at a time — worse than leaving the previous line standing,
// which at least names something that happened. Every other tool keeps the name fallback, since
// "apply_patch" or "update_plan" says what the agent is doing.
//
// Liveness does not ride on this. The rollout tail touches the idle timer whenever the file advances,
// never from the sink, and a codex leader releases the stagger gate on its first raw stdout write.
var execTools = map[string]struct{}{"exec": {}, "exec_command": {}, "shell": {}}

// sessionID pulls the session id out of one stderr line, or "" when the line is not the banner.
func (s *codexStderr) sessionID(line string) string {
	if m := sessionIDPattern.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// rolloutPath finds the session's rollout file. Codex names it by timestamp and session id under a
// dated directory, so the id is globbed for rather than the path being reconstructed.
func (c *Codex) rolloutPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	root := c.codexHome()
	if root == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "rollout-*-"+sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// codexHome is where codex keeps its sessions. CODEX_HOME wins when set — codex's own help documents
// it, and a reviewer who relocates it would otherwise get a silent codex with no clue why, since the
// glob would simply find nothing under the default.
func (c *Codex) codexHome() string {
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// tailRollout follows a codex session's rollout file and reports what the agent is doing.
//
// **This exists because codex writes nothing to stdout until it answers.** A review agent spends
// minutes reading files and reasoning, and every byte of that lands in the rollout — stdout stays
// empty, so a run watched through stdout alone shows one banner and then nothing at all for the whole
// review. Its stderr carries the reasoning as prose, but parsing prose is what the rollout exists to
// spare us; stderr is read for the session id and nothing else.
//
// **The file is looked for on every pass, not once at the start, and that is not a refinement.** Codex
// prints the session id before it creates the rollout — measured at 32ms between the two — so a single
// glob at the banner loses a race it has no reason to win, and losing it once silenced a codex source
// for a whole eleven-minute review while the file beside it filled with 28 reasoning records. Giving up
// there is indistinguishable from a codex that never worked. Nothing is lost by finding it late either:
// the read starts at offset zero, so a file discovered a poll interval in is still read from its first
// record.
//
// Blocks until ctx is done, so the caller runs it in its own goroutine and cancels it once the process
// has exited AND flushed its last record.
func (c *Codex) tailRollout(ctx context.Context, sessionID string, sink EventSink, touch func()) {
	var (
		path   string
		offset int64
	)
	for {
		if path == "" {
			path = c.rolloutPath(sessionID)
		}
		if path == "" {
			// nothing to read yet, but the run may still be about to create it
			select {
			case <-ctx.Done():
				// one last look, for the same reason the read below takes one: a short run can create
				// the rollout and be over inside a single interval
				if path = c.rolloutPath(sessionID); path != "" {
					c.readRollout(path, 0, sink)
				}
				return
			case <-time.After(rolloutPollInterval):
				continue
			}
		}

		// **liveness is the file advancing, not a record that happens to render.** Touching from the
		// sink instead ties the watchdog to the display filter: a tool call, a function_call_output, an
		// event_msg or a reasoning record with an empty summary all move the file without producing an
		// event, so codex could be demonstrably working and still starve the timer — the same failure
		// this tail exists to fix, one layer in.
		next := c.readRollout(path, offset, sink)
		if next != offset && touch != nil {
			touch()
		}
		offset = next

		select {
		case <-ctx.Done():
			// one last pass so the final records are not lost. It emits but does not touch, and it does
			// not have to: the caller withdraws the touch before canceling, because the pass at the top
			// of this loop has the same window and a comment here would cover neither.
			c.readRollout(path, offset, sink)
			return
		case <-time.After(rolloutPollInterval):
		}
	}
}

// readRollout consumes whatever has been appended since offset and returns the new offset. A partial
// last line is left for the next pass rather than parsed half-written.
func (c *Codex) readRollout(path string, offset int64, sink EventSink) int64 {
	f, err := os.Open(path) //nolint:gosec // path comes from a glob under the user's own codex home
	if err != nil {
		return offset
	}
	defer func() { _ = f.Close() }() // read-only, nothing to salvage from a failed close

	if _, err = f.Seek(offset, 0); err != nil {
		return offset
	}

	// ReadBytes rather than a Scanner, and that is the whole point of this loop. A Scanner strips the
	// delimiter and cannot say whether the token it returned ended with one, so counting len+1 per
	// token charges a byte that does not exist yet for the final line — and the final line of a file
	// being appended to is a partial record most of the time. That both loses the record, since half
	// an object fails to parse, and leaves the offset past bytes never read, so the completed record
	// is skipped when it lands. ReadBytes returns the delimiter it found, and returns an error instead
	// when it did not, which is exactly the boundary this needs.
	r := bufio.NewReader(f)
	var read int64
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // a partial trailing line: not counted, not parsed, retried whole on the next pass
		}
		read += int64(len(line))
		if ev := c.rolloutLine(line); ev != nil {
			c.emit(sink, *ev)
		}
	}
	return offset + read
}

// rolloutLine turns one rollout record into an event, or nil when it carries nothing worth showing.
//
// Reasoning summaries are activity: codex titles each step ("**Planning the diff**"), and that title is
// the closest thing it has to the prose claude streams. Tool calls are progress, matching claude, so
// one throttle governs both executors.
func (c *Codex) rolloutLine(line []byte) *Event {
	var ev rolloutEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil
	}
	var p rolloutPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return nil
	}

	switch {
	// only response_item/reasoning, never event_msg/agent_reasoning. Codex writes BOTH for every
	// reasoning step — the capture this was built from held eight of each — so handling the two would
	// report every step twice, which is exactly what it did before this case was narrowed.
	case ev.Type == "response_item" && p.Type == "reasoning":
		for _, s := range p.Summary {
			if text := c.rolloutTitle(s.Text); text != "" {
				return &Event{Kind: EventActivity, Text: text}
			}
		}
	case ev.Type == "response_item" && (p.Type == "function_call" || p.Type == "custom_tool_call"):
		if p.Name == "" {
			return nil
		}
		if cmd := c.rolloutCmd(p); cmd != "" {
			return &Event{Kind: EventProgress, Text: cmd}
		}
		if _, shell := execTools[p.Name]; shell {
			return nil
		}
		return &Event{Kind: EventProgress, Text: p.Name}
	}
	return nil
}

// rolloutCmd is the command a tool call actually ran. The name alone — "exec_command", "exec" — says
// only that codex called a tool, which is the same nothing a bare "Read" said on the claude side.
//
// The two record shapes carry it differently: a function_call has JSON arguments, a custom_tool_call
// has a JavaScript snippet where codex composes several calls, so that one is matched rather than
// parsed. Returns "" when neither yields anything, and the caller falls back to the name.
func (c *Codex) rolloutCmd(p rolloutPayload) string {
	if p.Arguments != "" {
		var args struct {
			Cmd     string `json:"cmd"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(p.Arguments), &args); err == nil {
			if cmd := cmp.Or(args.Cmd, args.Command); cmd != "" {
				return c.shorten(cmd)
			}
		}
	}
	if m := cmdPattern.FindStringSubmatch(p.Input); m != nil {
		return c.shorten(m[1])
	}
	return ""
}

// shorten folds a command onto one line. How much of it fits is the renderer's call, not this one's.
func (c *Codex) shorten(cmd string) string {
	return clampRunes(flattenLines(cmd))
}

// rolloutTitle reduces a reasoning record to its headline: the first line, with the bold markers codex
// wraps it in removed. Only the first line, because some codex versions append a whole paragraph after
// the title and forwarding it would reintroduce the flood the rollout is read to avoid.
func (c *Codex) rolloutTitle(text string) string {
	title, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return clampRunes(strings.TrimSpace(strings.Trim(strings.TrimSpace(title), "*")))
}
