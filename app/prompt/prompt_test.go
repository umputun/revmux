package prompt

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTree materializes a prompt tree under root; keys are slash-separated relative paths.
func writeTree(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	return root
}

func TestLoad_EmbeddedDefaults(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	assert.Equal(t, []string{"claude-only", "codex-only", "comprehensive", "final", "focused", "grill-me", "triage"}, set.ProfileNames())
	for _, l := range set.Lenses() {
		assert.NotEmpty(t, l.Description, "lens %s", l.Name)
	}

	for _, name := range []string{"synthesis", "verify"} {
		st, err := set.Stage(name)
		require.NoError(t, err, name)
		assert.Empty(t, st.Executor, "a stage file names no runner; the profile that runs it does")
		assert.NotEmpty(t, st.Body, name)
	}

	assert.Equal(t, map[string]struct{}{"adversarial": {}, "antithesis": {}, "architecture": {}, "bugs": {},
		"comments": {}, "cost": {}, "docs": {}, "grounding": {}, "impl": {}, "precedent": {}, "quality": {},
		"tests": {}, "thesis": {}}, set.LensNames())
}

func TestLoad_MissingDirsAreAbsentLayers(t *testing.T) {
	set, err := Load(LoadOpts{ProjectDir: filepath.Join(t.TempDir(), "nope"), UserDir: filepath.Join(t.TempDir(), "also-nope")})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-only", "codex-only", "comprehensive", "final", "focused", "grill-me", "triage"}, set.ProfileNames())

	for _, o := range set.Provenance() {
		assert.Equal(t, LayerEmbedded, o.Layer, o.Path)
	}
}

func TestLoad_Precedence(t *testing.T) {
	user := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md":       "---\ndescription: user bugs\n---\nuser bugs body",
		"lenses/user-only.md":  "---\ndescription: only the user has this\n---\nuser only body",
		"prompts/synthesis.md": "---\ndescription: user synthesis\n---\nuser synthesis body",
	})
	project := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md": "---\ndescription: project bugs\n---\nproject bugs body",
	})

	set, err := Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)

	body, err := set.lens("bugs")
	require.NoError(t, err)
	assert.Equal(t, "project bugs body", body, "project beats user")

	body, err = set.lens("user-only")
	require.NoError(t, err)
	assert.Equal(t, "user only body", body, "a lens only the user has must appear")

	body, err = set.lens("adversarial")
	require.NoError(t, err)
	assert.Contains(t, body, "Lens: adversarial", "an un-overridden lens keeps the embedded text")

	st, err := set.Stage("synthesis")
	require.NoError(t, err)
	assert.Equal(t, "user synthesis body", st.Body, "user beats embedded")

	p, err := set.Profile("focused")
	require.NoError(t, err)
	assert.Contains(t, p.Body, "Severity bar", "overriding one lens does not orphan the profile")
}

func TestLoad_Provenance(t *testing.T) {
	project := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md": "---\ndescription: project bugs\n---\nproject bugs body",
	})
	user := writeTree(t, t.TempDir(), map[string]string{
		"prompts/verify.md": "---\ndescription: user verify\n---\nuser verify body",
	})

	set, err := Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)

	byPath := map[string]FileOrigin{}
	for _, o := range set.Provenance() {
		byPath[o.Path] = o
	}

	assert.Equal(t, LayerProject, byPath["lenses/bugs.md"].Layer)
	assert.Equal(t, filepath.Join(project, "lenses", "bugs.md"), byPath["lenses/bugs.md"].Source)
	assert.Equal(t, LayerUser, byPath["prompts/verify.md"].Layer)
	assert.Equal(t, LayerEmbedded, byPath["lenses/adversarial.md"].Layer)
	assert.Empty(t, byPath["lenses/adversarial.md"].Source, "an embedded file has no on-disk source")
	assert.Len(t, byPath["lenses/bugs.md"].Hash, 64)

	first := byPath["lenses/bugs.md"].Hash
	writeTree(t, project, map[string]string{"lenses/bugs.md": "---\ndescription: project bugs\n---\nedited body"})
	set, err = Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)
	for _, o := range set.Provenance() {
		if o.Path == "lenses/bugs.md" {
			assert.NotEqual(t, first, o.Hash, "editing an override must change its hash")
		}
	}
}

