//  +build linux

package daemon

import (
	"github.com/docker/docker/daemon/execdriver/lxc"
	_ "github.com/docker/docker/daemon/graphdriver/vfs"
	_ "github.com/docker/docker/daemon/networkdriver/bridge"
)

func KillIfLxc(ID string) {
	lxc.KillLxc(ID, 9)
}
