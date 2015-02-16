// +build windows

package windowsexec

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	//	"strings"

	//	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/pkg/term"
)

const (
	DriverName = "windowsexec"
	Version    = "0.1"
)

//type activeContainer struct {
//	container *libcontainer.Config
//	cmd       *exec.Cmd
//}

type driver struct {
	root     string
	initPath string
}

type info struct {
	ID     string
	driver *driver
}

func NewDriver(root, initPath string) (*driver, error) {

	return &driver{
		root:     root,
		initPath: initPath,
	}, nil
}

func (d *driver) Run(c *execdriver.Command, pipes *execdriver.Pipes, startCallback execdriver.StartCallback) (execdriver.ExitStatus, error) {
	// Not yet implemented
	return execdriver.ExitStatus{ExitCode: -1}, nil
}

func (d *driver) Kill(p *execdriver.Command, sig int) error {
	return fmt.Errorf("windowsexec: Kill() not implemented")
}

func (d *driver) Pause(c *execdriver.Command) error {
	//active := d.activeContainers[c.ID]
	//if active == nil {
	//	return fmt.Errorf("active container for %s does not exist", c.ID)
	//}
	//active.container.Cgroups.Freezer = "FROZEN"
	//if systemd.UseSystemd() {
	//	return systemd.Freeze(active.container.Cgroups, active.container.Cgroups.Freezer)
	//}
	//return fs.Freeze(active.container.Cgroups, active.container.Cgroups.Freezer)
	return fmt.Errorf("windowsexec: Pause() not implemented")
}

func (d *driver) Unpause(c *execdriver.Command) error {
	//active := d.activeContainers[c.ID]
	//if active == nil {
	//	return fmt.Errorf("active container for %s does not exist", c.ID)
	//}
	return fmt.Errorf("windowsexec: Unpause() not implemented")
}

func (d *driver) Terminate(p *execdriver.Command) error {
	return fmt.Errorf("windowsexec: Terminate() not implemented")
}

func (i *info) IsRunning() bool {
	var running bool

	//output, err := i.driver.getInfo(i.ID)
	//if err != nil {
	//	log.Errorf("Error getting info for Windows container %s: %s (%s)", i.ID, err, output)
	//	return false
	//}
	//if strings.Contains(string(output), "RUNNING") {
	//	running = true
	//}

	running = true

	return running
}

func (d *driver) Info(id string) execdriver.Info {
	return &info{
		ID:     id,
		driver: d,
	}
}

func (d *driver) Name() string {
	return fmt.Sprintf("%s-%s", DriverName, Version)
}

func (d *driver) GetPidsForContainer(id string) ([]int, error) {
	return nil, fmt.Errorf("GetPidsForContainer: GetPidsForContainer() not implemented")
}

func (d *driver) Clean(id string) error {
	return fmt.Errorf("windowsexec: Clean() not implemented")
}

func (d *driver) Stats(id string) (*execdriver.ResourceStats, error) {
	return nil, fmt.Errorf("windowsexec: Stats() not implemented")
}

type TtyConsole struct {
	MasterPty *os.File
}

func NewTtyConsole(processConfig *execdriver.ProcessConfig, pipes *execdriver.Pipes) (*TtyConsole, error) {
	return nil, nil
}

func (t *TtyConsole) Master() *os.File {
	return t.MasterPty
}

func (t *TtyConsole) Resize(h, w int) error {
	return term.SetWinsize(t.MasterPty.Fd(), &term.Winsize{Height: uint16(h), Width: uint16(w)})
}

func (t *TtyConsole) AttachPipes(command *exec.Cmd, pipes *execdriver.Pipes) error {
	go func() {
		if wb, ok := pipes.Stdout.(interface {
			CloseWriters() error
		}); ok {
			defer wb.CloseWriters()
		}

		io.Copy(pipes.Stdout, t.MasterPty)
	}()

	if pipes.Stdin != nil {
		go func() {
			io.Copy(t.MasterPty, pipes.Stdin)

			pipes.Stdin.Close()
		}()
	}

	return nil
}

func (t *TtyConsole) Close() error {
	return t.MasterPty.Close()
}
