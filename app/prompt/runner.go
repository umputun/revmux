package prompt

import (
	"fmt"
	"slices"
	"strings"
)

// Runner is one authored `model:` value, resolved: which binary, which of its models, and how hard it
// thinks. One field carries all three because they are not independent — `opus` means nothing to codex,
// so separate fields let a file declare a pairing that cannot run.
type Runner struct {
	Executor string
	Model    string
	Effort   string
}

// parseRunner reads `<executor>[/<model>][:<effort>]` — `claude`, `claude/opus:high`,
// `codex/gpt-6.2-sol`, `codex:high`. The executor is mandatory and closed, so the value validates
// itself; a trailing slash is rejected rather than accepted as a second spelling of the bare binary.
// It splits on the first slash, so a model name carrying one survives, and on the last colon, whose
// suffix must be a real effort — a typo'd `:hgih` is a load error, not part of the model name.
func parseRunner(s string) (Runner, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Runner{}, nil
	}

	out := Runner{}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		out.Effort, s = s[i+1:], s[:i]
		if !slices.Contains(efforts, out.Effort) {
			return Runner{}, fmt.Errorf("unknown effort %q, want one of %v", out.Effort, efforts)
		}
	}

	var found bool
	out.Executor, out.Model, found = strings.Cut(s, "/")
	// a trailing slash is a second spelling of the binary alone, and an accepted second spelling is one
	// nobody agrees on: `claude/` would inherit a fallback model or fall to the binary's default while
	// looking like it named one
	if found && out.Model == "" {
		return Runner{}, fmt.Errorf("no model after the slash in %q, write the binary alone for its default", s)
	}
	if !slices.Contains(executors, out.Executor) {
		return Runner{}, fmt.Errorf("unknown executor %q in %q, want one of %v", out.Executor, s, executors)
	}
	return out, nil
}

// or returns r with anything it omits taken from fallback. The executor is the one field that cannot be
// inherited piecemeal: an entry naming a binary brings its own model, or it would run the fallback's
// model on the wrong binary — the pairing this whole type exists to prevent.
func (r Runner) or(fallback Runner) Runner {
	if r.Executor == "" {
		return fallback
	}
	if r.Executor != fallback.Executor {
		// a different binary: only the effort, which is not a property of either model, can carry over
		if r.Effort == "" {
			r.Effort = fallback.Effort
		}
		return r
	}
	if r.Model == "" {
		r.Model = fallback.Model
	}
	if r.Effort == "" {
		r.Effort = fallback.Effort
	}
	return r
}
