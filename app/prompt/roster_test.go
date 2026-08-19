package prompt

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadProfile builds a one-profile tree over the standard lens set and returns the parsed profile.
func loadProfile(t *testing.T, frontMatter string) (*Set, *Profile) {
	t.Helper()
	project := writeTree(t, t.TempDir(), map[string]string{
		"prompts/profiles/custom.md": "---\n" + frontMatter + "---\nprofile body",
	})
	set, err := Load(LoadOpts{ProjectDir: project})
	require.NoError(t, err)
	p, err := set.Profile("custom")
	require.NoError(t, err)
	return set, p
}

func TestParseProfile_DefaultsAndOverrides(t *testing.T) {
	_, p := loadProfile(t, `description: a custom roster
model: claude/opus:high
agents:
  - {name: inherits, lenses: [bugs]}
  - {name: overrides, lenses: [bugs, adversarial], model: codex/gpt-5.6-sol:xhigh}
`)

	assert.Equal(t, "a custom roster", p.Description)
	assert.Equal(t, "profile body", p.Body)

	specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}, "adversarial": {}})
	require.NoError(t, err)
	require.Len(t, specs, 2)

	assert.Equal(t, AgentSpec{Name: "inherits", Lenses: []string{"bugs"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6"}, specs[0])
	assert.Equal(t, AgentSpec{Name: "overrides", Lenses: []string{"bugs", "adversarial"}, Executor: "codex",
		Model: "gpt-5.6-sol", Effort: "xhigh", Color: "5"}, specs[1])
}

func TestProfile_ExecutorDefault(t *testing.T) {
	const codexProfile = `model: codex/gpt-5.6-sol:high
agents:
  - {name: inherits, lenses: [bugs]}
  - {name: explicit, lenses: [impl], model: claude/opus}
stages:
  verify: claude/opus
`

	t.Run("a roster entry omitting a model takes the profile's", func(t *testing.T) {
		_, p := loadProfile(t, codexProfile)
		specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}, "impl": {}})
		require.NoError(t, err)
		assert.Equal(t, "codex", specs[0].Executor)
		assert.Equal(t, "claude", specs[1].Executor, "a per-entry model still wins")
	})

	// the override inherits the profile's model, so hardcoding claude built a claude agent asked for a
	// codex model — and on a host with no claude the only finder could not launch
	t.Run("the lenses override inherits it rather than forcing claude", func(t *testing.T) {
		_, p := loadProfile(t, codexProfile)
		specs, err := p.Roster([]string{"bugs"}, map[string]struct{}{"bugs": {}, "impl": {}})
		require.NoError(t, err)
		require.Len(t, specs, 1)
		assert.Equal(t, "lenses", specs[0].Name)
		assert.Equal(t, "codex", specs[0].Executor)
		assert.Equal(t, "gpt-5.6-sol", specs[0].Model, "the executor and the model must come from one place")
	})

	t.Run("a stage takes the profile's runner, named or not", func(t *testing.T) {
		set, p := loadProfile(t, codexProfile)
		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "codex", st.Executor)
		assert.Equal(t, "gpt-5.6-sol", st.Model)

		st, err = p.Stage(set, "verify")
		require.NoError(t, err)
		assert.Equal(t, "claude", st.Executor, "a per-stage model still wins")
	})

	// a profile's model is the review's model, so a stage follows it with no stages: block to write —
	// this is what lets codex-only be one line rather than one line plus a two-entry block
	t.Run("a stage follows the profile with no stages block at all", func(t *testing.T) {
		set, p := loadProfile(t, "model: codex/gpt-5.6-sol\nagents:\n  - {name: a, lenses: [bugs]}\n")
		for _, name := range []string{"synthesis", "verify"} {
			st, err := p.Stage(set, name)
			require.NoError(t, err, name)
			assert.Equal(t, "codex", st.Executor, name)
			assert.Equal(t, "gpt-5.6-sol", st.Model, name)
		}
	})

	t.Run("omitted, the default is claude", func(t *testing.T) {
		_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n")
		specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
		require.NoError(t, err)
		assert.Equal(t, "claude", specs[0].Executor)
	})
}

