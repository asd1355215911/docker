// +build windows

package builtins

import (
	"github.com/docker/docker/daemon/networkdriver/windows"
	"github.com/docker/docker/engine"
)

func daemon(eng *engine.Engine) error {
	return eng.Register("init_networkdriver", windows.InitDriver)
	//return nil
}
