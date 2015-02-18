package daemon

import (
	"github.com/docker/docker/daemon/networkdriver"
)

const (
	defaultNetworkMtu    = 1500
	disableNetworkBridge = "none"
)

func getDefaultNetworkMtu() int {
	if iface, err := networkdriver.GetDefaultRouteIface(); err == nil {
		return iface.MTU
	}
	return defaultNetworkMtu
}
