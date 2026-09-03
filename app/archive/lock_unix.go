//go:build !windows

package archive

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes an exclusive advisory lock on f and never waits for one. The lock belongs to the
// descriptor rather than the process, so a second Archive on the same round is refused even inside one
// revmux, and the kernel drops it when the holder dies — which is what tells an abandoned round from a
// live one.
func tryLock(f *os.File) (ok bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // a process fd, far inside int range
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("flock %s: %w", f.Name(), err)
	}
	return true, nil
}
