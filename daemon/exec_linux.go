// +build linux

package daemon

import (
	"strings"

	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/daemon/execdriver/lxc"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/runconfig"
	"github.com/docker/docker/utils"
)

func (d *Daemon) ContainerExecCreate(job *engine.Job) engine.Status {
	if len(job.Args) != 1 {
		return job.Errorf("Usage: %s [options] container command [args]", job.Name)
	}

	if strings.HasPrefix(d.execDriver.Name(), lxc.DriverName) {
		return job.Error(lxc.ErrExec)
	}

	var name = job.Args[0]

	container, err := d.getActiveContainer(name)
	if err != nil {
		return job.Error(err)
	}

	config, err := runconfig.ExecConfigFromJob(job)
	if err != nil {
		return job.Error(err)
	}

	entrypoint, args := d.getEntrypointAndArgs(nil, config.Cmd)

	processConfig := execdriver.ProcessConfig{
		Tty:        config.Tty,
		Entrypoint: entrypoint,
		Arguments:  args,
	}

	execConfig := &execConfig{
		ID:            utils.GenerateRandomID(),
		OpenStdin:     config.AttachStdin,
		OpenStdout:    config.AttachStdout,
		OpenStderr:    config.AttachStderr,
		StreamConfig:  StreamConfig{},
		ProcessConfig: processConfig,
		Container:     container,
		Running:       false,
	}

	container.LogEvent("exec_create: " + execConfig.ProcessConfig.Entrypoint + " " + strings.Join(execConfig.ProcessConfig.Arguments, " "))

	d.registerExecCommand(execConfig)

	job.Printf("%s\n", execConfig.ID)

	return engine.StatusOK
}