func TestSet_Content(t *testing.T) {
	const (
		projectBugs = "---\ndescription: project bugs\n---\nproject bugs body\n"
		userBugs    = "---\ndescription: user bugs\n---\nuser bugs body\n"
		userVerify  = "---\ndescription: user verify\n---\nuser verify body\n"
	)
	project := writeTree(t, t.TempDir(), map[string]string{"lenses/bugs.md": projectBugs})
	user := writeTree(t, t.TempDir(), map[string]string{"lenses/bugs.md": userBugs, "prompts/verify.md": userVerify})

	set, err := Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)

	t.Run("front matter is retained", func(t *testing.T) {
		got, err := set.Content("lenses/bugs.md")
		require.NoError(t, err)
		assert.Equal(t, projectBugs, string(got), "the unparsed bytes, not the stripped body")
		assert.Contains(t, string(got), "---\ndescription:", "a stripped write would fail the next Load")
	})

	t.Run("project layer wins", func(t *testing.T) {
		got, err := set.Content("lenses/bugs.md")
		require.NoError(t, err)
		assert.Equal(t, projectBugs, string(got))
		assert.NotEqual(t, userBugs, string(got), "the user copy of the same file must not be materialized")
	})

	t.Run("user layer wins", func(t *testing.T) {
		got, err := set.Content("prompts/verify.md")
		require.NoError(t, err)
		assert.Equal(t, userVerify, string(got))
	})

	t.Run("embedded layer wins", func(t *testing.T) {
		want, err := fs.ReadFile(Defaults(), "lenses/adversarial.md")
		require.NoError(t, err)
		got, err := set.Content("lenses/adversarial.md")
		require.NoError(t, err)
		assert.Equal(t, want, got, "an un-overridden file materializes the embedded bytes")
	})

	t.Run("unknown path errors", func(t *testing.T) {
		got, err := set.Content("lenses/nosuch.md")
		require.ErrorContains(t, err, `unknown prompt file "lenses/nosuch.md"`)
		assert.Nil(t, got, "an unknown path must never yield empty bytes to write")
	})

	t.Run("the caller cannot mutate the set", func(t *testing.T) {
		got, err := set.Content("lenses/bugs.md")
		require.NoError(t, err)
		got[0] = 'x'
		again, err := set.Content("lenses/bugs.md")
		require.NoError(t, err)
		assert.Equal(t, projectBugs, string(again))
	})
}

func TestSet_ContentAgreesWithProvenance(t *testing.T) {
	project := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md":              "---\ndescription: project bugs\n---\nproject bugs body\n",
		"prompts/profiles/focused.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n---\nproject focused body\n",
	})
	user := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md":       "---\ndescription: user bugs\n---\nuser bugs body\n",
		"lenses/user-only.md":  "---\ndescription: only the user has this\n---\nuser only body\n",
		"prompts/synthesis.md": "---\ndescription: user synthesis\n---\nuser synthesis body\n",
	})

	set, err := Load(LoadOpts{ProjectDir: project, UserDir: user})
	require.NoError(t, err)

	origins := set.Provenance()
	require.NotEmpty(t, origins)
	layers := map[string]bool{}
	for _, o := range origins {
		data, err := set.Content(o.Path)
		require.NoError(t, err, o.Path)
		assert.Equal(t, o.Hash, hashOf(data), "%s: Content must be the bytes Provenance hashed", o.Path)

		if o.Layer == LayerEmbedded {
			want, err := fs.ReadFile(Defaults(), o.Path)
			require.NoError(t, err, o.Path)
			assert.Equal(t, want, data, o.Path)
		} else {
			want, err := os.ReadFile(o.Source)
			require.NoError(t, err, o.Path)
			assert.Equal(t, want, data, o.Path)
		}
		layers[o.Layer] = true
	}
	assert.Equal(t, map[string]bool{LayerProject: true, LayerUser: true, LayerEmbedded: true}, layers,
		"all three layers must be exercised or the agreement is untested")
}

