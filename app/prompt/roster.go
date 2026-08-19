package prompt

import (
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	executorClaude = "claude"

	// overrideAgent is the name the --lenses override's single agent carries. It reaches
	// Finding.sources and becomes agents/<name>.jsonl, so it can never be empty.
	overrideAgent = "lenses"

	// ansiDefaultFg restores the default foreground and nothing else. A full reset would clear the
	// enclosing pane's background too.
	ansiDefaultFg = "\x1b[39m"
)

// the two accepted vocabularies, checked by validate and reported verbatim by `revmux config`.
var (
	executors = []string{executorClaude, "codex", "agy"}
	efforts   = []string{"low", "medium", "high", "xhigh", "max"}
)

// overridableStages is the closed set a profile's `stages:` block may name: the stages the pipeline
// dispatches. The tree's glob turns any `prompts/*.md` into a stage, so validating against whatever
// loaded would accept an override that then silently never runs.
var overridableStages = []string{"synthesis", "verify"}

// ansiColors maps the accepted color names to their ANSI index. A raw index is deliberately not accepted
// as front matter: `color: 12` says nothing to whoever edits the file.
var ansiColors = map[string]int{
	"black": 0, "red": 1, "green": 2, "yellow": 3,
	"blue": 4, "magenta": 5, "cyan": 6, "white": 7,
	"bright-black": 8, "bright-red": 9, "bright-green": 10, "bright-yellow": 11,
	"bright-blue": 12, "bright-magenta": 13, "bright-cyan": 14, "bright-white": 15,
}

// colorPalette fills an omitted color by roster position rather than by hashing the name, so two
// runs of one profile color an agent identically even after the reviewer renames it.
var colorPalette = []string{"cyan", "magenta", "green", "yellow", "blue", "red", "bright-cyan", "bright-magenta"}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// each kind of prompt file declares its own front-matter shape rather than sharing one, because unknown
// keys are rejected and one shared shape accepts every key in every file. Every one of them selects its
// runner through a single `model:` string — see runner.go — and there is no `executor:` or `effort:` key.

// profileYAML is a profile's front matter: the roster-wide runner default, the roster, and the per-stage
// runner overrides. A per-entry `model:` still wins, since a roster mixing claude and codex is the point.
type profileYAML struct {
	Description string            `yaml:"description"`
	Model       string            `yaml:"model"`
	Agents      []agentYAML       `yaml:"agents"`
	Stages      map[string]string `yaml:"stages"`
}

// stageYAML is a stage prompt's front matter. The shipped pair declare no `model:`; one authored here
// sits between a profile's `stages:` override and its own `model:`.
type stageYAML struct {
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
}

// lensYAML is a lens file's front matter. A lens is executor-agnostic text and carries no runner
// selection: the roster entry composing it is where the model lives.
type lensYAML struct {
	Description string `yaml:"description"`
}

type agentYAML struct {
	Name   string   `yaml:"name"`
	Lenses []string `yaml:"lenses"`
	Model  string   `yaml:"model"`
	Color  string   `yaml:"color"`
}

// Profile is a roster plus the shared body every agent in it composes, and the runner it resolves each
// stage to.
type Profile struct {
	doc
	Name   string
	runner Runner
	agents []AgentSpec
	stages map[string]Runner // per-stage runner overrides, never a body
}

// Stage is a stage prompt — synthesis or verify. It has no roster and must not expose one. Executor,
// Model and Effort are the resolved selection Profile.Stage fills in; runner is what the file declared.
type Stage struct {
	doc
	Name     string
	Executor string
	Model    string
	Effort   string
	runner   Runner
}

// AgentSpec is one roster entry. Color is the resolved form a renderer hands straight to lipgloss —
// an ANSI index "0"-"15" or the original #RRGGBB — and ColorName is the authored value, empty when
// the color came from the palette, so `revmux config` reads back `cyan` rather than "6".
type AgentSpec struct {
	Name      string   `json:"name"`
	Lenses    []string `json:"lenses"`
	Executor  string   `json:"executor"`
	Model     string   `json:"model,omitempty"`
	Effort    string   `json:"effort,omitempty"`
	Color     string   `json:"color"`
	ColorName string   `json:"color_name,omitempty"`
}

