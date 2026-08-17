package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

// catalogStages are the stage prompts the catalog reports, in pipeline order. It is the same slice
// app/prompt validates a profile's stage override against, so the catalog cannot advertise a stage a
// profile would then be refused for naming.
var catalogStages = prompt.Stages()

// configCmd is the `revmux config` subcommand. It only records the selection: go-flags calls Execute
// from inside parseArgs, before the injected writers and the loaded prompt tree exist, so the catalog
// is built and written by run.
type configCmd struct {
	opts *options
}

// catalog is what `revmux config` emits — the resolved configuration a caller model needs to compose
// an invocation without reading the prompt tree.
type catalog struct {
	Knobs      []knob            `json:"knobs"`
	Profiles   []profileInfo     `json:"profiles"`
	Lenses     []prompt.LensInfo `json:"lenses"`
	Stages     []stageInfo       `json:"stages"`
	Vocabulary vocabulary        `json:"vocabulary"`
	Paths      pathInfo          `json:"paths"`
}

// knob is one runtime setting with the precedence layer that supplied it. The value alone would not
// tell a caller whether it is worth passing the flag.
type knob struct {
	Name   string `json:"name"`
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// profileInfo is a profile, the roster it would dispatch with colors included, and the runner each stage
// resolves to under it. Stages carries every stage rather than the overridden ones, so a caller does not
// apply the fallback rule himself to learn what actually runs.
type profileInfo struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Runner      stageRunner        `json:"runner"`
	Roster      []prompt.AgentSpec `json:"roster"`
	Stages      []stageRunner      `json:"stages"`
}

// stageInfo is one stage prompt as the catalog reports it: its description, plus a runner only when the
// file authored one. The shipped pair author none, so `"executor": ""` would put a value outside the
// vocabulary in front of a caller. What runs is profileInfo.Stages.
type stageInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Executor    string `json:"executor,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
}

// stageRunner is a resolved runner as the catalog reports it — one per stage under a profile, plus the
// profile's own base, where Name is omitted. It is its own type rather than a stageInfo with the
// description omitted: sharing one needs that field omitempty, which would drop the key when empty.
type stageRunner struct {
	Name     string `json:"name,omitempty"`
	Executor string `json:"executor"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// vocabulary is the accepted front-matter values, read from the same constants validate checks
// against so a new effort level cannot ship working but undiscoverable.
type vocabulary struct {
	Executors []string `json:"executors"`
	Efforts   []string `json:"efforts"`
}

// pathInfo is where this invocation resolved its directories, plus the tasks that already exist — a
// --run name collides with an existing round and a caller cannot avoid that blind. TasksError and
// WorkDirError carry why a field is the raw flag: an unreadable tasks root reported as no tasks reads
// as "nothing is there" and mints a duplicate id.
type pathInfo struct {
	TasksDir        string     `json:"tasks_dir"`
	TasksError      string     `json:"tasks_error,omitempty"`
	ConfigDir       string     `json:"config_dir"`
	ProjectDir      string     `json:"project_dir,omitempty"`
	ProfileFallback string     `json:"profile_fallback,omitempty"`
	WorkDir         string     `json:"workdir"`
	WorkDirError    string     `json:"workdir_error,omitempty"`
	Tasks           []taskInfo `json:"tasks"`
}

// taskInfo is one existing task: its id, what its task.md says about it, and the rounds already recorded
// under it. This is what a caller matches an in-flight review against — without the anchors it cannot
// tell pr-123 from the near-duplicate it is about to mint. MetaError and RoundsError carry why a field
// is empty; the task is still listed either way, since one omitted is one a caller gives a second id to.
type taskInfo struct {
	ID string `json:"id"`
	task.Meta
	MetaError   string   `json:"meta_error,omitempty"`
	Rounds      []string `json:"rounds"`
	RoundsError string   `json:"rounds_error,omitempty"`
}

// Execute records that the config command was selected. Writing the catalog here would bypass the
// injected stdout and leave nothing for a test to capture.
func (c *configCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showConfig = true
	return nil
}

