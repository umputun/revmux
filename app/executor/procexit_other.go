//go:build !darwin && !linux && !windows

package executor

import "errors"

func waitForProcessExit(_ int) error {
	return errors.New("child exit observation is unsupported on this platform")
}
