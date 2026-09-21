package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapabilitiesAndExplicitUnbounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	opts, err := parseArgs([]string{"--capabilities", "--hard-timeout=0s", "--idle-timeout=0s", "--process-proof=" + filepath.Join(t.TempDir(), "proof.json")})
	require.NoError(t, err)
	assert.Zero(t, opts.HardTimeout)
	assert.Zero(t, opts.IdleTimeout)
	var stdout, stderr bytes.Buffer
	assert.Zero(t, run(runOpts{opts: opts, stdout: &stdout, stderr: &stderr}))
	assert.Empty(t, stderr.String())
	var got struct {
		Protocol string `json:"protocol"`
		Version  int    `json:"version"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, "plan-exec-revmux", got.Protocol)
	assert.Equal(t, 1, got.Version)
}
