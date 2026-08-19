// Package executor runs and supervises the model CLIs revmux fans out to.
//
// Every process is started through a CommandRunner, watched by an idle timer driven by an injected
// Clock, and torn down as a process group. Nothing here knows about lenses, stages or terminals.
package executor

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"time"
)

//go:generate moq -out mocks/command_runner.go -pkg mocks -skip-ensure -fmt goimports . CommandRunner
//go:generate moq -out mocks/event_sink.go -pkg mocks -skip-ensure -fmt goimports . EventSink
//go:generate moq -out mocks/clock.go -pkg mocks -skip-ensure -fmt goimports . Clock
//go:generate moq -out mocks/timer.go -pkg mocks -skip-ensure -fmt goimports . Timer

// CommandRunner builds the command a run executes. It exists so tests can hand back a process of their
// own choosing instead of spawning a real model CLI.
type CommandRunner interface {
	Command(ctx context.Context, name string, args ...string) *exec.Cmd
}

// EventSink receives activity as it happens. Implemented by an adapter in app/pipeline.
type EventSink interface {
	Emit(Event)
}

// Clock supplies the two timers a supervised run needs, so timeout behavior is deterministic in tests.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is the subset of time.Timer the idle watchdog uses.
type Timer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

// EventKind names what an Event reports.
type EventKind string

// Event kinds emitted by every executor. EventActivity and EventProgress both mean the process produced
// model output — prose in one case, a tool call in the other — and are kept distinct from everything
// else because the stagger waits on first output and must not be released by a banner or a fork. There
// is deliberately no "started" kind: one existed and latched the gate before a byte had been read.
const (
	EventInfo      EventKind = "info"
	EventActivity  EventKind = "activity"
	EventProgress  EventKind = "progress"
	EventRateLimit EventKind = "rate_limit"
	EventFinished  EventKind = "finished"
)

// Event is one activity item from a supervised process.
type Event struct {
	Kind EventKind
	Text string
}

// Opts is the construction-time configuration shared by every run of one executor. Model and effort are
// deliberately absent: they travel on Request, since one instance serves roster entries that differ. A
// zero IdleTimeout or HardTimeout disables that watchdog, so the composition root sets both.
type Opts struct {
	IdleTimeout    time.Duration
	HardTimeout    time.Duration
	WorkDir        string
	PreserveAPIKey bool
	PreserveMCP    bool
	Clock          Clock
}

// Request carries everything that varies between runs of one executor.
type Request struct {
	Prompt    string
	Model     string
	Effort    string
	Schema    json.RawMessage
	RawOutput io.Writer
}

// RateLimitInfo is the payload of a claude rate_limit_event.
type RateLimitInfo struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`
	OverageStatus string `json:"overageStatus"`
}

// Result is one completed run. A non-zero ExitCode or a set IdleTimedOut is reported here rather than as
// an error: deciding whether that degrades the source belongs to the pipeline.
type Result struct {
	StructuredOutput json.RawMessage
	Raw              string
	ExitCode         int
	IdleTimedOut     bool
	RateLimited      bool
	RateLimit        RateLimitInfo
	RequestedModel   string
	ActualModel      string
	Tokens           int
	TTFTMs           int
}

// NewRunner returns the production CommandRunner backed by os/exec.
func NewRunner() CommandRunner { return realRunner{} }

// NewClock returns the production Clock backed by the time package.
func NewClock() Clock { return realClock{} }

type realRunner struct{}

func (realRunner) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	//nolint:gosec // running an operator-configured model CLI with composed arguments is the package's job
	return exec.CommandContext(ctx, name, args...)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return realTimer{t: time.AfterFunc(d, f)}
}

type realTimer struct{ t *time.Timer }

func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
