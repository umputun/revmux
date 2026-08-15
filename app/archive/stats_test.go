package archive

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/finding"
)

// the totals fold both size and date across tasks — only the id and the description are per-task, since no
// one description covers a corpus.
func TestCorpus_JSON(t *testing.T) {
	c := Corpus{
		Tasks: []taskStats{{
			ID:          "pr-123",
			Description: "the round-layout rework",
			SizeMB:      6.6,
			LastRun:     "2026-07-27",
			Rounds:      2,
			Agents: []agentStats{{
				Name: "bugs+impl", Raised: 5, Survived: 4, Corroborated: 3,
				DegradedRounds: 1, Retries: 2, Tokens: 4986379,
			}},
			Lenses: []lensStats{{
				Name: "bugs", Raised: 3, Ambiguous: 2,
				Verdicts: map[finding.Verdict]int{finding.Confirmed: 1, finding.Unverified: 2},
			}},
			Stages: []stageFlow{{Name: "synthesis", In: 26, Out: 17}},
		}},
		Totals: taskStats{Rounds: 2, SizeMB: 6.6, LastRun: "2026-07-27"},
	}

	data, err := json.Marshal(c)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	task := got["tasks"].([]any)[0].(map[string]any)

	t.Run("every field a caller reads is named in snake case", func(t *testing.T) {
		assert.Equal(t, []string{"agents", "description", "id", "last_run", "lenses", "rounds", "size_mb",
			"skipped", "stages"}, keys(task))
		assert.Equal(t, "pr-123", task["id"])
		assert.Equal(t, "the round-layout rework", task["description"], "what a caller weighs before removing it")
		assert.InDelta(t, 6.6, task["size_mb"], 0.01)
		assert.Equal(t, "2026-07-27", task["last_run"])

		assert.Equal(t, []string{"corroborated", "degraded_rounds", "name", "raised", "retries", "survived", "tokens"},
			keys(task["agents"].([]any)[0].(map[string]any)))
		assert.Equal(t, []string{"ambiguous", "name", "raised", "verdicts"},
			keys(task["lenses"].([]any)[0].(map[string]any)))
		assert.Equal(t, []string{"in", "name", "out"}, keys(task["stages"].([]any)[0].(map[string]any)))
	})

	t.Run("verdict counts are keyed by the vocabulary a report carries", func(t *testing.T) {
		lens := task["lenses"].([]any)[0].(map[string]any)
		assert.Equal(t, map[string]any{"confirmed": 1.0, "unverified": 2.0}, lens["verdicts"])
	})

	t.Run("totals carry size and date but no id or description", func(t *testing.T) {
		totals := got["totals"].(map[string]any)
		assert.Equal(t, []string{"agents", "last_run", "lenses", "rounds", "size_mb", "skipped", "stages"},
			keys(totals))
		assert.InDelta(t, 6.6, totals["size_mb"], 0.01)
		assert.Equal(t, "2026-07-27", totals["last_run"], "the corpus's own last review")
	})
}

func keys(obj map[string]any) []string { return slices.Sorted(maps.Keys(obj)) }
