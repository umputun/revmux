package main

import (
	"fmt"

	"github.com/umputun/revmux/app/archive"
)

// statsCmd is the `revmux stats` subcommand. It only records the selection: go-flags calls Execute from
// inside parseArgs, before the injected writers exist, so the corpus is read and reported by run.
type statsCmd struct {
	opts *options
}

// Execute records that the stats command was selected. Reading the corpus here would write through the
// real os.Stdout and leave nothing for a test to capture.
func (c *statsCmd) Execute([]string) error { //nolint:unparam // the signature is flags.Commander's
	c.opts.showStats = true
	return nil
}

// writeStats aggregates every round that ran under the tasks root and prints it as JSON, one of the
// carve-outs in "stdout belongs to the report". The query is built from --tasks-dir and --task, the flags
// a review already carries: a second --task on the subcommand would land in a different field.
func (o runOpts) writeStats() error {
	corpus, err := archive.CollectStats(archive.StatsQuery{TasksDir: o.opts.TasksDir, Task: o.opts.Task})
	if err != nil {
		return fmt.Errorf("collect stats: %w", err)
	}
	return o.writeJSON(corpus, "stats")
}