func TestSet_LensDescriptionIsNotInherited(t *testing.T) {
	project := writeTree(t, t.TempDir(), map[string]string{
		"lenses/bugs.md":        "---\ndescription: my own bugs lens\n---\nbody",
		"lenses/adversarial.md": "no front matter at all",
	})

	set, err := Load(LoadOpts{ProjectDir: project})
	require.NoError(t, err)

	got := map[string]string{}
	for _, l := range set.Lenses() {
		got[l.Name] = l.Description
	}
	assert.Equal(t, "my own bugs lens", got["bugs"], "an override reports its own description, never the embedded default's")
	assert.Empty(t, got["adversarial"], "an override with no front matter reports no description rather than inheriting one")
	assert.NotEmpty(t, got["quality"], "a lens nobody overrode keeps the embedded description")
}

func TestSet_UnknownNames(t *testing.T) {
	set, err := Load(LoadOpts{})
	require.NoError(t, err)

	_, err = set.Profile("nope")
	require.ErrorContains(t, err, "unknown profile")
	_, err = set.Stage("nope")
	require.ErrorContains(t, err, "unknown stage")
	_, err = set.lens("nope")
	require.ErrorContains(t, err, "unknown lens")
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"unknown variable", map[string]string{"lenses/bugs.md": "read {{DIFF}} first"}, "unknown variable {{DIFF}}"},
		{"unterminated front matter", map[string]string{"lenses/bugs.md": "---\ndescription: x\nbody"}, "never closed"},
		{"bad yaml", map[string]string{"lenses/bugs.md": "---\ndescription: [1,\n---\nbody"}, "parse front matter"},
		{"unknown front matter key", map[string]string{"lenses/bugs.md": "---\ndescriptio: typo\n---\nbody"}, "parse front matter"},
		{
			"missing lens",
			map[string]string{"prompts/profiles/focused.md": "---\nagents:\n  - {name: a, lenses: [nosuch]}\n---\nbody"},
			`unknown lens "nosuch"`,
		},
		{
			"empty roster",
			map[string]string{"prompts/profiles/focused.md": "---\ndescription: x\n---\nbody"},
			"roster is empty",
		},
		{
			// the binary leads the model string, so an unknown one is caught without revmux owning a
			// catalog of model names
			"profile-level model naming an unknown binary",
			map[string]string{
				"prompts/profiles/focused.md": "---\nmodel: gemini/pro\nagents:\n  - {name: a, lenses: [bugs]}\n---\nbody",
			},
			`unknown executor "gemini"`,
		},
		{
			"lens naming a runner it does not select",
			map[string]string{"lenses/bugs.md": "---\ndescription: x\nmodel: opus\n---\nbody"},
			"parse front matter",
		},
		{
			"stage declaring a roster it cannot have",
			map[string]string{"prompts/verify.md": "---\nagents:\n  - {name: a, lenses: [bugs]}\n---\nbody"},
			"parse front matter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(LoadOpts{ProjectDir: writeTree(t, t.TempDir(), tt.files)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoad_StageVariablesAreAllowed(t *testing.T) {
	project := writeTree(t, t.TempDir(), map[string]string{
		"prompts/synthesis.md": "---\ndescription: x\n---\n{{SOURCES}} then {{FINDINGS}}",
	})
	_, err := Load(LoadOpts{ProjectDir: project})
	require.NoError(t, err)
}
