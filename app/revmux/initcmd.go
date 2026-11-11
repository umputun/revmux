package main

import (
	"fmt"
	"io"

	"github.com/umputun/revmux/app/prompt"
)

// initCmd is the `revmux init` subcommand. It only records the selection: go-flags calls Execute from
// inside parseArgs, before the injected writers and the loaded prompt tree exist, so the tree is
// materialized and reported by run.
type initCmd struct {
	opts *options
}

// initPaths is what `revmux init` emits — the local tree a user edits, reported rather than described,
// so no caller composes a path into it.
type initPaths struct {
	Dir    string     `json:"dir"`
	Config string     `json:"config"`
	Files  []initFile `json:"files"`
}

// initFile is one prompt file under ./.revmux/: where it is, which layer supplied its content, and
// whether this call wrote it. One already there is reported and left exactly as it is.
type initFile struct {
	Path    string `json:"path"`
	Layer   string `json:"layer"`
	Created bool   `json:"created"`
}

// Execute records that the init command was selected. Materializing here would write through the real
// os.Stdout and leave nothing for a test to capture.
func (c *initCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showInit = true
	return nil
}

// writeInitPaths materializes ./.revmux/ and prints what is in it as JSON, one of the carve-outs in
// "stdout belongs to the report". The prompt tree is loaded directly rather than through promptSet: a
// caller initializing a project has not chosen a --profile yet.
func (o runOpts) writeInitPaths() error {
	set, err := prompt.Load(o.opts.promptOpts())
	if err != nil {
		return fmt.Errorf("load prompts: %w", err)
	}
	dir, err := projectDir()
	if err != nil {
		return err
	}
	// one handle on ./.revmux/ for everything init writes into it, config included: a second writer is a
	// second chance for one of them to be the one that resolves a path instead of holding a root
	w, err := newTreeWriter(dir)
	if err != nil {
		return err
	}
	defer w.close() //nolint:errcheck // nothing is written after this point

	// settings ship commented out so an upgrade can still overwrite them, which is the whole reason the
	// config is not materialized the way the prompt files are; its prose report is not the payload, and
	// the path it reports back is what the payload carries rather than a second composition of it
	cfg, err := o.opts.initConfig(w, io.Discard)
	if err != nil {
		return err
	}
	files, err := o.materializePrompts(set, w)
	if err != nil {
		return err
	}
	return o.writeJSON(initPaths{Dir: dir, Config: cfg, Files: files}, "init paths")
}

// materializePrompts writes what each prompt file resolved to through w, leaving one already there
// untouched. The bytes are the winning file's own, front matter included: a stripped write produces a
// tree the next prompt.Load rejects. A file that cannot be written stops the run with the path named,
// and running init again completes exactly what the first call did not.
func (o runOpts) materializePrompts(set *prompt.Set, w *treeWriter) ([]initFile, error) {
	origins := set.Provenance()
	out := make([]initFile, 0, len(origins))
	for _, org := range origins {
		data, contentErr := set.Content(org.Path)
		if contentErr != nil {
			return nil, fmt.Errorf("resolve %s: %w", org.Path, contentErr)
		}
		created, writeErr := w.write(org.Path, data)
		if writeErr != nil {
			return nil, writeErr
		}
		out = append(out, initFile{Path: w.dest(org.Path), Layer: org.Layer, Created: created})
	}
	return out, nil
}
