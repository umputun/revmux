// Package task owns the task-directory layout: the names every round is built from, the optional
// task.md metadata file, and the validation a caller-supplied name passes before it becomes a path
// component. Nothing here reads review context, and nothing task.md names is ever resolved — a branch
// or a base ref is reported back, never fetched.
package task

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/umputun/revmux/app/frontmatter"
)

// the task-directory layout every path in app/archive, app/pipeline and package main is joined from.
// Nothing outside this package spells one of these names.
const (
	InputDir     = "input"
	ScopeFile    = "scope.md"
	GoalFile     = "goal.md"
	ProfileFile  = "profile.md"
	ContextDir   = "context"
	ManifestFile = "manifest.json"
	FindingsFile = "findings.json"
	ReportFile   = "report.md"

	// metaFile describes the task rather than any one round, so it is the only entry at task level.
	metaFile = "task.md"
)

// EventsFile is the run's decision record: stalls, retries, degrades and stage transitions.
const EventsFile = "events.jsonl"

// AgentsDir holds one verbatim stream tee per agent.
const AgentsDir = "agents"

// AgentPromptDir and StagePromptDir hold composed prompts, split so a roster agent named after a
// stage cannot overwrite a stage prompt.
const (
	AgentPromptDir = "prompts/agents"
	StagePromptDir = "prompts/stages"
)

// ProfileSnapshotFile is the round's copy of a project-level profile.md, taken when the round carries
// no non-empty profile of its own. {{PROFILE}} expands to a path rather than to content, so a round
// pointed at the project file would carry no record of what calibrated it once that file changed.
const ProfileSnapshotFile = "prompts/input-profile.md"

// FoundFile, SynthesizedFile and VerifiedFile are the per-stage findings snapshots. Each is a
// finding.Report, so each carries the full source roster alongside that stage's findings.
const (
	FoundFile       = "stages/1-found.json"
	SynthesizedFile = "stages/2-synthesized.json"
	VerifiedFile    = "stages/3-verified.json"
)

// metaTemplate is the task.md a fresh task is scaffolded with. It ships fully commented out, so a task
// nobody described carries no metadata rather than a placeholder anchor that matches the wrong task.
const metaTemplate = `---
# every key is optional, and revmux only stores what is here: a branch or a base ref is reported back,
# never resolved and never fetched. Uncomment what applies.
#
# description: one line naming what this task reviews
# url: https://github.com/owner/repo/pull/123
# branch: feature/oauth
# base: 4ed3259
---

<!-- what this task covers, in prose. revmux reads the front matter above and never this. -->
`

// Meta is the task.md front matter, every key optional. Both yaml and json tags: it is parsed from front
// matter and marshaled into the `revmux config` payload, where an untagged field emits `URL` not `url`.
type Meta struct {
	Description string `yaml:"description" json:"description"`
	URL         string `yaml:"url" json:"url"`
	Branch      string `yaml:"branch" json:"branch"`
	Base        string `yaml:"base" json:"base"`
}

// Load reads the front matter of <dir>/task.md. An absent file carries no metadata and is not an
// error; a malformed one is, since a task matched on metadata that failed to parse is matched on
// nothing.
func Load(dir string) (Meta, error) {
	path := filepath.Join(dir, metaFile)
	data, err := os.ReadFile(path) //nolint:gosec // the path is the caller's own task directory
	if errors.Is(err, fs.ErrNotExist) {
		return Meta{}, nil
	}
	if err != nil {
		return Meta{}, fmt.Errorf("read %s: %w", path, err)
	}

	block, _, err := frontmatter.Split(data)
	if err != nil {
		return Meta{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(bytes.TrimSpace(block)) == 0 {
		return Meta{}, nil
	}

	var m Meta
	dec := yaml.NewDecoder(bytes.NewReader(block))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return Meta{}, fmt.Errorf("parse %s front matter: %w", path, err)
	}
	return m, nil
}

// CheckMarker is the single definition of what a round's manifest.json means: a regular file with content
// is a round that ran, an empty one is a claim its run never came back from, anything else is refused.
// fi must come from an Lstat — os.Root.Stat follows a link landing back inside the round.
func CheckMarker(path string, fi fs.FileInfo) (ran bool, err error) {
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file: revmux writes this run's record into it, and a link "+
			"there is followed by that write", path)
	}
	return fi.Size() > 0, nil
}