func TestProfile_Stage(t *testing.T) {
	t.Run("no override and no profile model resolves the default binary", func(t *testing.T) {
		set, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n")
		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "claude", st.Executor)
		assert.Empty(t, st.Model, "a shipped stage file names no model; it runs whatever the profile does")
	})

	t.Run("an override replaces the triple rather than merging into it", func(t *testing.T) {
		set, p := loadProfile(t, `agents:
  - {name: a, lenses: [bugs]}
stages:
  synthesis: codex/gpt-5.6-sol:xhigh
`)
		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "codex", st.Executor)
		assert.Equal(t, "gpt-5.6-sol", st.Model)
		assert.Equal(t, "xhigh", st.Effort)
		assert.Equal(t, "merges every source's findings, dedupes them, boosts corroboration and drops weak singletons",
			st.Description, "the body and its metadata are the stage file's, only the runner is the profile's")
	})

	t.Run("an omitted model and effort fall back to the profile's own defaults", func(t *testing.T) {
		set, p := loadProfile(t, `model: codex/gpt-5.6-sol:high
agents:
  - {name: a, lenses: [bugs]}
stages:
  synthesis: codex
`)
		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "codex", st.Executor)
		assert.Equal(t, "gpt-5.6-sol", st.Model)
		assert.Equal(t, "high", st.Effort)
	})

	// the stage file's model must not survive an override naming a different binary: codex launched on
	// opus is the pairing the one-string grammar exists to make unwritable. The shipped verify.md names
	// no runner, so this needs a tree that carries one, or it asserts nothing about the case in its name.
	t.Run("a stage file's model does not survive an override naming another binary", func(t *testing.T) {
		project := writeTree(t, t.TempDir(), map[string]string{
			"prompts/verify.md": "---\ndescription: d\nmodel: claude/opus:high\n---\nbody",
			"prompts/profiles/custom.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n" +
				"stages:\n  verify: codex\n---\nbody",
		})
		set, err := Load(LoadOpts{ProjectDir: project})
		require.NoError(t, err)
		p, err := set.Profile("custom")
		require.NoError(t, err)

		st, err := p.Stage(set, "verify")
		require.NoError(t, err)
		assert.Equal(t, "codex", st.Executor)
		assert.Empty(t, st.Model, "opus belongs to claude and must not reach codex")
		assert.Equal(t, "high", st.Effort, "effort belongs to neither model, so it carries")
	})

	// each layer is applied in turn and none is collapsed first: an override that switches back to the
	// profile's binary must still find the profile's model, which folding stage and profile together
	// before applying it destroys
	t.Run("an override switching back to the profile's binary keeps the profile's model", func(t *testing.T) {
		project := writeTree(t, t.TempDir(), map[string]string{
			"prompts/synthesis.md": "---\ndescription: d\nmodel: codex/gpt-5.6-sol:low\n---\nbody",
			"prompts/profiles/custom.md": "---\nmodel: claude/opus:high\nagents:\n  - {name: a, lenses: [bugs]}\n" +
				"stages:\n  synthesis: claude\n---\nbody",
		})
		set, err := Load(LoadOpts{ProjectDir: project})
		require.NoError(t, err)
		p, err := set.Profile("custom")
		require.NoError(t, err)

		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "claude", st.Executor)
		assert.Equal(t, "opus", st.Model, "the profile's model is compatible with the override's binary")
		assert.Equal(t, "low", st.Effort, "effort still comes from the stage file, which named one first")
	})

	// the order is override, then the stage file's own model, then the profile's — resolving the
	// override straight against the profile skips the middle one
	t.Run("a partial override falls back to the stage file before the profile", func(t *testing.T) {
		project := writeTree(t, t.TempDir(), map[string]string{
			"prompts/synthesis.md": "---\ndescription: d\nmodel: claude/sonnet:low\n---\nbody",
			"prompts/profiles/custom.md": "---\nmodel: claude/opus:high\nagents:\n  - {name: a, lenses: [bugs]}\n" +
				"stages:\n  synthesis: claude\n---\nbody",
		})
		set, err := Load(LoadOpts{ProjectDir: project})
		require.NoError(t, err)
		p, err := set.Profile("custom")
		require.NoError(t, err)

		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "claude", st.Executor)
		assert.Equal(t, "sonnet", st.Model, "the stage file's own model, not the profile's opus")
		assert.Equal(t, "low", st.Effort)
	})

	t.Run("overriding one stage leaves the other alone", func(t *testing.T) {
		set, p := loadProfile(t, `agents:
  - {name: a, lenses: [bugs]}
stages:
  synthesis: codex
`)
		st, err := p.Stage(set, "verify")
		require.NoError(t, err)
		assert.Equal(t, "claude", st.Executor)
	})

	// the tree glob turns any prompts/*.md into a stage, so validating an override against what loaded
	// would accept this one and then never dispatch it — a silently ignored key
	t.Run("a loaded stage the pipeline does not dispatch is refused as an override", func(t *testing.T) {
		project := writeTree(t, t.TempDir(), map[string]string{
			"prompts/finalize.md": "---\ndescription: a project's own extra stage\n---\nbody",
			"prompts/profiles/custom.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n" +
				"stages:\n  {finalize: codex}\n---\nbody",
		})
		_, err := Load(LoadOpts{ProjectDir: project})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown stage "finalize"`)
		assert.Contains(t, err.Error(), "synthesis verify", "the error names the closed set")
	})

	t.Run("an unknown stage name is an error from the set, not a silent miss", func(t *testing.T) {
		set, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n")
		_, err := p.Stage(set, "nosuch")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown stage "nosuch"`)
	})
}

