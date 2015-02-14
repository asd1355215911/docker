// +build !exclude_graphdriver_overlay,!windows

package daemon

import (
	_ "github.com/docker/docker/daemon/graphdriver/overlay"
)