// DerivedSpec colors a process the roster does not name — a stage, a verify group — so the two renderers
// cannot disagree about it. The palette entry comes from a hash of the name rather than an arrival index:
// both renderers build their rows independently, and a hash needs nothing threaded through.
func DerivedSpec(name string) AgentSpec {
	spec := AgentSpec{Name: name}
	if name == "" {
		return spec
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	spec.ColorName = colorPalette[int(h.Sum32()%uint32(len(colorPalette)))] //nolint:gosec // a palette length is far inside uint32
	spec.Color = strconv.Itoa(ansiColors[spec.ColorName])
	return spec
}

// Efforts returns the accepted effort vocabulary, the same slice parseRunner checks a `model:` against.
func Efforts() []string { return slices.Clone(efforts) }

// Executors returns the accepted binary vocabulary, the same slice parseRunner checks a `model:` against.
func Executors() []string { return slices.Clone(executors) }

// Runner reports the profile's own `model:`, which the roster falls back to and which the single agent
// `--lenses` synthesizes runs on. Exported because nothing outside this package can otherwise tell which
// binary that override needs — the reported roster and stages may all name another one.
func (p *Profile) Runner() Runner { return p.runner }

// Stages returns the stages a profile may override, in pipeline order — the same slice validate checks
// against, so `revmux config` cannot report a set the loader would refuse.
func Stages() []string { return slices.Clone(overridableStages) }

// Roster returns the resolved roster, every entry carrying a color. A non-empty lensOverride replaces the
// roster with a single agent carrying every named lens — one viewpoint, not two corroborating votes —
// inheriting the profile's runner whole. Taking the model while forcing the binary to claude built a
// claude agent asked for the profile's codex model.
func (p *Profile) Roster(lensOverride []string, known map[string]struct{}) ([]AgentSpec, error) {
	specs := slices.Clone(p.agents)
	if len(lensOverride) > 0 {
		override := AgentSpec{
			Name: overrideAgent, Lenses: slices.Clone(lensOverride),
			Executor: p.runner.Executor, Model: p.runner.Model, Effort: p.runner.Effort,
		}
		if err := override.validate(known); err != nil {
			return nil, fmt.Errorf("lens override: %w", err)
		}
		specs = []AgentSpec{override}
	}

	for i := range specs {
		specs[i].Lenses = slices.Clone(specs[i].Lenses)
		color, err := specs[i].resolveColor()
		if err != nil {
			return nil, err
		}
		if color == "" {
			color = strconv.Itoa(ansiColors[colorPalette[i%len(colorPalette)]])
		}
		specs[i].Color = color
	}
	return specs, nil
}

// Stage resolves the stage prompt this profile runs: the shared body from the set, under the runner three
// layers produce. Highest first: the profile's `stages:` entry, the stage file's own `model:`, then the
// profile's `model:`. The shipped stage files name none, which is what makes `codex-only` one line.
func (p *Profile) Stage(set *Set, name string) (*Stage, error) {
	st, err := set.Stage(name)
	if err != nil {
		return nil, err
	}
	// applied in order and never collapsed first: Runner.or refuses to carry a model across binaries, so
	// folding two layers loses whichever of them the third turns out to be compatible with
	res := st.runner.or(p.runner)
	if over, ok := p.stages[name]; ok {
		res = over.or(st.runner).or(p.runner)
	}

	out := *st
	out.Executor, out.Model, out.Effort = res.Executor, res.Model, res.Effort
	return &out, nil
}

func (p *Profile) validate(known map[string]struct{}) error {
	// parseRunner already checked every executor and effort, so what is left is the stage names
	for _, name := range slices.Sorted(maps.Keys(p.stages)) {
		if !slices.Contains(overridableStages, name) {
			return fmt.Errorf("profile %s: unknown stage %q, want one of %v", p.Name, name, overridableStages)
		}
	}
	if len(p.agents) == 0 {
		return fmt.Errorf("profile %s: roster is empty", p.Name)
	}
	seen := make(map[string]struct{}, len(p.agents))
	for _, a := range p.agents {
		if err := a.validate(known); err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("profile %s: duplicate agent name %q", p.Name, a.Name)
		}
		seen[a.Name] = struct{}{}
	}
	return nil
}

func (a AgentSpec) validate(known map[string]struct{}) error {
	if err := a.checkName(); err != nil {
		return err
	}
	if len(a.Lenses) == 0 {
		return fmt.Errorf("agent %s: no lenses", a.Name)
	}
	for _, l := range a.Lenses {
		if _, ok := known[l]; !ok {
			return fmt.Errorf("agent %s: unknown lens %q", a.Name, l)
		}
	}
	if _, err := a.resolveColor(); err != nil {
		return fmt.Errorf("agent %s: %w", a.Name, err)
	}
	return nil
}

