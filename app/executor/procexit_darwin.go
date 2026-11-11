package executor

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func waitForProcessExit(pid int) error {
	fd, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("create exit observer: %w", err)
	}
	defer unix.Close(fd)
	unix.CloseOnExec(fd)
	change := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}} //nolint:gosec // PID comes from a successfully started child
	for {
		_, err = unix.Kevent(fd, change, nil, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	// Only this caller reaps the child, so ESRCH cannot refer to a reused PID.
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("register child exit: %w", err)
	}
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(fd, nil, events[:], nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for child exit: %w", err)
		}
		if n > 0 && events[0].Fflags&unix.NOTE_EXIT != 0 {
			return nil
		}
	}
}