// catalog assembles what resolved, never what is embedded: a user who overrode one lens and added
// another must see his own tree, or this describes a review that will not happen.
func (o options) catalog(set *prompt.Set) catalog {
	c := catalog{
		Knobs:      o.knobs(),
		Lenses:     set.Lenses(),
		Vocabulary: vocabulary{Executors: prompt.Executors(), Efforts: prompt.Efforts()},
		Paths:      o.paths(),
	}

	known := set.LensNames()
	for _, name := range set.ProfileNames() {
		p, err := set.Profile(name)
		if err != nil {
			continue
		}
		// the roster resolves for every loaded profile: Load rejected any that did not validate
		roster, err := p.Roster(nil, known)
		if err != nil {
			continue
		}
		base := p.Runner()
		c.Profiles = append(c.Profiles, profileInfo{Name: name, Description: p.Description,
			Runner: stageRunner{Executor: base.Executor, Model: base.Model, Effort: base.Effort},
			Roster: roster, Stages: o.profileStages(set, p)})
	}

	for _, name := range catalogStages {
		st, err := set.Stage(name)
		if err != nil {
			continue
		}
		c.Stages = append(c.Stages, stageInfo{Name: name, Description: st.Description,
			Executor: st.Executor, Model: st.Model, Effort: st.Effort})
	}
	return c
}

// profileStages resolves every stage under one profile, so the reported runner is the one that would
// actually run rather than the stage file's own. The description is left off here: it describes a body
// no profile changes, and the top-level stages entry already carries it.
func (o options) profileStages(set *prompt.Set, p *prompt.Profile) []stageRunner {
	out := make([]stageRunner, 0, len(catalogStages))
	for _, name := range catalogStages {
		st, err := p.Stage(set, name)
		if err != nil {
			continue
		}
		out = append(out, stageRunner{Name: name, Executor: st.Executor, Model: st.Model, Effort: st.Effort})
	}
	return out
}

// knobs reports every INI-backed setting with the layer parseArgs recorded during the load. The
// origins are read, never re-derived: a second tracking mechanism would be one more thing to drift.
func (o options) knobs() []knob {
	v := reflect.ValueOf(o)
	out := make([]knob, 0, v.NumField())
	for f := range v.Type().Fields() {
		if f.Tag.Get("no-ini") != "" || f.Tag.Get("long") == "" {
			continue
		}
		name := f.Tag.Get("long")
		val := v.FieldByIndex(f.Index).Interface()
		if d, ok := val.(time.Duration); ok {
			val = d.String()
		}
		out = append(out, knob{Name: name, Value: val, Source: o.knobOrigins[name]})
	}
	return out
}

// paths resolves the two roots and the working directory, and lists the tasks under the tasks root. Each
// part reports its own failure instead of a plausible-looking value; an absent tasks root is a clean
// install rather than a failure.
func (o options) paths() pathInfo {
	p := pathInfo{TasksDir: o.TasksDir, ConfigDir: o.layers.user, ProjectDir: o.layers.project,
		WorkDir: o.WorkDir, Tasks: []taskInfo{}}
	// reported so a caller can tell a round it left without a profile will still be calibrated. A read
	// failure is left silent here: the review reports it, and this command is not where it is decided
	if fallback, err := o.projectProfile(); err == nil {
		p.ProfileFallback = fallback
	}
	if dir, err := o.workDir(); err != nil {
		p.WorkDirError = err.Error()
	} else {
		p.WorkDir = dir
	}

	abs, err := filepath.Abs(o.TasksDir)
	if err != nil {
		p.TasksError = fmt.Sprintf("resolve tasks dir %q: %s", o.TasksDir, err)
		return p
	}
	p.TasksDir = abs

	tasks, err := o.tasks(abs)
	if err != nil {
		p.TasksError = err.Error()
	}
	p.Tasks = tasks
	return p
}

// tasks reports every task under the tasks root with the metadata a caller matches on. A task.md that is
// absent or will not parse leaves the anchors empty and reports meta_error rather than hiding the task:
// silently empty anchors are how a typo'd key becomes a duplicate id. Rounds come from task.Rounds and
// ids from task.List, the same enumerations the inventory and `revmux stats` read.
func (o options) tasks(root string) ([]taskInfo, error) {
	out := []taskInfo{}
	names, err := task.List(root)
	if err != nil {
		return out, fmt.Errorf("list tasks: %w", err)
	}

	for _, name := range names {
		dir := filepath.Join(root, name)
		meta, loadErr := task.Load(dir)
		info := taskInfo{ID: name, Meta: meta, Rounds: []string{}}
		if loadErr != nil {
			info.MetaError = loadErr.Error()
		}
		rounds, roundsErr := task.Rounds(dir)
		if roundsErr != nil {
			info.RoundsError = roundsErr.Error()
		} else {
			info.Rounds = rounds
		}
		out = append(out, info)
	}
	return out, nil
}
