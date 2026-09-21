//go:build !windows

package executor

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// setupProcessGroup detaches the child from the controlling terminal. Setsid rather than Setpgid: a
// descendant touching terminal I/O would otherwise stop the whole group with SIGTTIN/SIGTTOU.
func (p *proc) setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// killProcessGroup signals the whole group, not just the direct child — node subagents and MCP servers
// are the things that actually accumulate.
func (pg *processGroupCleanup) killProcessGroup() {
	pg.killOnce.Do(func() {
		if pg.cmd.Process == nil {
			return
		}
		pgid := pg.cmd.Process.Pid
		// a group that is already gone needs no grace delay, and every normal exit takes this path
		if err := syscall.Kill(-pgid, syscall.SIGTERM); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(killGrace)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
}

// A successful signal is not exit evidence. No further signals are sent after Wait:
// the reaped leader's numeric PID could already belong to another process.
func observeProcessGroupExit(pgid int) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("observe process group %d: %w", pgid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process group %d still exists after cleanup", pgid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
