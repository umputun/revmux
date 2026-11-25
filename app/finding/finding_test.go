package finding

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allSeverities = []Severity{Critical, Major, Minor}

var allVerdicts = []Verdict{Confirmed, Refined, Rejected, Immaterial, PreExisting, Unverified}

func TestFinding_JSONRoundTrip(t *testing.T) {
	in := Finding{
		ID: "bugs+impl-1", File: "app/executor/proc.go", Line: 120, EndLine: 128,
		Severity: Critical, Confidence: 92, Title: "watchdog never rearmed",
		Body: "the idle timer is armed once and never reset", Fix: "reset the timer per line",
		Sources: []string{"bugs+impl", "codex"}, Lenses: []string{"bugs", "adversarial"},
		Verdict: Confirmed,
	}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	var out Finding
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, in, out)
}

func TestFinding_SourcesAndLensesAreDistinct(t *testing.T) {
	in := Finding{Sources: []string{"bugs+impl"}, Lenses: []string{"bugs", "impl"}}

	data, err := json.Marshal(in)
	require.NoError(t, err)

	raw := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Contains(t, raw, "sources")
	require.Contains(t, raw, "lenses")
	assert.JSONEq(t, `["bugs+impl"]`, string(raw["sources"]))
	assert.JSONEq(t, `["bugs","impl"]`, string(raw["lenses"]))

	var out Finding
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, []string{"bugs+impl"}, out.Sources, "one agent stays one source")
	assert.Equal(t, []string{"bugs", "impl"}, out.Lenses, "both lenses survive independently")
}

func TestFinding_NoTagsField(t *testing.T) {
	data, err := json.Marshal(Finding{})
	require.NoError(t, err)

	raw := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.NotContains(t, raw, "tags")
}

func TestFinding_Location(t *testing.T) {
	tests := []struct {
		name string
		f    Finding
		want string
	}{
		{"single line", Finding{File: "app/main.go", Line: 12}, "app/main.go:12"},
		{"single line with zero end", Finding{File: "app/main.go", Line: 12, EndLine: 0}, "app/main.go:12"},
		{"end equal to line", Finding{File: "app/main.go", Line: 12, EndLine: 12}, "app/main.go:12"},
		{"range", Finding{File: "app/main.go", Line: 12, EndLine: 18}, "app/main.go:12-18"},
		{"file level", Finding{File: "app/main.go"}, "app/main.go"},
		{"file level with end line", Finding{File: "app/main.go", EndLine: 9}, "app/main.go"},
		{"no file", Finding{Line: 12}, "(no file)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.f.Location())
		})
	}
}

func TestFinding_MetaLine(t *testing.T) {
	tests := []struct {
		name string
		f    Finding
		want string
	}{
		{"confidence only", Finding{Confidence: 70}, "_confidence: 70_"},
		{"with sources", Finding{Confidence: 70, Sources: []string{"a", "b"}}, "_confidence: 70 | sources: a, b_"},
		{"with lenses", Finding{Confidence: 70, Lenses: []string{"bugs"}}, "_confidence: 70 | lenses: bugs_"},
		{"full", Finding{Confidence: 92, Sources: []string{"a"}, Lenses: []string{"bugs"}, Verdict: Confirmed},
			"_confidence: 92 | sources: a | lenses: bugs | verdict: confirmed_"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.f.metaLine())
		})
	}
}

func TestFinding_MarkdownLines(t *testing.T) {
	f := Finding{File: "app/main.go", Line: 12, Title: "leaks a file handle", Body: "the file is never closed",
		Fix: "defer f.Close()", Confidence: 80}
	assert.Equal(t, []string{
		"### leaks a file handle", "",
		"`app/main.go:12`", "",
		"the file is never closed", "",
		"Fix: defer f.Close()", "",
		"_confidence: 80_", "",
	}, f.markdownLines())

	bare := Finding{File: "app/main.go", Title: "no body or fix", Confidence: 50}
	assert.Equal(t, []string{
		"### no body or fix", "",
		"`app/main.go`", "",
		"_confidence: 50_", "",
	}, bare.markdownLines())

	judged := Finding{File: "app/main.go", Title: "verified", Fix: "close it", Confidence: 90, Verdict: Confirmed,
		Reason: "the handle leaks on the error path"}
	assert.Equal(t, []string{
		"### verified", "",
		"`app/main.go`", "",
		"Fix: close it", "",
		"Verifier: the handle leaks on the error path", "",
		"_confidence: 90 | verdict: confirmed_", "",
	}, judged.markdownLines())
}

func TestSeverity_Rank(t *testing.T) {
	assert.Equal(t, 0, Critical.rank())
	assert.Equal(t, 1, Major.rank())
	assert.Equal(t, 2, Minor.rank())
	assert.Equal(t, 3, Severity("bogus").rank(), "an unrecognized severity sorts last, never dropped")
	assert.Equal(t, 3, Severity("").rank())
}

func TestSeverity_Heading(t *testing.T) {
	assert.Equal(t, "Critical", Critical.Heading())
	assert.Equal(t, "Major", Major.Heading())
	assert.Equal(t, "Minor", Minor.Heading())
	assert.Equal(t, "Unspecified", Severity("").Heading())
	assert.Equal(t, "Bogus", Severity("bogus").Heading())
	// only claude gets a --json-schema, so a codex source can put any string here
	assert.Equal(t, "Ölarm", Severity("ölarm").Heading(), "a multi-byte first rune must survive titlecasing")
	assert.True(t, utf8.ValidString(Severity("ölarm").Heading()), "a byte-indexed cut would split the rune")
}
