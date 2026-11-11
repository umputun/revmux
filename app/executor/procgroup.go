package executor

import (
	"os/exec"
	"sync"
	"time"
)

// killGrace is how long a process group gets between SIGTERM and SIGKILL.
const killGrace = 200 * time.Millisecond

// processGroupCleanup tears down a started process and everything it spawned. Both the wait and the kill
// are once-guarded, so the normal-exit path and the cancellation path can both run.
type processGroupCleanup struct {
	cmd      *exec.Cmd
	waitOnce sync.Once
	waitErr  error
	killOnce sync.Once
	done     chan struct{}
}

func newProcessGroupCleanup(cmd *exec.Cmd, cancelCh <-chan struct{}) *processGroupCleanup {
	pg := &processGroupCleanup{cmd: cmd, done: make(chan struct{})}
	go pg.watchForCancel(cancelCh)
	return pg
}

func (pg *processGroupCleanup) wait() error {
	pg.waitOnce.Do(func() {
		pg.waitErr = pg.cmd.Wait()
		close(pg.done)
	})
	return pg.waitErr
}

func (pg *processGroupCleanup) watchForCancel(cancelCh <-chan struct{}) {
	select {
	case <-cancelCh:
		pg.killProcessGroup()
	case <-pg.done:
	}
}
