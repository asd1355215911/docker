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

// TODO WINDOWS - This function still needs more refactoring.
func (container *Container) Start() (err error) {
	container.Lock()
	defer container.Unlock()

	if container.Running {
		return nil
	}

	// if we encounter an error during start we need to ensure that any other
	// setup has been cleaned up properly
	defer func() {
		if err != nil {
			container.setError(err)
			// if no one else has set it, make sure we don't leave it at zero
			if container.ExitCode == 0 {
				container.ExitCode = 128
			}
			container.toDisk()
			container.cleanup()
		}
	}()

	if err := container.Mount(); err != nil {
		return err
	}

	// This is where is Linux it calls into the Exec Driver. TODO WINDOWS (see populateCommand())

	// TODO WINDOWS To factor this out requires work in volumes.go which I suspect might be a bunch of work
	if err := container.setupMounts(); err != nil {
		return err
	}

	return container.waitForStart()
}

// No-op on Windows. TODO Windows. Factor this out
func (container *Container) RestoreNetwork() error {
	return nil
}

// TODO WINDOWS
// This can be totally factored out but currently also used in create.go
func (container *Container) prepareVolumes() error {
	return nil
}

// TODO WINDOWS
// GetSize, return real size, virtual size
func (container *Container) GetSize() (int64, int64) {
	return 0, 0
}
