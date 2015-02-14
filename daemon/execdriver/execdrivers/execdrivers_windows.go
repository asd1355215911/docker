// +build windows

package execdrivers

import (
	"fmt"
	"path"

	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/daemon/execdriver/windowsexec"
)

func NewDriver(name, root, initPath string, sysInfo *sysinfo.SysInfo) (execdriver.Driver, error) {
	switch name {
	case "windows":
		return windowsexec.NewDriver(root, initPath)
	}
	return nil, fmt.Errorf("unknown exec driver %s", name)
}
