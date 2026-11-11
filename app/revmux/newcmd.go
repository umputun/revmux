package main

import (
	"fmt"

	"github.com/umputun/revmux/app/task"
)

// newCmd is the `revmux new` subcommand. It only records the selection: go-flags calls Execute from
// inside parseArgs, before the injected writers exist, so the round is created and reported by run.
type newCmd struct {
	opts *options
}

// Execute records that the new command was selected. Scaffolding here would write through the real
// os.Stdout and leave nothing for a test to capture.
func (c *newCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showNew = true
	return nil
}

// writeTaskPaths creates the round and prints every path the caller writes to as JSON, one of the
// carve-outs in "stdout belongs to the report". It is the only place revmux creates review context: the
// review path opens and never creates, so a typo'd --task errors rather than producing an empty task.
func (o runOpts) writeTaskPaths() error {
	paths, err := task.Scaffold(task.Round{TasksDir: o.opts.TasksDir, Task: o.opts.Task, Run: o.opts.Run})
	if err != nil {
		return fmt.Errorf("create round: %w", err)
	}
	return o.writeJSON(paths, "task paths")
}
