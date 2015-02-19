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

// No-op on Windows. TODO Windows. Factor this out.
func (container *Container) setupContainerDns() error {
	return nil
}

// No-op on Windows. TODO Windows. Factor this out
func (container *Container) updateParentsHosts() error {
	return nil
}

// No-op on Windows. TODO Windows. Factor this out
// Make sure the config is compatible with the current kernel
func (container *Container) verifyDaemonSettings() {
}

// No-op on Windows. TODO Windows. Factor this out
func (container *Container) AllocateNetwork() error {
	return nil
}

// No-op on Windows. TODO Windows. Factor this out
func (container *Container) RestoreNetwork() error {
	return nil
}

// No-op on Windows. TODO Windows. Factor this out
func (container *Container) initializeNetworking() error {
	return nil
}
