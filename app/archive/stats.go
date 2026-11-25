package archive

import "github.com/umputun/revmux/app/finding"

// Corpus is every task's review record under one tasks root. It is read back out of what revmux itself
// wrote, so every number below names the artifact it came from: the same count differs by artifact, and a
// reflection agent acting on the wrong one drops a working agent.
type Corpus struct {
	Tasks  []taskStats `json:"tasks"`
	Totals taskStats   `json:"totals"`
}

// taskStats is one task's rounds folded together. ID is empty on Corpus.Totals, which is every task at once.
type taskStats struct {
	ID string `json:"id,omitempty"`

	// Description is this task's task.md, from the same task.Load `revmux config` reports it with. Empty
	// on Corpus.Totals, and for a task whose task.md is absent or will not parse: `revmux config` is
	// where a parse failure is named.
	Description string `json:"description,omitempty"`

	// SizeMB is what this task occupies on disk, every round and the caller's own input/ alike, summed
	// from file sizes rather than block counts so two filesystems report the same task the same. It is
	// the only number here not read back out of an artifact's content.
	SizeMB float64 `json:"size_mb"`

	// sizeBytes is what SizeMB is rounded from, kept so the totals fold exact byte counts: adding rounded
	// megabytes drifts by up to a tenth per task, against a threshold the user set.
	sizeBytes int64

	// LastRun is the finished_at of the most recent round's manifest, as a date — when the task was last
	// reviewed rather than when anything last touched the directory. Empty when no round carries one.
	LastRun string `json:"last_run,omitempty"`

	// Rounds is the rounds these numbers were read from: task.HasRun accepts it and its artifacts
	// decoded. It is the denominator of everything beside it.
	Rounds int `json:"rounds"`

	// Skipped is one entry per round left out of Rounds because its artifacts would not decode, each
	// naming the artifact and why. A count that shrank silently reads as a corpus that is simply smaller.
	Skipped []string `json:"skipped"`

	Agents []agentStats `json:"agents"`
	Lenses []lensStats  `json:"lenses"`
	Stages []stageFlow  `json:"stages"`
}

// agentStats is what one roster entry produced.
type agentStats struct {
	// Name is the agent's roster name, read from sources.agents[] of a stage snapshot.
	Name string `json:"name"`

	// Raised is findings in stages/1-found.json naming this agent in sources.
	Raised int `json:"raised"`

	// Survived is findings naming this agent in the round's last stage snapshot, across that report's four
	// survivor arrays, Rejected excluded. That is stages/3-verified.json for a full pipeline, the stage before it for a run
	// that skipped verification. findings.json is never read for it: that is the filtered report.
	Survived int `json:"survived"`

	// Corroborated is the Survived subset whose sources names more than one agent.
	Corroborated int `json:"corroborated"`

	// DegradedRounds is rounds whose stage snapshot names this agent in SourceStatus.DegradedNames, which
	// merges the explicit degraded list with the per-agent flags because the two disagree.
	DegradedRounds int `json:"degraded_rounds"`

	// Retries is agent_retried entries in events.jsonl naming this agent. No stage snapshot records a
	// relaunch, so the event log is the only artifact that carries this.
	Retries int `json:"retries"`

	// Tokens is summed from sources.agents[].tokens of a stage snapshot.
	Tokens int `json:"tokens"`
}

// lensStats is what one lens raised and what became of it. A per-lens number is only as good as its
// Ambiguous share, so the two are quoted together.
type lensStats struct {
	// Name is the lens name as it appears in a finding's lenses array.
	Name string `json:"name"`

	// Raised is findings in stages/1-found.json naming this lens. Stage 1 is the only place the number
	// means anything: after synthesis lenses is a union across merged findings from different agents.
	Raised int `json:"raised"`

	// Ambiguous is the Raised subset whose finding named exactly its agent's whole lens set while that
	// agent carried more than one — the shape find produces when the model named no valid lens. It
	// over-counts a model that genuinely named every lens it carried; the archive cannot tell them apart.
	Ambiguous int `json:"ambiguous"`

	// Verdicts counts this lens's surviving findings by verdict, from the stage snapshot Survived is read
	// from. A finding that never went through verification carries no verdict and counts as unverified.
	Verdicts map[finding.Verdict]int `json:"verdicts"`
}

// stageFlow is how many findings went into one stage and how many came out. Name is the stage that
// produced Out; the find stage has no entry. Each count is the union of a report's four survivor arrays, so a
// finding moved into immaterial or pre-existing leaves the total unchanged — which is why Reclassified
// and Refined exist. Measured over one corpus, verify dropped 2 of 150 while lowering 21 severities.
type stageFlow struct {
	Name string `json:"name"`
	In   int    `json:"in"`
	Out  int    `json:"out"`

	// Reclassified is findings this stage moved into the immaterial or pre-existing arrays. They leave
	// the actionable list without leaving the report, so they are invisible in Out.
	Reclassified int `json:"reclassified,omitempty"`

	// Refined is findings this stage kept and rewrote.
	Refined int `json:"refined,omitempty"`
}

// StatsQuery selects what CollectStats reads. Two adjacent string parameters would be a swap hazard, and
// swapping these silently reads the wrong directory.
type StatsQuery struct {
	TasksDir string
	Task     string // empty means every task under TasksDir
}
