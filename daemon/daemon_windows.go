//  +build windows

package daemon

import (
	_ "github.com/docker/docker/daemon/graphdriver/windows"
)

func KillIfLxc(ID string) {
	// No-op on Windows as Lxc execution driver is not used.
}

func checkKernel() error {
	// No-op on Windows
	return nil
}
