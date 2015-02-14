// +build !windows

package builtins

import (
	"runtime"

	"github.com/docker/docker/api"
	apiserver "github.com/docker/docker/api/server"
	"github.com/docker/docker/daemon/networkdriver/bridge"
	"github.com/docker/docker/dockerversion"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/events"
	"github.com/docker/docker/pkg/parsers/kernel"
)

func daemon(eng *engine.Engine) error {
	return eng.Register("init_networkdriver", bridge.InitDriver)
}
