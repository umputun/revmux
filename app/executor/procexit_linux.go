package executor

import (
	"errors"

	"golang.org/x/sys/unix"
)

func waitForProcessExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err //nolint:wrapcheck // caller adds process-observation context
		}
	}
}
