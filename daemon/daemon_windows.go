//  +build windows

package daemon

func KillIfLxc(ID string) {
	// No-op on Windows as Lxc execution driver is not used.
}
