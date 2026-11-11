package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/umputun/revmux/app/archive"
	"github.com/umputun/revmux/app/finding"
	"github.com/umputun/revmux/app/pipeline"
	"github.com/umputun/revmux/app/prompt"
	"github.com/umputun/revmux/app/task"
)

// manifest is what makes a finished run auditable: which roster ran, what each agent actually
// executed, which prompt file won each precedence race and what it contained, and how long each stage
// took. None of it is recoverable from the report — two rounds of one task can use different lens
// text, and nothing can recompute a stage duration after the fact.
type manifest struct {
	Task       string               `json:"task"`
	Run        string               `json:"run"`
	Profile    string               `json:"profile"`
	ScopePath  string               `json:"scope_path"`
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt time.Time            `json:"finished_at"`
	DurationMS int64                `json:"duration_ms"`
	Tokens     int                  `json:"tokens"`
	Agents     []finding.SourceStat `json:"agents"`
	Degraded   []string             `json:"degraded"`
	Prompts    []prompt.FileOrigin  `json:"prompts"`
	Stages     []finding.StageRun   `json:"stages"`
}

// archiveRun writes the run's own artifacts into the archive, before the report reaches either the
// findings browser or stdout. The archived report is the filtered one, byte for byte what the caller was
// shown; the unfiltered set is still in stages/3-verified.json.
func (o runOpts) archiveRun(a *archive.Archive, cfg pipeline.Config, rep finding.Report) error {
	if err := o.writeArtifact(a, task.ReportFile, rep.Markdown); err != nil {
		return err
	}
	if err := o.writeArtifact(a, task.FindingsFile, rep.JSON); err != nil {
		return err
	}
	return o.writeArtifact(a, task.ManifestFile, o.manifest(cfg, rep).write)
}

// materializeProfile copies a project-level profile.md into the round; a round carrying a non-empty
// profile needs none of this, since that file already sits inside the archive.
//
// It runs early in review because two readers need the bytes: the agents through {{PROFILE}}, and the
// TUI's input snapshot, taken when the renderer is built.
func (o runOpts) materializeProfile(a *archive.Archive, rc reviewContext) error {
	if rc.ProfileSource == "" {
		return nil
	}
	body, err := os.ReadFile(rc.ProfileSource)
	if err != nil {
		return fmt.Errorf("read project profile %s: %w", rc.ProfileSource, err)
	}
	return o.writeArtifact(a, task.ProfileSnapshotFile, func(w io.Writer) error {
		if _, writeErr := w.Write(body); writeErr != nil {
			return fmt.Errorf("write profile snapshot: %w", writeErr)
		}
		return nil
	})
}

func (o runOpts) writeArtifact(a *archive.Archive, name string, render func(io.Writer) error) error {
	w, err := a.Writer(name)
	if err != nil {
		return fmt.Errorf("archive %s: %w", name, err)
	}
	if err := render(w); err != nil {
		_ = w.Close()
		return fmt.Errorf("archive %s: %w", name, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close archived %s: %w", name, err)
	}
	return nil
}

// manifest assembles the record from the resolved roster rather than from the report alone: the roster
// is what was asked for and is complete even for an agent that never launched, while the report
// carries what each process actually did.
func (o runOpts) manifest(cfg pipeline.Config, rep finding.Report) manifest {
	ran := make(map[string]finding.SourceStat, len(rep.Sources.Agents))
	for _, a := range rep.Sources.Agents {
		ran[a.Name] = a
	}

	agents := make([]finding.SourceStat, 0, len(cfg.Roster))
	for _, spec := range cfg.Roster {
		stat := finding.SourceStat{
			Name: spec.Name, Lenses: spec.Lenses, Executor: spec.Executor,
			RequestedModel: spec.Model, Effort: spec.Effort,
		}
		if got, ok := ran[spec.Name]; ok {
			stat.ActualModel, stat.Tokens = got.ActualModel, got.Tokens
			stat.Raised, stat.Degraded = got.Raised, got.Degraded
		}
		agents = append(agents, stat)
	}

	return manifest{
		Task: cfg.Task, Run: cfg.Run, Profile: cfg.Profile.Name, ScopePath: cfg.ScopePath,
		StartedAt: rep.Stats.StartedAt, FinishedAt: rep.Stats.FinishedAt,
		DurationMS: rep.Stats.DurationMS, Tokens: rep.Stats.Tokens,
		Agents: agents, Degraded: rep.Sources.DegradedSources,
		Prompts: cfg.Set.Provenance(), Stages: rep.Stats.Stages,
	}
}

func (m manifest) write(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	return nil
}