// checkName rejects a name that cannot become one path component. The archive turns it into
// agents/<name>.jsonl and prompts/agents/<name>.md, so a separator would write outside the round.
func (a AgentSpec) checkName() error {
	switch {
	case a.Name == "":
		return errors.New("roster entry has no name")
	case filepath.IsAbs(a.Name):
		return fmt.Errorf("agent %q is an absolute path", a.Name)
	case strings.ContainsAny(a.Name, `/\`):
		return fmt.Errorf("agent %q contains a path separator", a.Name)
	case strings.Contains(a.Name, ".."):
		return fmt.Errorf("agent %q references a parent directory", a.Name)
	case strings.HasPrefix(a.Name, "."):
		return fmt.Errorf("agent %q starts with a dot", a.Name)
	}
	return nil
}

// Paint wraps s in the agent's resolved color. Both renderers call it, so the TUI and the plain --no-tui
// output show one agent in one color. It emits raw SGR rather than going through lipgloss: a nested
// lipgloss render ends in a full reset that kills the enclosing pane's background.
func (a AgentSpec) Paint(s string) string {
	seq := a.sgr()
	if seq == "" || s == "" {
		return s
	}
	return seq + s + ansiDefaultFg
}

// sgr renders the resolved color as a foreground sequence. An index picks the color out of the
// reader's own terminal theme; a hex value asks for that exact shade and ignores the theme.
func (a AgentSpec) sgr() string {
	if strings.HasPrefix(a.Color, "#") {
		rgb, err := strconv.ParseUint(a.Color[1:], 16, 32)
		if err != nil {
			return ""
		}
		return "\x1b[38;2;" + strconv.FormatUint(rgb>>16&0xff, 10) + ";" +
			strconv.FormatUint(rgb>>8&0xff, 10) + ";" + strconv.FormatUint(rgb&0xff, 10) + "m"
	}
	idx, err := strconv.Atoi(a.Color)
	if err != nil || idx < 0 || idx > 15 {
		return ""
	}
	if idx < 8 {
		return "\x1b[3" + strconv.Itoa(idx) + "m"
	}
	return "\x1b[9" + strconv.Itoa(idx-8) + "m"
}

func (a AgentSpec) resolveColor() (string, error) {
	if a.ColorName == "" {
		return "", nil
	}
	if idx, ok := ansiColors[a.ColorName]; ok {
		return strconv.Itoa(idx), nil
	}
	if hexColor.MatchString(a.ColorName) {
		return a.ColorName, nil
	}
	return "", fmt.Errorf("invalid color %q, want an ANSI-16 name or #RRGGBB", a.ColorName)
}

func parseProfile(name string, meta, body []byte) (*Profile, error) {
	var fm profileYAML
	text, err := parseFile(meta, body, &fm)
	if err != nil {
		return nil, err
	}

	base, err := parseRunner(fm.Model)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	if base.Executor == "" {
		base.Executor = executorClaude
	}

	p := &Profile{doc: doc{Description: fm.Description, Body: text}, Name: name, runner: base}
	p.agents = make([]AgentSpec, 0, len(fm.Agents))
	for _, a := range fm.Agents {
		run, agentErr := parseRunner(a.Model)
		if agentErr != nil {
			return nil, fmt.Errorf("profile %s: agent %s: %w", name, a.Name, agentErr)
		}
		run = run.or(base)
		p.agents = append(p.agents, AgentSpec{Name: a.Name, Lenses: a.Lenses, ColorName: a.Color,
			Executor: run.Executor, Model: run.Model, Effort: run.Effort})
	}

	p.stages = make(map[string]Runner, len(fm.Stages))
	for stage, over := range fm.Stages {
		run, stageErr := parseRunner(over)
		if stageErr != nil {
			return nil, fmt.Errorf("profile %s: stage %s: %w", name, stage, stageErr)
		}
		// the key is present, so an empty value states nothing while still counting as an override,
		// skipping the stage file's own runner without saying so
		if run.Executor == "" {
			return nil, fmt.Errorf("profile %s: stage %s: empty runner, want %s",
				name, stage, "<binary>[/<model>][:<effort>]")
		}
		p.stages[stage] = run
	}
	return p, nil
}

func parseStage(name string, meta, body []byte) (*Stage, error) {
	var fm stageYAML
	text, err := parseFile(meta, body, &fm)
	if err != nil {
		return nil, err
	}

	run, err := parseRunner(fm.Model)
	if err != nil {
		return nil, fmt.Errorf("stage %s: %w", name, err)
	}
	st := &Stage{doc: doc{Description: fm.Description, Body: text}, Name: name, runner: run,
		Executor: run.Executor, Model: run.Model, Effort: run.Effort}
	return st, nil
}

func parseLens(meta, body []byte) (doc, error) {
	var fm lensYAML
	text, err := parseFile(meta, body, &fm)
	if err != nil {
		return doc{}, err
	}
	return doc{Description: fm.Description, Body: text}, nil
}