func TestProfile_ValidationErrors(t *testing.T) {
	tests := []struct {
		name, frontMatter, want string
	}{
		{"unknown executor", "agents:\n  - {name: a, lenses: [bugs], model: gemini/pro}\n", `unknown executor "gemini"`},
		{"unknown effort", "agents:\n  - {name: a, lenses: [bugs], model: claude/opus:turbo}\n", `unknown effort "turbo"`},
		{"missing lens", "agents:\n  - {name: a, lenses: [nosuch]}\n", `unknown lens "nosuch"`},
		{"no lenses", "agents:\n  - {name: a, lenses: []}\n", "no lenses"},
		{"no name", "agents:\n  - {lenses: [bugs]}\n", "roster entry has no name"},
		{"duplicate name", "agents:\n  - {name: a, lenses: [bugs]}\n  - {name: a, lenses: [adversarial]}\n", `duplicate agent name "a"`},
		// the name becomes one path component under agents/ and prompts/agents/, so it is rejected at
		// load rather than at the moment the archive builds a filename out of it
		{"name with a separator", "agents:\n  - {name: a/b, lenses: [bugs]}\n", `agent "a/b" contains a path separator`},
		{"absolute name", "agents:\n  - {name: /etc/passwd, lenses: [bugs]}\n", `agent "/etc/passwd" is an absolute path`},
		{"name referencing a parent", "agents:\n  - {name: ..up, lenses: [bugs]}\n", `agent "..up" references a parent`},
		{"hidden name", "agents:\n  - {name: .hidden, lenses: [bugs]}\n", `agent ".hidden" starts with a dot`},
		{"bad color name", "agents:\n  - {name: a, lenses: [bugs], color: puce}\n", `invalid color "puce"`},
		{"numeric color", "agents:\n  - {name: a, lenses: [bugs], color: \"12\"}\n", `invalid color "12"`},
		{"short hex color", "agents:\n  - {name: a, lenses: [bugs], color: \"#fff\"}\n", `invalid color "#fff"`},
		{"empty roster", "description: x\n", "roster is empty"},
		{"unknown stage", "agents:\n  - {name: a, lenses: [bugs]}\nstages:\n  {finalize: codex}\n",
			`unknown stage "finalize"`},
		{"stage name the pipeline never dispatches", "agents:\n  - {name: a, lenses: [bugs]}\nstages:\n  {find: codex}\n",
			`unknown stage "find"`},
		{"unknown stage executor", "agents:\n  - {name: a, lenses: [bugs]}\nstages:\n  {verify: gemini/pro}\n",
			`unknown executor "gemini"`},
		{"unknown stage effort", "agents:\n  - {name: a, lenses: [bugs]}\nstages:\n  {verify: claude/opus:turbo}\n",
			`unknown effort "turbo"`},
		{"stage override that is not a runner string", "agents:\n  - {name: a, lenses: [bugs]}\nstages:\n  {verify: {lenses: [bugs]}}\n",
			"parse front matter"},
		{"empty stage override", "agents:\n  - {name: a, lenses: [bugs]}\nstages:\n  {verify: \"\"}\n",
			"stage verify: empty runner"},
		{"whitespace stage override", "agents:\n  - {name: a, lenses: [bugs]}\nstages:\n  {verify: \"  \"}\n",
			"stage verify: empty runner"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := writeTree(t, t.TempDir(), map[string]string{
				"prompts/profiles/custom.md": "---\n" + tt.frontMatter + "---\nbody",
			})
			_, err := Load(LoadOpts{ProjectDir: project})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestProfile_Restrict(t *testing.T) {
	// a mixed roster: one claude agent, one codex agent, stages inheriting claude/opus:high
	const mixed = `model: claude/opus:high
agents:
  - {name: bugs, lenses: [bugs]}
  - {name: adversarial, lenses: [adversarial], model: codex/gpt-5.6-sol:high}
`
	known := map[string]struct{}{"bugs": {}, "adversarial": {}}

	t.Run("filters a mixed roster to the named binaries, order unchanged", func(t *testing.T) {
		_, p := loadProfile(t, mixed)
		p, err := p.Restrict([]string{"codex"})
		require.NoError(t, err)
		specs, err := p.Roster(nil, known)
		require.NoError(t, err)
		require.Len(t, specs, 1)
		assert.Equal(t, "adversarial", specs[0].Name)
		assert.Equal(t, "codex", specs[0].Executor)
		assert.Equal(t, "gpt-5.6-sol", specs[0].Model, "a surviving agent keeps its own runner whole")
	})

	t.Run("a filter that empties the roster is an error, never a zero-agent run", func(t *testing.T) {
		_, p := loadProfile(t, mixed)
		p, err := p.Restrict([]string{"agy"})
		require.NoError(t, err)
		_, err = p.Roster(nil, known)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "custom", "names the profile")
		assert.Contains(t, err.Error(), "claude", "names what the roster runs")
		assert.Contains(t, err.Error(), "codex")
	})

	t.Run("an unknown binary is an error naming the valid set", func(t *testing.T) {
		_, p := loadProfile(t, mixed)
		_, err := p.Restrict([]string{"gemini"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"gemini"`)
		assert.Contains(t, err.Error(), "agy", "the message carries the vocabulary")
	})

	t.Run("a binary carrying a model is an error: the flag filters, model strings select", func(t *testing.T) {
		_, p := loadProfile(t, mixed)
		_, err := p.Restrict([]string{"claude/opus"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"claude/opus"`)
	})

	t.Run("a stage resolving to an excluded binary falls back to the first listed, bare", func(t *testing.T) {
		set, p := loadProfile(t, mixed)
		p, err := p.Restrict([]string{"codex", "agy"})
		require.NoError(t, err)
		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "codex", st.Executor)
		assert.Empty(t, st.Model, "the excluded runner's model belongs to it and must not carry over")
		assert.Empty(t, st.Effort)
	})

	t.Run("a stage already on an allowed binary keeps its resolution", func(t *testing.T) {
		set, p := loadProfile(t, mixed)
		p, err := p.Restrict([]string{"claude"})
		require.NoError(t, err)
		st, err := p.Stage(set, "verify")
		require.NoError(t, err)
		assert.Equal(t, "claude", st.Executor)
		assert.Equal(t, "opus", st.Model)
		assert.Equal(t, "high", st.Effort)
	})

	t.Run("the lenses override falls back the same way rather than erroring", func(t *testing.T) {
		_, p := loadProfile(t, mixed)
		p, err := p.Restrict([]string{"agy"})
		require.NoError(t, err)
		specs, err := p.Roster([]string{"bugs"}, known)
		require.NoError(t, err)
		require.Len(t, specs, 1)
		assert.Equal(t, "agy", specs[0].Executor)
		assert.Empty(t, specs[0].Model)
	})

	t.Run("no filter leaves the roster and stages exactly as before", func(t *testing.T) {
		set, p := loadProfile(t, mixed)
		specs, err := p.Roster(nil, known)
		require.NoError(t, err)
		require.Len(t, specs, 2)
		st, err := p.Stage(set, "synthesis")
		require.NoError(t, err)
		assert.Equal(t, "opus", st.Model)
	})
}

func TestProfile_Roster_Colors(t *testing.T) {
	t.Run("every ansi name resolves to its index", func(t *testing.T) {
		for name, idx := range ansiColors {
			_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs], color: "+name+"}\n")
			specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
			require.NoError(t, err, name)
			assert.Equal(t, name, specs[0].ColorName)
			assert.Equal(t, strconv.Itoa(idx), specs[0].Color, name)
		}
	})

	t.Run("hex survives to the spec", func(t *testing.T) {
		_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs], color: \"#A1b2C3\"}\n")
		specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
		require.NoError(t, err)
		assert.Equal(t, "#A1b2C3", specs[0].Color)
		assert.Equal(t, "#A1b2C3", specs[0].ColorName)
	})

	t.Run("omitted is filled by position and stable across loads", func(t *testing.T) {
		fm := "agents:\n  - {name: a, lenses: [bugs]}\n  - {name: b, lenses: [bugs]}\n  - {name: c, lenses: [bugs]}\n"
		known := map[string]struct{}{"bugs": {}}

		_, first := loadProfile(t, fm)
		firstSpecs, err := first.Roster(nil, known)
		require.NoError(t, err)
		assert.Equal(t, []string{"6", "5", "2"}, []string{firstSpecs[0].Color, firstSpecs[1].Color, firstSpecs[2].Color})
		assert.Empty(t, firstSpecs[0].ColorName, "a palette color was never authored")

		_, second := loadProfile(t, fm)
		secondSpecs, err := second.Roster(nil, known)
		require.NoError(t, err)
		assert.Equal(t, firstSpecs, secondSpecs)
	})

	t.Run("palette wraps past its length", func(t *testing.T) {
		fm := strings.Builder{}
		fm.WriteString("agents:\n")
		for i := range len(colorPalette) + 1 {
			fm.WriteString("  - {name: a" + strconv.Itoa(i) + ", lenses: [bugs]}\n")
		}
		_, p := loadProfile(t, fm.String())
		specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
		require.NoError(t, err)
		assert.Equal(t, specs[0].Color, specs[len(colorPalette)].Color)
	})
}

