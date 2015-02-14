//  +build linux

package daemon

func KillIfLxc(ID string) {
	lxc.KillLxc(ID, 9)
}
