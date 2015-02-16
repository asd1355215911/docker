package daemon

import (
	"time"
)

func (container *Container) Kill() error {
	if !container.IsRunning() {
		return nil
	}

	// 1. Send SIGKILL
	if err := container.killPossiblyDeadProcess(9); err != nil {
		return err
	}

	// 2. Wait for the process to die, in last resort, try to kill the process directly
	// TODO Windows: Equivalent to Linux

	container.WaitStop(-1 * time.Second)
	return nil
}
