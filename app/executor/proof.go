package executor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ProcessProof records launch intents and observed process-group disappearance. It never
// certifies descendants that escape a group, or the supervising runner's own exit.
type ProcessProof struct {
	mu        sync.Mutex
	path      string
	document  proofDocument
	err       error
	launchErr error
	finished  bool
}

type proofDocument struct {
	Version                 int               `json:"version"`
	Scope                   string            `json:"scope"`
	State                   string            `json:"state"`
	RunnerProcessInstanceID string            `json:"runnerProcessInstanceId"`
	ObservedAt              int64             `json:"observedAt,omitempty"`
	Instances               []processInstance `json:"instances"`
	Reason                  string            `json:"reason,omitempty"`
}

type processInstance struct {
	ProcessInstanceID string `json:"processInstanceId"`
	Role              string `json:"role"`
	PID               int    `json:"pid,omitempty"`
	PGID              int    `json:"pgid,omitempty"`
	State             string `json:"state"`
}

// Capabilities describes only guarantees implemented by this binary. Process-group
// proof must not be interpreted as containment of arbitrary descendant sessions.
func Capabilities() map[string]any {
	return map[string]any{
		"protocol": "plan-exec-revmux", "version": 1,
		"executionLifetime":    map[string]any{"version": 1, "flag": "--execution-lifetime", "modes": []string{"unbounded", "bounded"}},
		"processTerminalProof": map[string]any{"version": 1, "scope": "process-groups", "escapedDescendants": "unsupported", "supported": runtime.GOOS != "windows"},
	}
}

// NewProcessProof durably claims a previously unused absolute path before any model
// child can start. An interrupted writer leaves pending evidence, never observed proof.
func NewProcessProof(path string) (*ProcessProof, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("process proof path must be absolute")
	}
	if runtime.GOOS == "windows" {
		return nil, errors.New("process-group proof is unsupported on Windows")
	}
	id := instanceID()
	p := &ProcessProof{path: path, document: proofDocument{Version: 1, Scope: "process-groups", State: "pending", RunnerProcessInstanceID: id,
		Instances: []processInstance{{ProcessInstanceID: id, Role: "runner", PID: os.Getpid(), State: "pending"}}}}
	//nolint:gosec // caller explicitly chooses a new proof artifact; O_EXCL prevents overwriting it
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("claim process proof: %w", err)
	}
	if err = f.Close(); err != nil {
		return nil, fmt.Errorf("close process proof claim: %w", err)
	}
	if err := p.persist(); err != nil {
		return nil, err
	}
	return p, nil
}

func instanceID() string {
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	return hex.EncodeToString(nonce[:])
}

func (p *ProcessProof) prepare() (string, error) {
	if p == nil {
		return "", nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return "", errors.New("process proof already finalized")
	}
	if p.err != nil {
		return "", p.err
	}
	if p.launchErr != nil {
		return "", p.launchErr
	}
	id := instanceID()
	p.document.Instances = append(p.document.Instances, processInstance{ProcessInstanceID: id, Role: "agent", State: "pending"})
	return id, p.persist()
}

func (p *ProcessProof) started(id string, pid int) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.document.Instances {
		if p.document.Instances[i].ProcessInstanceID == id {
			p.document.Instances[i].PID = pid
			p.document.Instances[i].PGID = pid
		}
	}
	return p.persist()
}

func (p *ProcessProof) notStarted(id string) { p.completed(id, nil) }

func (p *ProcessProof) completed(id string, observation error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.document.Instances {
		if p.document.Instances[i].ProcessInstanceID == id {
			p.document.Instances[i].State = "observed"
			if observation != nil {
				p.document.Instances[i].State = "unknown"
			}
		}
	}
	if observation != nil {
		p.document.Reason = observation.Error()
		p.launchErr = observation
	}
	_ = p.persist()
}

// Finish closes the launch set and publishes observed only when every recorded agent
// group was observed absent. Runner exit must be observed separately by its parent.
func (p *ProcessProof) Finish() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finished = true
	p.document.State = "observed"
	p.document.ObservedAt = time.Now().UnixMilli()
	for _, instance := range p.document.Instances {
		if instance.Role == "agent" && instance.State != "observed" {
			p.document.State = "unknown"
			p.document.ObservedAt = 0
			if p.document.Reason == "" {
				p.document.Reason = "an agent process group has no terminal observation"
			}
		}
	}
	if p.err != nil {
		return p.err
	}
	return p.persist()
}

func (p *ProcessProof) persist() (retErr error) {
	defer func() {
		if retErr != nil {
			p.err = retErr
		}
	}()
	f, err := os.CreateTemp(filepath.Dir(p.path), ".process-proof-*")
	if err != nil {
		return fmt.Errorf("create process proof update: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck // best effort temporary file cleanup
	defer f.Close()
	if err = json.NewEncoder(f).Encode(p.document); err != nil {
		return fmt.Errorf("encode process proof: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync process proof: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close process proof: %w", err)
	}
	if err = os.Rename(f.Name(), p.path); err != nil {
		return fmt.Errorf("publish process proof: %w", err)
	}
	dir, err := os.Open(filepath.Dir(p.path))
	if err != nil {
		return fmt.Errorf("open process proof directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync process proof directory: %w", err)
	}
	return nil
}
