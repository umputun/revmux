package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
)

func TestOpenCode_Run_cleanCapture(t *testing.T) {
	c := executor.NewOpenCode(fakeRunner("cat", filepath.Join("testdata", "opencode-clean.jsonl")), executor.Opts{})
	sink := discardSink()
	res, err := c.Run(context.Background(), executor.Request{Prompt: "test"}, sink)
	require.NoError(t, err)
	assert.Zero(t, res.ExitCode)
	assert.Equal(t, 250, res.Tokens, "sum of step_finish input+output across both steps")
	assert.Contains(t, res.Raw, "step_start")

	kinds := eventKinds(sink)
	assert.Contains(t, kinds, executor.EventActivity, "text events produce activity")
	assert.Contains(t, kinds, executor.EventProgress, "tool_use events produce progress")
}

func TestOpenCode_Run_extractsJSON(t *testing.T) {
	c := executor.NewOpenCode(fakeRunner("cat", filepath.Join("testdata", "opencode-clean.jsonl")), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "test"}, discardSink())
	require.NoError(t, err)
	require.NotNil(t, res.StructuredOutput, "last text block should parse as JSON")
	assert.JSONEq(t, `{"findings":[]}`, string(res.StructuredOutput))
}

func TestOpenCode_Run_setsRequestedModel(t *testing.T) {
	c := executor.NewOpenCode(fakeRunner("cat", filepath.Join("testdata", "opencode-clean.jsonl")), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "test", Model: "opencode/gpt-5.1"}, discardSink())
	require.NoError(t, err)
	assert.Equal(t, "opencode/gpt-5.1", res.RequestedModel)
	assert.Equal(t, "opencode/gpt-5.1", res.ActualModel)
}

func TestOpenCode_Run_scrubsChildEnv(t *testing.T) {
	t.Setenv("OPENCODE", "1")
	c := executor.NewOpenCode(fakeRunner("env", "-"), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err)
	assert.NotContains(t, res.Raw, "OPENCODE=")
	assert.Contains(t, res.Raw, "PATH=")
}

func TestOpenCodeOutputContract_empty(t *testing.T) {
	assert.Empty(t, executor.OpenCodeOutputContract(nil))
}

func TestOpenCodeOutputContract_withSchema(t *testing.T) {
	schema := []byte(`{"type":"object"}`)
	contract := executor.OpenCodeOutputContract(schema)
	assert.Contains(t, contract, "Return ONLY a JSON object")
	assert.Contains(t, contract, `{"type":"object"}`)
}