// HasRun reports whether dir is a round a review actually completed in. An empty marker is a round still
// open, and listing it would put the round being written into its own inventory.
func HasRun(dir string) bool {
	path := filepath.Join(dir, ManifestFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	ran, err := CheckMarker(path, fi)
	return err == nil && ran
}

// Rounds lists the rounds recorded under a task directory, in the lexical order os.ReadDir returns. A
// round is a directory HasRun accepts, so neither a directory a caller left there nor a round claimed by a
// run that never came back is one. An absent task directory has no rounds and is not an error.
func Rounds(taskDir string) ([]string, error) {
	entries, err := os.ReadDir(taskDir)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", taskDir, err)
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() || !HasRun(filepath.Join(taskDir, e.Name())) {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// CheckReclaim refuses re-using a round an interrupted run already wrote artifacts into: anything beside
// input/ and the marker means a second run would claim two runs' artifacts under one manifest. entries are
// that round's own entries, read through the caller's handle on it. Nothing is deleted to make such a
// round usable — the caller's own input/ copies across to a new round.
func CheckReclaim(dir string, entries []os.DirEntry) error {
	leftovers := []string{}
	for _, e := range entries {
		if e.Name() == InputDir || e.Name() == ManifestFile {
			continue
		}
		leftovers = append(leftovers, e.Name())
	}
	if len(leftovers) == 0 {
		return nil
	}
	return fmt.Errorf("round %s was claimed by a run that never came back and still holds what it wrote (%s): "+
		"re-using it would put two runs' artifacts under one %s, so open a new round instead — the %s/ you "+
		"wrote copies across, and nothing here deletes it", dir, strings.Join(leftovers, ", "), ManifestFile, InputDir)
}

// Round addresses one round: the tasks root, the task under it, and the round's own name. Scaffold and
// archive.New both take this rather than a joined path, so each anchors its chain at the tasks root.
type Round struct {
	TasksDir string
	Task     string
	Run      string
}

// Paths is every path a caller writes to, plus the fields Scaffold actually created. Reporting them is
// what keeps the layout revmux's own detail rather than a shape every caller reproduces.
type Paths struct {
	TaskDir  string   `json:"task_dir"`
	TaskFile string   `json:"task_file"`
	RoundDir string   `json:"round_dir"`
	InputDir string   `json:"input_dir"`
	Scope    string   `json:"scope"`
	Goal     string   `json:"goal"`
	Profile  string   `json:"profile"`
	Context  string   `json:"context"`
	Created  []string `json:"created"`
}

// created names one Paths field in the JSON `revmux new` prints. Each value is a json tag on Paths.
type created string

const (
	createdTaskDir  created = "task_dir"
	createdTaskFile created = "task_file"
	createdRoundDir created = "round_dir"
	createdInputDir created = "input_dir"
)

// Scaffold creates the round <TasksDir>/<Task>/<Run> with its input/, and the task directory and task.md
// above it when absent. It is idempotent at task level, and refuses a round that has already run.
//
// It walks down from the tasks root as nested os.Roots and writes through those handles rather than by
// path: a check on a path and the write that follows it are two operations, and a directory swapped for
// a symlink in between is followed.
func Scaffold(opts Round) (Paths, error) {
	if opts.TasksDir == "" {
		return Paths{}, errors.New("tasks directory is empty")
	}
	if err := CheckName("--task", opts.Task); err != nil {
		return Paths{}, err
	}
	if err := CheckRoundName("--run", opts.Run); err != nil {
		return Paths{}, err
	}

	root, err := filepath.Abs(opts.TasksDir)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve tasks directory %q: %w", opts.TasksDir, err)
	}
	dir := filepath.Join(root, opts.Task)
	round := filepath.Join(dir, opts.Run)
	input := filepath.Join(round, InputDir)
	p := Paths{
		TaskDir: dir, TaskFile: filepath.Join(dir, metaFile), RoundDir: round, InputDir: input,
		Scope:   filepath.Join(input, ScopeFile),
		Goal:    filepath.Join(input, GoalFile),
		Profile: filepath.Join(input, ProfileFile),
		Context: filepath.Join(input, ContextDir),
		Created: []string{},
	}

	// the tasks root itself is created when it is absent — a first `revmux new` on a clean checkout
	// materializes ./.revmux/tasks/ — and it is not one of the layout fields Created names
	if err = os.MkdirAll(root, 0o750); err != nil {
		return Paths{}, fmt.Errorf("create %s: %w", root, err)
	}
	tasksRoot, err := os.OpenRoot(root)
	if err != nil {
		return Paths{}, fmt.Errorf("open tasks directory %s: %w", root, err)
	}
	defer tasksRoot.Close()

	if buildErr := p.build(tasksRoot, opts); buildErr != nil {
		return Paths{}, buildErr
	}
	return p, nil
}

// build walks the layout down from the tasks root, creating what is missing and refusing what cannot be
// written into. It is split off Scaffold so every handle it opens closes on one return path.
func (p *Paths) build(tasksRoot *os.Root, opts Round) error {
	taskRoot, err := p.openTask(tasksRoot, opts.Task)
	if err != nil {
		return err
	}
	defer taskRoot.Close()

	if metaErr := p.writeMeta(taskRoot); metaErr != nil {
		return metaErr
	}

	roundRoot, err := p.openRound(taskRoot, opts.Run)
	if err != nil {
		return err
	}
	defer roundRoot.Close()

	if err := p.reclaimable(roundRoot); err != nil {
		return err
	}
	return p.makeInput(roundRoot)
}

// openTask creates the task directory when it is absent and opens it as a root of its own. A task
// directory may legitimately be a relative symlink to a sibling task, which os.Root resolves inside the
// tasks root; one pointing anywhere else is refused at this hop rather than written through.
func (p *Paths) openTask(tasksRoot *os.Root, name string) (*os.Root, error) {
	err := tasksRoot.Mkdir(name, 0o750)
	switch {
	case err == nil:
		p.Created = append(p.Created, string(createdTaskDir))
	case !errors.Is(err, fs.ErrExist):
		return nil, fmt.Errorf("create %s: %w", p.TaskDir, err)
	default:
		fi, statErr := tasksRoot.Stat(name)
		if statErr != nil {
			return nil, fmt.Errorf("open task directory %s inside the tasks root %s: %w",
				p.TaskDir, filepath.Dir(p.TaskDir), statErr)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", p.TaskDir)
		}
	}

	r, err := tasksRoot.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open task directory %s inside the tasks root %s: %w",
			p.TaskDir, filepath.Dir(p.TaskDir), err)
	}
	return r, nil
}

