// +build !exclude_graphdriver_devicemapper,!windows

package daemon

import (
	_ "github.com/docker/docker/daemon/graphdriver/devmapper"
)
