// +build windows

package execdrivers

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/daemon/execdriver/windowsexec"
	"github.com/docker/docker/pkg/sysinfo"
)

func NewDriver(name, root, initPath string, sysInfo *sysinfo.SysInfo) (execdriver.Driver, error) {
	log.Debugln("Windows execdriver - NewDriver %s", name)
	switch name {
	case "windows":
		return windowsexec.NewDriver(root, initPath)
	}
	return nil, fmt.Errorf("unknown exec driver %s", name)
}