// openRound creates the round when it is absent and opens it as a root, refusing a symlink outright: a
// link landing back inside the tasks root is contained and still aliases another round's input/. The
// entry is read again against the open handle, since looking and opening are two operations.
func (p *Paths) openRound(taskRoot *os.Root, name string) (*os.Root, error) {
	err := taskRoot.Mkdir(name, 0o750)
	switch {
	case err == nil:
		p.Created = append(p.Created, string(createdRoundDir))
	case !errors.Is(err, fs.ErrExist):
		return nil, fmt.Errorf("create %s: %w", p.RoundDir, err)
	}
	if dirErr := p.checkRealDir(taskRoot, name, p.RoundDir); dirErr != nil {
		return nil, dirErr
	}

	r, err := taskRoot.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open round directory %s: %w", p.RoundDir, err)
	}
	entry, err := taskRoot.Lstat(name)
	if err != nil {
		r.Close() //nolint:gosec // the round is refused, and nothing was written through it
		return nil, fmt.Errorf("stat %s: %w", p.RoundDir, err)
	}
	got, err := r.Stat(".")
	if err != nil {
		r.Close() //nolint:gosec // same
		return nil, fmt.Errorf("stat open %s: %w", p.RoundDir, err)
	}
	if !os.SameFile(entry, got) {
		r.Close() //nolint:gosec // same
		return nil, fmt.Errorf("%s was replaced while it was being opened", p.RoundDir)
	}
	return r, nil
}

// makeInput creates the round's input/ when it is absent. A symlink there aliases this round's whole
// review context onto another directory, so the review path refuses one too.
func (p *Paths) makeInput(roundRoot *os.Root) error {
	err := roundRoot.Mkdir(InputDir, 0o750)
	if err == nil {
		p.Created = append(p.Created, string(createdInputDir))
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", p.InputDir, err)
	}
	return p.checkRealDir(roundRoot, InputDir, p.InputDir)
}

