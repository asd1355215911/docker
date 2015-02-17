// +build !windows

package builtins

import (
	"github.com/docker/docker/daemon/networkdriver/bridge"
	"github.com/docker/docker/engine"
)

func daemon(eng *engine.Engine) error {
	return eng.Register("init_networkdriver", bridge.InitDriver)
}
