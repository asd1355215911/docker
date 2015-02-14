//  +build linux

package daemon

import (
	"github.com/docker/docker/daemon/execdriver/lxc"
)

func KillIfLxc(ID string) {
	lxc.KillLxc(ID, 9)
}