// checkRealDir refuses anything holding a layout name that is not a directory revmux may write into: a
// symlink, however contained, and a plain file. path names the entry for the message.
func (p *Paths) checkRealDir(parent *os.Root, name, path string) error {
	fi, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink, and revmux writes only a real directory it was handed", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// List names every task under root, in the lexical order os.ReadDir returns. An absent tasks root has no
// tasks and is not an error. Which entries are tasks is decided through an os.Root, so this reports
// exactly what archive.New can open. It is the single enumerator `revmux config` and `revmux stats` read.
func List(root string) ([]string, error) {
	out := []string{}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("read %s: %w", root, err)
	}
	tasksRoot, err := os.OpenRoot(root)
	if err != nil {
		return out, fmt.Errorf("open %s: %w", root, err)
	}
	defer tasksRoot.Close()

	for _, e := range entries {
		if fi, statErr := tasksRoot.Stat(e.Name()); statErr != nil || !fi.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// CheckContained verifies a path resolves to somewhere inside the tasks root, which the lexical check on
// a name cannot do. It applies where a path string is all there is; nothing that writes uses it, since a
// resolve followed by a write is two operations and the rename in between is the whole attack.
func CheckContained(root, path string) error {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve tasks directory %s: %w", root, err)
	}
	if !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("%s escapes the tasks root %s", realPath, realRoot)
	}
	return nil
}

// reclaimable refuses a round this run may not take over, reading it through the round's own handle the
// same way archive.New's claim does. The entry is read with Lstat: os.Root.Stat follows a link landing
// back inside the round, so a manifest.json aliasing goal.md would read as its size.
func (p *Paths) reclaimable(roundRoot *os.Root) error {
	path := filepath.Join(p.RoundDir, ManifestFile)
	fi, err := roundRoot.Lstat(ManifestFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	ran, err := CheckMarker(path, fi)
	if err != nil {
		return err
	}
	if ran {
		return fmt.Errorf("round %s has already run, %s is in place: a round that went badly is exactly "+
			"the one a later reflection agent reads, so it is never reused", p.RoundDir, ManifestFile)
	}
	entries, err := fs.ReadDir(roundRoot.FS(), ".")
	if err != nil {
		return fmt.Errorf("read %s: %w", p.RoundDir, err)
	}
	return CheckReclaim(p.RoundDir, entries)
}

// writeMeta materializes the commented-out task.md template, leaving one already there untouched. It is
// the one file Scaffold writes rather than a directory it creates: the entry is read with Lstat, since a
// dangling link is a link and not a missing file, and the create is exclusive through the task's handle.
func (p *Paths) writeMeta(taskRoot *os.Root) error {
	fi, err := taskRoot.Lstat(metaFile)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink, and revmux writes only a real %s inside the tasks root",
				p.TaskFile, metaFile)
		}
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory, want the task's own %s", p.TaskFile, metaFile)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", p.TaskFile, err)
	}

	f, err := taskRoot.OpenFile(metaFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", p.TaskFile, err)
	}
	if _, err := f.WriteString(metaTemplate); err != nil {
		_ = f.Close() // the write already failed, and its error is the one worth returning
		return fmt.Errorf("write %s: %w", p.TaskFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", p.TaskFile, err)
	}
	p.Created = append(p.Created, string(createdTaskFile))
	return nil
}

// CheckName rejects a caller-supplied name before it becomes one path component, and is the single
// definition of that rule. what is the flag the name came from, so one rule speaks with one vocabulary.
func CheckName(what, name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%s is empty", what)
	case filepath.IsAbs(name):
		return fmt.Errorf("%s %q is absolute", what, name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%s %q contains a path separator", what, name)
	case strings.Contains(name, ".."):
		return fmt.Errorf("%s %q references a parent directory", what, name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("%s %q starts with a dot", what, name)
	}
	return nil
}

// CheckRoundName is CheckName plus the one entry a round may not be named after: a round shares the task
// directory's namespace with task.md, and one carrying that name is unreachable as a round.
func CheckRoundName(what, name string) error {
	if err := CheckName(what, name); err != nil {
		return err
	}
	if name == metaFile {
		return fmt.Errorf("%s %q is reserved: the task directory keeps %s beside its rounds, and a round "+
			"named after it is read as the task's own metadata", what, name, metaFile)
	}
	return nil
}
