//go:build !windows

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readProof(t *testing.T, path string) proofDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)
	var doc proofDocument
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

func TestProcessProofLaunchAndExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	before := readProof(t, path)
	assert.Equal(t, "pending", before.State)
	require.Len(t, before.Instances, 1)
	assert.Equal(t, os.Getpid(), before.Instances[0].PID)

	p := newProc("sh", NewRunner(), Opts{ProcessProof: proof})
	child, err := p.start(context.Background(), []string{"-c", "exit 0"}, "")
	require.NoError(t, err)
	started := readProof(t, path)
	require.Len(t, started.Instances, 2)
	assert.Equal(t, "pending", started.State)
	assert.Equal(t, child.cmd.Process.Pid, started.Instances[1].PGID)
	assert.NotEqual(t, before.RunnerProcessInstanceID, started.Instances[1].ProcessInstanceID)
	_, err = io.Copy(io.Discard, child.stdout)
	require.NoError(t, err)
	child.finish()
	require.NoError(t, proof.Finish())
	after := readProof(t, path)
	assert.Equal(t, "observed", after.State)
	assert.Equal(t, "process-groups", after.Scope)
	assert.Positive(t, after.ObservedAt)
	assert.Equal(t, "pending", after.Instances[0].State)
	assert.Equal(t, "observed", after.Instances[1].State)
	require.ErrorIs(t, syscall.Kill(-child.cmd.Process.Pid, 0), syscall.ESRCH)
	_, err = p.start(context.Background(), []string{"-c", "exit 0"}, "")
	require.ErrorContains(t, err, "already finalized")
}

func TestProcessProofCrashGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	_, err = proof.prepare()
	require.NoError(t, err)
	pending := readProof(t, path)
	require.Len(t, pending.Instances, 2)
	assert.Zero(t, pending.Instances[1].PID)
	assert.Equal(t, "pending", pending.State)
	require.NoError(t, proof.Finish())
	assert.Equal(t, "unknown", readProof(t, path).State)
	_, err = NewProcessProof(path)
	require.ErrorContains(t, err, "claim process proof")
	assert.Equal(t, "unknown", readProof(t, path).State)
}

func TestProcessProofUnknownObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	id, err := proof.prepare()
	require.NoError(t, err)
	proof.completed(id, errors.New("group still exists"))
	_, err = proof.prepare()
	require.ErrorContains(t, err, "group still exists")
	require.NoError(t, proof.Finish())
	doc := readProof(t, path)
	assert.Equal(t, "unknown", doc.State)
	assert.Zero(t, doc.ObservedAt)
	assert.Equal(t, "group still exists", doc.Reason)
}

func TestProcessProofWriteFailureFencesLaunch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Remove(dir))
	p := newProc("sh", NewRunner(), Opts{ProcessProof: proof})
	child, err := p.start(context.Background(), []string{"-c", "exit 0"}, "")
	require.Error(t, err)
	assert.Nil(t, child)
	require.Error(t, proof.Finish())
}

func TestProcessProofCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newProc("sh", NewRunner(), Opts{ProcessProof: proof})
	child, err := p.start(ctx, []string{"-c", "exec sleep 60"}, "")
	require.NoError(t, err)
	cancel()
	child.finish()
	require.NoError(t, proof.Finish())
	assert.Equal(t, "observed", readProof(t, path).State)
	require.ErrorIs(t, syscall.Kill(-child.cmd.Process.Pid, 0), syscall.ESRCH)
}

func TestProcessProofFailedStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	p := newProc(filepath.Join(t.TempDir(), "missing"), NewRunner(), Opts{ProcessProof: proof})
	_, err = p.start(context.Background(), nil, "")
	require.Error(t, err)
	require.NoError(t, proof.Finish())
	doc := readProof(t, path)
	assert.Equal(t, "observed", doc.State)
	require.Len(t, doc.Instances, 2)
	assert.Zero(t, doc.Instances[1].PID)
}

func TestObserveProcessGroupExitDoesNotSignal(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	require.ErrorContains(t, observeProcessGroupExit(cmd.Process.Pid), "still exists")
	require.NoError(t, cmd.Process.Signal(syscall.Signal(0)))
}

func TestProcessProofCleansGroupDescendant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	p := newProc("sh", NewRunner(), Opts{ProcessProof: proof})
	child, err := p.start(context.Background(), []string{"-c", "sleep 60 </dev/null >/dev/null 2>&1 & exit 0"}, "")
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, child.stdout)
	require.NoError(t, err)
	child.finish()
	require.NoError(t, proof.Finish())
	assert.Equal(t, "observed", readProof(t, path).State)
	require.ErrorIs(t, syscall.Kill(-child.cmd.Process.Pid, 0), syscall.ESRCH)
}

func TestProcessProofEscapedDescendantRemainsOutsideScope(t *testing.T) {
	t.Setenv("REVMUX_PROOF_HELPER", "spawn")
	path := filepath.Join(t.TempDir(), "proof.json")
	proof, err := NewProcessProof(path)
	require.NoError(t, err)
	binary, err := os.Executable()
	require.NoError(t, err)
	p := newProc(binary, NewRunner(), Opts{ProcessProof: proof})
	child, err := p.start(context.Background(), []string{"-test.run=^TestProcessProofEscapeHelper$"}, "")
	require.NoError(t, err)
	output, err := io.ReadAll(child.stdout)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	require.NoError(t, err)
	escaped, err := os.FindProcess(pid)
	require.NoError(t, err)
	t.Cleanup(func() { _ = escaped.Kill() })
	child.finish()
	require.NoError(t, proof.Finish())
	doc := readProof(t, path)
	assert.Equal(t, "observed", doc.State)
	assert.Equal(t, "process-groups", doc.Scope)
	require.NoError(t, escaped.Signal(syscall.Signal(0)))
}

func TestProcessProofEscapeHelper(t *testing.T) {
	switch os.Getenv("REVMUX_PROOF_HELPER") {
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "spawn":
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestProcessProofEscapeHelper$") //nolint:gosec // re-exec this test binary
		cmd.Env = []string{"REVMUX_PROOF_HELPER=sleep"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
		fmt.Println(cmd.Process.Pid)
		os.Exit(0)
	}
}
