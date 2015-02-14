// +build windows

package builtins

import (
	"github.com/docker/docker/engine"
)

func daemon(eng *engine.Engine) error {
	// TODO Windows Add a networking driver
	//return eng.Register("init_networkdriver", bridge.InitDriver)
	return nil
}