func TestAgentSpec_Paint(t *testing.T) {
	tests := []struct {
		name  string
		color string
		want  string
	}{
		{"an index picks a color out of the reader's own theme", "6", "\x1b[36mbugs\x1b[39m"},
		{"the first index", "0", "\x1b[30mbugs\x1b[39m"},
		{"a bright index has its own range", "12", "\x1b[94mbugs\x1b[39m"},
		{"the last bright index", "15", "\x1b[97mbugs\x1b[39m"},
		{"hex asks for that exact shade", "#ff8800", "\x1b[38;2;255;136;0mbugs\x1b[39m"},
		{"black hex is not an absent color", "#000000", "\x1b[38;2;0;0;0mbugs\x1b[39m"},
		{"no color leaves the text alone", "", "bugs"},
		{"and so does an unresolvable one", "chartreuse", "bugs"},
		{"or an index outside the ansi-16 set", "16", "bugs"},
		{"or a malformed hex value", "#nothex", "bugs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, AgentSpec{Name: "bugs", Color: tt.color}.Paint("bugs"))
		})
	}

	t.Run("the reset restores the foreground only", func(t *testing.T) {
		// a full reset would clear the enclosing pane's background along with the color
		assert.NotContains(t, AgentSpec{Color: "6"}.Paint("bugs"), "\x1b[0m")
	})

	t.Run("an empty string is not worth a color sequence", func(t *testing.T) {
		assert.Empty(t, AgentSpec{Color: "6"}.Paint(""))
	})

	t.Run("every resolved roster color paints", func(t *testing.T) {
		_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n  - {name: b, lenses: [bugs], color: red}\n")
		specs, err := p.Roster(nil, map[string]struct{}{"bugs": {}})
		require.NoError(t, err)
		for _, s := range specs {
			assert.NotEqual(t, s.Name, s.Paint(s.Name), "a resolved color always reaches the renderers")
		}
	})
}

func TestProfile_Roster_LensOverride(t *testing.T) {
	known := map[string]struct{}{"bugs": {}, "adversarial": {}}
	_, p := loadProfile(t, `model: claude/opus:high
agents:
  - {name: bugs, lenses: [bugs], color: red}
  - {name: codex, lenses: [adversarial], model: codex/gpt-5.6-sol}
`)

	specs, err := p.Roster([]string{"bugs", "adversarial"}, known)
	require.NoError(t, err)
	require.Len(t, specs, 1, "two lenses yield one agent carrying both, not two sources")
	assert.Equal(t, AgentSpec{Name: "lenses", Lenses: []string{"bugs", "adversarial"}, Executor: "claude",
		Model: "opus", Effort: "high", Color: "6"}, specs[0])
}

func TestProfile_Roster_LensOverrideRejectsUnknownLens(t *testing.T) {
	_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n")
	_, err := p.Roster([]string{"bugs", "nosuch"}, map[string]struct{}{"bugs": {}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown lens "nosuch"`)
}

func TestProfile_Roster_DoesNotAliasTheProfile(t *testing.T) {
	known := map[string]struct{}{"bugs": {}}
	_, p := loadProfile(t, "agents:\n  - {name: a, lenses: [bugs]}\n")

	specs, err := p.Roster(nil, known)
	require.NoError(t, err)
	specs[0].Lenses[0] = "mutated"
	specs[0].Color = "#000000"

	again, err := p.Roster(nil, known)
	require.NoError(t, err)
	assert.Equal(t, []string{"bugs"}, again[0].Lenses)
	assert.Equal(t, "6", again[0].Color)
}

func TestStage_FrontMatter(t *testing.T) {
	t.Run("parsed", func(t *testing.T) {
		project := writeTree(t, t.TempDir(), map[string]string{
			"prompts/synthesis.md": "---\ndescription: d\nmodel: codex/gpt-5.6-sol:xhigh\n---\nbody",
		})
		set, err := Load(LoadOpts{ProjectDir: project})
		require.NoError(t, err)

		st, err := set.Stage("synthesis")
		require.NoError(t, err)
		assert.Equal(t, "d", st.Description)
		assert.Equal(t, "codex", st.Executor)
		assert.Equal(t, "gpt-5.6-sol", st.Model)
		assert.Equal(t, "xhigh", st.Effort)
	})

	tests := []struct {
		name, frontMatter, want string
	}{
		{"unknown executor", "model: gemini/pro\n", `stage synthesis: unknown executor "gemini"`},
		{"unknown effort", "model: claude/opus:turbo\n", `stage synthesis: unknown effort "turbo"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := writeTree(t, t.TempDir(), map[string]string{
				"prompts/synthesis.md": "---\n" + tt.frontMatter + "---\nbody",
			})
			_, err := Load(LoadOpts{ProjectDir: project})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestVocabularies(t *testing.T) {
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, Efforts())
	assert.Equal(t, []string{"claude", "codex", "agy"}, Executors())

	Efforts()[0] = "mutated"
	Executors()[0] = "mutated"
	assert.Equal(t, "low", Efforts()[0], "the accessor must not hand out the package slice")
	assert.Equal(t, "claude", Executors()[0])
}

// a stage or verify group is not in the roster but still has to be told apart in the log. Both
// renderers call this, so what it must guarantee is that they cannot disagree.
func TestDerivedSpec(t *testing.T) {
	t.Run("the same name always resolves to the same color", func(t *testing.T) {
		for _, name := range []string{"synthesis", "verify ui", "verify executor"} {
			first := DerivedSpec(name)
			assert.Equal(t, first, DerivedSpec(name), "%s must resolve identically every time", name)
			assert.NotEmpty(t, first.Color, "a derived agent is colored, or it cannot be told apart")
			assert.NotEmpty(t, first.ColorName)
			assert.Equal(t, name, first.Name)
		}
	})

	t.Run("it does not depend on when the agent was first seen", func(t *testing.T) {
		// the two renderers build their rows independently and create a derived agent on first sight,
		// so anything counting arrivals could hand them different colors for the same run
		forward := []AgentSpec{DerivedSpec("a"), DerivedSpec("b"), DerivedSpec("c")}
		backward := []AgentSpec{DerivedSpec("c"), DerivedSpec("b"), DerivedSpec("a")}
		assert.Equal(t, forward[0], backward[2])
		assert.Equal(t, forward[2], backward[0])
	})

	t.Run("different names generally differ, which is the point", func(t *testing.T) {
		seen := map[string]bool{}
		for _, n := range []string{"verify app", "verify ui", "verify executor", "verify pipeline"} {
			seen[DerivedSpec(n).ColorName] = true
		}
		assert.Greater(t, len(seen), 1, "one color for every verify group would defeat coloring them")
	})

	t.Run("an empty name is not colored", func(t *testing.T) {
		assert.Empty(t, DerivedSpec("").Color, "there is nothing to tell apart")
	})

	t.Run("the resolved color is one lipgloss accepts", func(t *testing.T) {
		spec := DerivedSpec("synthesis")
		assert.Contains(t, spec.Paint("x"), "x", "and Paint round-trips the text it wraps")
		assert.NotEqual(t, "x", spec.Paint("x"), "wrapped in a sequence rather than returned bare")
	})
}
