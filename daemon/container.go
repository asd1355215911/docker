package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docker/libcontainer/devices"
	"github.com/docker/libcontainer/label"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/image"
	"github.com/docker/docker/links"
	"github.com/docker/docker/nat"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/broadcastwriter"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/networkfs/etchosts"
	"github.com/docker/docker/pkg/promise"
	"github.com/docker/docker/pkg/symlink"
	"github.com/docker/docker/runconfig"
	"github.com/docker/docker/utils"
)

const DefaultPathEnv = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

var (
	ErrNotATTY               = errors.New("The PTY is not a file")
	ErrNoTTY                 = errors.New("No PTY found")
	ErrContainerStart        = errors.New("The container failed to start. Unknown error")
	ErrContainerStartTimeout = errors.New("The container failed to start due to timed out.")
)

type StreamConfig struct {
	stdout    *broadcastwriter.BroadcastWriter
	stderr    *broadcastwriter.BroadcastWriter
	stdin     io.ReadCloser
	stdinPipe io.WriteCloser
}

type Container struct {
	*State `json:"State"` // Needed for remote api version <= 1.11
	root   string         // Path to the "home" of the container, including metadata.
	basefs string         // Path to the graphdriver mountpoint

	ID string

	Created time.Time

	Path string
	Args []string

	Config  *runconfig.Config
	ImageID string `json:"Image"`

	NetworkSettings *NetworkSettings

	ResolvConfPath string
	HostnamePath   string
	HostsPath      string
	Name           string
	Driver         string
	ExecDriver     string

	command *execdriver.Command
	StreamConfig

	daemon                   *Daemon
	MountLabel, ProcessLabel string

	// TODO Windows - Can factor out AppArmor
	AppArmorProfile string
	RestartCount    int
	UpdateDns       bool

	// Maps container paths to volume paths.  The key in this is the path to which
	// the volume is being mounted inside the container.  Value is the path of the
	// volume on disk
	Volumes map[string]string
	// Store rw/ro in a separate structure to preserve reverse-compatibility on-disk.
	// Easier than migrating older container configs :)
	VolumesRW  map[string]bool
	hostConfig *runconfig.HostConfig

	activeLinks        map[string]*links.Link
	monitor            *containerMonitor
	execCommands       *execStore
	AppliedVolumesFrom map[string]struct{}
}

func (container *Container) FromDisk() error {
	pth, err := container.jsonPath()
	if err != nil {
		return err
	}

	jsonSource, err := os.Open(pth)
	if err != nil {
		return err
	}
	defer jsonSource.Close()

	dec := json.NewDecoder(jsonSource)

	// Load container settings
	// udp broke compat of docker.PortMapping, but it's not used when loading a container, we can skip it
	if err := dec.Decode(container); err != nil && !strings.Contains(err.Error(), "docker.PortMapping") {
		return err
	}

	if err := label.ReserveLabel(container.ProcessLabel); err != nil {
		return err
	}
	return container.readHostConfig()
}

func (container *Container) toDisk() error {
	data, err := json.Marshal(container)
	if err != nil {
		return err
	}

	pth, err := container.jsonPath()
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(pth, data, 0666)
	if err != nil {
		return err
	}

	return container.WriteHostConfig()
}

func (container *Container) ToDisk() error {
	container.Lock()
	err := container.toDisk()
	container.Unlock()
	return err
}

func (container *Container) readHostConfig() error {
	container.hostConfig = &runconfig.HostConfig{}
	// If the hostconfig file does not exist, do not read it.
	// (We still have to initialize container.hostConfig,
	// but that's OK, since we just did that above.)
	pth, err := container.hostConfigPath()
	if err != nil {
		return err
	}

	_, err = os.Stat(pth)
	if os.IsNotExist(err) {
		return nil
	}

	data, err := ioutil.ReadFile(pth)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, container.hostConfig)
}

func (container *Container) WriteHostConfig() error {
	data, err := json.Marshal(container.hostConfig)
	if err != nil {
		return err
	}

	pth, err := container.hostConfigPath()
	if err != nil {
		return err
	}

	return ioutil.WriteFile(pth, data, 0666)
}

func (container *Container) LogEvent(action string) {
	d := container.daemon
	if err := d.eng.Job("log", action, container.ID, d.Repositories().ImageName(container.ImageID)).Run(); err != nil {
		log.Errorf("Error logging event %s for %s: %s", action, container.ID, err)
	}
}

func (container *Container) getResourcePath(path string) (string, error) {
	cleanPath := filepath.Join("/", path)
	return symlink.FollowSymlinkInScope(filepath.Join(container.basefs, cleanPath), container.basefs)
}

func (container *Container) getRootResourcePath(path string) (string, error) {
	cleanPath := filepath.Join("/", path)
	return symlink.FollowSymlinkInScope(filepath.Join(container.root, cleanPath), container.root)
}

func populateCommand(c *Container, env []string) error {
	en := &execdriver.Network{
		Mtu:       c.daemon.config.Mtu,
		Interface: nil,
	}

	parts := strings.SplitN(string(c.hostConfig.NetworkMode), ":", 2)
	switch parts[0] {
	case "none":
	case "host":
		en.HostNetworking = true
	case "bridge", "": // empty string to support existing containers
		if !c.Config.NetworkDisabled {
			network := c.NetworkSettings
			en.Interface = &execdriver.NetworkInterface{
				Gateway:              network.Gateway,
				Bridge:               network.Bridge,
				IPAddress:            network.IPAddress,
				IPPrefixLen:          network.IPPrefixLen,
				MacAddress:           network.MacAddress,
				LinkLocalIPv6Address: network.LinkLocalIPv6Address,
				GlobalIPv6Address:    network.GlobalIPv6Address,
				GlobalIPv6PrefixLen:  network.GlobalIPv6PrefixLen,
				IPv6Gateway:          network.IPv6Gateway,
			}
		}
	case "container":
		nc, err := c.getNetworkedContainer()
		if err != nil {
			return err
		}
		en.ContainerID = nc.ID
	default:
		return fmt.Errorf("invalid network mode: %s", c.hostConfig.NetworkMode)
	}

	ipc := &execdriver.Ipc{}

	if c.hostConfig.IpcMode.IsContainer() {
		ic, err := c.getIpcContainer()
		if err != nil {
			return err
		}
		ipc.ContainerID = ic.ID
	} else {
		ipc.HostIpc = c.hostConfig.IpcMode.IsHost()
	}

	pid := &execdriver.Pid{}
	pid.HostPid = c.hostConfig.PidMode.IsHost()

	// Build lists of devices allowed and created within the container.
	userSpecifiedDevices := make([]*devices.Device, len(c.hostConfig.Devices))
	for i, deviceMapping := range c.hostConfig.Devices {
		device, err := devices.GetDevice(deviceMapping.PathOnHost, deviceMapping.CgroupPermissions)
		if err != nil {
			return fmt.Errorf("error gathering device information while adding custom device %q: %s", deviceMapping.PathOnHost, err)
		}
		device.Path = deviceMapping.PathInContainer
		userSpecifiedDevices[i] = device
	}
	allowedDevices := append(devices.DefaultAllowedDevices, userSpecifiedDevices...)

	autoCreatedDevices := append(devices.DefaultAutoCreatedDevices, userSpecifiedDevices...)

	// TODO: this can be removed after lxc-conf is fully deprecated
	lxcConfig, err := mergeLxcConfIntoOptions(c.hostConfig)
	if err != nil {
		return err
	}

	resources := &execdriver.Resources{
		Memory:     c.Config.Memory,
		MemorySwap: c.Config.MemorySwap,
		CpuShares:  c.Config.CpuShares,
		Cpuset:     c.Config.Cpuset,
	}

	processConfig := execdriver.ProcessConfig{
		Privileged: c.hostConfig.Privileged,
		Entrypoint: c.Path,
		Arguments:  c.Args,
		Tty:        c.Config.Tty,
		User:       c.Config.User,
	}

	// TODO WINDOWS BUGBUG
	//JJH Temp THIS LINE MUST GO BACK IN - NEEDS REFACTORING.   processConfig.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	processConfig.Env = env

	c.command = &execdriver.Command{
		ID:                 c.ID,
		Rootfs:             c.RootfsPath(),
		ReadonlyRootfs:     c.hostConfig.ReadonlyRootfs,
		InitPath:           "/.dockerinit",
		WorkingDir:         c.Config.WorkingDir,
		Network:            en,
		Ipc:                ipc,
		Pid:                pid,
		Resources:          resources,
		AllowedDevices:     allowedDevices,
		AutoCreatedDevices: autoCreatedDevices,
		CapAdd:             c.hostConfig.CapAdd,
		CapDrop:            c.hostConfig.CapDrop,
		ProcessConfig:      processConfig,
		ProcessLabel:       c.GetProcessLabel(),
		MountLabel:         c.GetMountLabel(),
		// TODO WINDOWS Can factor out LxcConfig and AppArmorProfile
		LxcConfig:       lxcConfig,
		AppArmorProfile: c.AppArmorProfile,
	}

	return nil
}

func (container *Container) Start() (err error) {
	container.Lock()
	defer container.Unlock()

	if container.Running {
		return nil
	}

	// if we encounter an error during start we need to ensure that any other
	// setup has been cleaned up properly
	defer func() {
		if err != nil {
			container.setError(err)
			// if no one else has set it, make sure we don't leave it at zero
			if container.ExitCode == 0 {
				container.ExitCode = 128
			}
			container.toDisk()
			container.cleanup()
		}
	}()

	// Note setupContainerDns is a no-op on the Windows daemon
	if err := container.setupContainerDns(); err != nil {
		return err
	}
	if err := container.Mount(); err != nil {
		return err
	}
	if err := container.initializeNetworking(); err != nil {
		return err
	}
	// Note updateParentHosts is a no-op on the Windows daemon
	if err := container.updateParentsHosts(); err != nil {
		return err
	}
	container.verifyDaemonSettings()
	if err := container.prepareVolumes(); err != nil {
		return err
	}
	linkedEnv, err := container.setupLinkedContainers()
	if err != nil {
		return err
	}
	if err := container.setupWorkingDirectory(); err != nil {
		return err
	}
	env := container.createDaemonEnvironment(linkedEnv)
	if err := populateCommand(container, env); err != nil {
		return err
	}
	if err := container.setupMounts(); err != nil {
		return err
	}

	return container.waitForStart()
}

func (container *Container) Run() error {
	if err := container.Start(); err != nil {
		return err
	}
	container.WaitStop(-1 * time.Second)
	return nil
}

func (container *Container) Output() (output []byte, err error) {
	pipe := container.StdoutPipe()
	defer pipe.Close()
	if err := container.Start(); err != nil {
		return nil, err
	}
	output, err = ioutil.ReadAll(pipe)
	container.WaitStop(-1 * time.Second)
	return output, err
}

// StreamConfig.StdinPipe returns a WriteCloser which can be used to feed data
// to the standard input of the container's active process.
// Container.StdoutPipe and Container.StderrPipe each return a ReadCloser
// which can be used to retrieve the standard output (and error) generated
// by the container's active process. The output (and error) are actually
// copied and delivered to all StdoutPipe and StderrPipe consumers, using
// a kind of "broadcaster".

func (streamConfig *StreamConfig) StdinPipe() io.WriteCloser {
	return streamConfig.stdinPipe
}

func (streamConfig *StreamConfig) StdoutPipe() io.ReadCloser {
	reader, writer := io.Pipe()
	streamConfig.stdout.AddWriter(writer, "")
	return ioutils.NewBufReader(reader)
}

func (streamConfig *StreamConfig) StderrPipe() io.ReadCloser {
	reader, writer := io.Pipe()
	streamConfig.stderr.AddWriter(writer, "")
	return ioutils.NewBufReader(reader)
}

func (streamConfig *StreamConfig) StdoutLogPipe() io.ReadCloser {
	reader, writer := io.Pipe()
	streamConfig.stdout.AddWriter(writer, "stdout")
	return ioutils.NewBufReader(reader)
}

func (streamConfig *StreamConfig) StderrLogPipe() io.ReadCloser {
	reader, writer := io.Pipe()
	streamConfig.stderr.AddWriter(writer, "stderr")
	return ioutils.NewBufReader(reader)
}

func (container *Container) buildHostnameFile() error {
	hostnamePath, err := container.getRootResourcePath("hostname")
	if err != nil {
		return err
	}
	container.HostnamePath = hostnamePath

	if container.Config.Domainname != "" {
		return ioutil.WriteFile(container.HostnamePath, []byte(fmt.Sprintf("%s.%s\n", container.Config.Hostname, container.Config.Domainname)), 0644)
	}
	return ioutil.WriteFile(container.HostnamePath, []byte(container.Config.Hostname+"\n"), 0644)
}

func (container *Container) buildHostsFiles(IP string) error {

	hostsPath, err := container.getRootResourcePath("hosts")
	if err != nil {
		return err
	}
	container.HostsPath = hostsPath

	var extraContent []etchosts.Record

	children, err := container.daemon.Children(container.Name)
	if err != nil {
		return err
	}

	for linkAlias, child := range children {
		_, alias := path.Split(linkAlias)
		extraContent = append(extraContent, etchosts.Record{Hosts: alias, IP: child.NetworkSettings.IPAddress})
	}

	for _, extraHost := range container.hostConfig.ExtraHosts {
		// allow IPv6 addresses in extra hosts; only split on first ":"
		parts := strings.SplitN(extraHost, ":", 2)
		extraContent = append(extraContent, etchosts.Record{Hosts: parts[0], IP: parts[1]})
	}

	return etchosts.Build(container.HostsPath, IP, container.Config.Hostname, container.Config.Domainname, extraContent)
}

func (container *Container) buildHostnameAndHostsFiles(IP string) error {
	if err := container.buildHostnameFile(); err != nil {
		return err
	}

	return container.buildHostsFiles(IP)
}

func (container *Container) ReleaseNetwork() {
	if container.Config.NetworkDisabled || !container.hostConfig.NetworkMode.IsPrivate() {
		return
	}
	eng := container.daemon.eng

	job := eng.Job("release_interface", container.ID)
	job.SetenvBool("overrideShutdown", true)
	job.Run()
	container.NetworkSettings = &NetworkSettings{}
}

func (container *Container) isNetworkAllocated() bool {
	return container.NetworkSettings.IPAddress != ""
}

// cleanup releases any network resources allocated to the container along with any rules
// around how containers are linked together.  It also unmounts the container's root filesystem.
func (container *Container) cleanup() {
	container.ReleaseNetwork()

	// Disable all active links
	if container.activeLinks != nil {
		for _, link := range container.activeLinks {
			link.Disable()
		}
	}

	if err := container.Unmount(); err != nil {
		log.Errorf("%v: Failed to umount filesystem: %v", container.ID, err)
	}

	for _, eConfig := range container.execCommands.s {
		container.daemon.unregisterExecCommand(eConfig)
	}
}

func (container *Container) KillSig(sig int) error {
	log.Debugf("Sending %d to %s", sig, container.ID)
	container.Lock()
	defer container.Unlock()

	// We could unpause the container for them rather than returning this error
	if container.Paused {
		return fmt.Errorf("Container %s is paused. Unpause the container before stopping", container.ID)
	}

	if !container.Running {
		return nil
	}

	// signal to the monitor that it should not restart the container
	// after we send the kill signal
	container.monitor.ExitOnNext()

	// if the container is currently restarting we do not need to send the signal
	// to the process.  Telling the monitor that it should exit on it's next event
	// loop is enough
	if container.Restarting {
		return nil
	}

	return container.daemon.Kill(container, sig)
}

// Wrapper aroung KillSig() suppressing "no such process" error.
func (container *Container) killPossiblyDeadProcess(sig int) error {
	err := container.KillSig(sig)
	if err == syscall.ESRCH {
		log.Debugf("Cannot kill process (pid=%d) with signal %d: no such process.", container.GetPid(), sig)
		return nil
	}
	return err
}

func (container *Container) Pause() error {
	if container.IsPaused() {
		return fmt.Errorf("Container %s is already paused", container.ID)
	}
	if !container.IsRunning() {
		return fmt.Errorf("Container %s is not running", container.ID)
	}
	return container.daemon.Pause(container)
}

func (container *Container) Unpause() error {
	if !container.IsPaused() {
		return fmt.Errorf("Container %s is not paused", container.ID)
	}
	if !container.IsRunning() {
		return fmt.Errorf("Container %s is not running", container.ID)
	}
	return container.daemon.Unpause(container)
}

func (container *Container) Stop(seconds int) error {
	if !container.IsRunning() {
		return nil
	}

	// 1. Send a SIGTERM
	if err := container.killPossiblyDeadProcess(15); err != nil {
		log.Infof("Failed to send SIGTERM to the process, force killing")
		if err := container.killPossiblyDeadProcess(9); err != nil {
			return err
		}
	}

	// 2. Wait for the process to exit on its own
	if _, err := container.WaitStop(time.Duration(seconds) * time.Second); err != nil {
		log.Infof("Container %v failed to exit within %d seconds of SIGTERM - using the force", container.ID, seconds)
		// 3. If it doesn't, then send SIGKILL
		if err := container.Kill(); err != nil {
			container.WaitStop(-1 * time.Second)
			return err
		}
	}
	return nil
}

func (container *Container) Restart(seconds int) error {
	// Avoid unnecessarily unmounting and then directly mounting
	// the container when the container stops and then starts
	// again
	if err := container.Mount(); err == nil {
		defer container.Unmount()
	}

	if err := container.Stop(seconds); err != nil {
		return err
	}
	return container.Start()
}

func (container *Container) Resize(h, w int) error {
	if !container.IsRunning() {
		return fmt.Errorf("Cannot resize container %s, container is not running", container.ID)
	}
	return container.command.ProcessConfig.Terminal.Resize(h, w)
}

func (container *Container) ExportRw() (archive.Archive, error) {
	if err := container.Mount(); err != nil {
		return nil, err
	}
	if container.daemon == nil {
		return nil, fmt.Errorf("Can't load storage driver for unregistered container %s", container.ID)
	}
	archive, err := container.daemon.Diff(container)
	if err != nil {
		container.Unmount()
		return nil, err
	}
	return ioutils.NewReadCloserWrapper(archive, func() error {
			err := archive.Close()
			container.Unmount()
			return err
		}),
		nil
}

func (container *Container) Export() (archive.Archive, error) {
	if err := container.Mount(); err != nil {
		return nil, err
	}

	archive, err := archive.Tar(container.basefs, archive.Uncompressed)
	if err != nil {
		container.Unmount()
		return nil, err
	}
	return ioutils.NewReadCloserWrapper(archive, func() error {
			err := archive.Close()
			container.Unmount()
			return err
		}),
		nil
}

func (container *Container) Mount() error {
	return container.daemon.Mount(container)
}

func (container *Container) changes() ([]archive.Change, error) {
	return container.daemon.Changes(container)
}

func (container *Container) Changes() ([]archive.Change, error) {
	container.Lock()
	defer container.Unlock()
	return container.changes()
}

func (container *Container) GetImage() (*image.Image, error) {
	if container.daemon == nil {
		return nil, fmt.Errorf("Can't get image of unregistered container")
	}
	return container.daemon.graph.Get(container.ImageID)
}

func (container *Container) Unmount() error {
	return container.daemon.Unmount(container)
}

func (container *Container) logPath(name string) (string, error) {
	return container.getRootResourcePath(fmt.Sprintf("%s-%s.log", container.ID, name))
}

func (container *Container) ReadLog(name string) (io.Reader, error) {
	pth, err := container.logPath(name)
	if err != nil {
		return nil, err
	}
	return os.Open(pth)
}

func (container *Container) hostConfigPath() (string, error) {
	return container.getRootResourcePath("hostconfig.json")
}

func (container *Container) jsonPath() (string, error) {
	return container.getRootResourcePath("config.json")
}

// This method must be exported to be used from the lxc template
// This directory is only usable when the container is running
func (container *Container) RootfsPath() string {
	return container.basefs
}

func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("Invalid empty id")
	}
	return nil
}

// GetSize, return real size, virtual size
func (container *Container) GetSize() (int64, int64) {
	var (
		sizeRw, sizeRootfs int64
		err                error
		driver             = container.daemon.driver
	)

	if err := container.Mount(); err != nil {
		log.Errorf("Warning: failed to compute size of container rootfs %s: %s", container.ID, err)
		return sizeRw, sizeRootfs
	}
	defer container.Unmount()

	initID := fmt.Sprintf("%s-init", container.ID)
	sizeRw, err = driver.DiffSize(container.ID, initID)
	if err != nil {
		log.Errorf("Warning: driver %s couldn't return diff size of container %s: %s", driver, container.ID, err)
		// FIXME: GetSize should return an error. Not changing it now in case
		// there is a side-effect.
		sizeRw = -1
	}

	// TODO WINDOWS BUGBUG. FIXME
	//JJH Temp
	//if _, err = os.Stat(container.basefs); err != nil {
	//	if sizeRootfs, err = utils.TreeSize(container.basefs); err != nil {
	//		sizeRootfs = -1
	//	}
	//}
	return sizeRw, sizeRootfs
}

func (container *Container) Copy(resource string) (io.ReadCloser, error) {
	if err := container.Mount(); err != nil {
		return nil, err
	}

	basePath, err := container.getResourcePath(resource)
	if err != nil {
		container.Unmount()
		return nil, err
	}

	// Check if this is actually in a volume
	for _, mnt := range container.VolumeMounts() {
		if len(mnt.MountToPath) > 0 && strings.HasPrefix(resource, mnt.MountToPath[1:]) {
			return mnt.Export(resource)
		}
	}

	stat, err := os.Stat(basePath)
	if err != nil {
		container.Unmount()
		return nil, err
	}
	var filter []string
	if !stat.IsDir() {
		d, f := path.Split(basePath)
		basePath = d
		filter = []string{f}
	} else {
		filter = []string{path.Base(basePath)}
		basePath = path.Dir(basePath)
	}

	archive, err := archive.TarWithOptions(basePath, &archive.TarOptions{
		Compression:  archive.Uncompressed,
		IncludeFiles: filter,
	})
	if err != nil {
		container.Unmount()
		return nil, err
	}
	return ioutils.NewReadCloserWrapper(archive, func() error {
			err := archive.Close()
			container.Unmount()
			return err
		}),
		nil
}

// Returns true if the container exposes a certain port
func (container *Container) Exposes(p nat.Port) bool {
	_, exists := container.Config.ExposedPorts[p]
	return exists
}

func (container *Container) GetPtyMaster() (*os.File, error) {
	ttyConsole, ok := container.command.ProcessConfig.Terminal.(execdriver.TtyTerminal)
	if !ok {
		return nil, ErrNoTTY
	}
	return ttyConsole.Master(), nil
}

func (container *Container) HostConfig() *runconfig.HostConfig {
	container.Lock()
	res := container.hostConfig
	container.Unlock()
	return res
}

func (container *Container) SetHostConfig(hostConfig *runconfig.HostConfig) {
	container.Lock()
	container.hostConfig = hostConfig
	container.Unlock()
}

func (container *Container) DisableLink(name string) {
	if container.activeLinks != nil {
		if link, exists := container.activeLinks[name]; exists {
			link.Disable()
		} else {
			log.Debugf("Could not find active link for %s", name)
		}
	}
}

// called when the host's resolv.conf changes to check whether container's resolv.conf
// is unchanged by the container "user" since container start: if unchanged, the
// container's resolv.conf will be updated to match the host's new resolv.conf
func (container *Container) updateResolvConf(updatedResolvConf []byte, newResolvHash string) error {

	if container.ResolvConfPath == "" {
		return nil
	}
	if container.Running {
		//set a marker in the hostConfig to update on next start/restart
		container.UpdateDns = true
		return nil
	}

	resolvHashFile := container.ResolvConfPath + ".hash"

	//read the container's current resolv.conf and compute the hash
	resolvBytes, err := ioutil.ReadFile(container.ResolvConfPath)
	if err != nil {
		return err
	}
	curHash, err := utils.HashData(bytes.NewReader(resolvBytes))
	if err != nil {
		return err
	}

	//read the hash from the last time we wrote resolv.conf in the container
	hashBytes, err := ioutil.ReadFile(resolvHashFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// backwards compat: if no hash file exists, this container pre-existed from
		// a Docker daemon that didn't contain this update feature. Given we can't know
		// if the user has modified the resolv.conf since container start time, safer
		// to just never update the container's resolv.conf during it's lifetime which
		// we can control by setting hashBytes to an empty string
		hashBytes = []byte("")
	}

	//if the user has not modified the resolv.conf of the container since we wrote it last
	//we will replace it with the updated resolv.conf from the host
	if string(hashBytes) == curHash {
		log.Debugf("replacing %q with updated host resolv.conf", container.ResolvConfPath)

		// for atomic updates to these files, use temporary files with os.Rename:
		dir := path.Dir(container.ResolvConfPath)
		tmpHashFile, err := ioutil.TempFile(dir, "hash")
		if err != nil {
			return err
		}
		tmpResolvFile, err := ioutil.TempFile(dir, "resolv")
		if err != nil {
			return err
		}

		// write the updates to the temp files
		if err = ioutil.WriteFile(tmpHashFile.Name(), []byte(newResolvHash), 0644); err != nil {
			return err
		}
		if err = ioutil.WriteFile(tmpResolvFile.Name(), updatedResolvConf, 0644); err != nil {
			return err
		}

		// rename the temp files for atomic replace
		if err = os.Rename(tmpHashFile.Name(), resolvHashFile); err != nil {
			return err
		}
		return os.Rename(tmpResolvFile.Name(), container.ResolvConfPath)
	}
	return nil
}

func (container *Container) initializeNetworking() error {
	var err error
	if container.hostConfig.NetworkMode.IsHost() {
		container.Config.Hostname, err = os.Hostname()
		if err != nil {
			return err
		}

		parts := strings.SplitN(container.Config.Hostname, ".", 2)
		if len(parts) > 1 {
			container.Config.Hostname = parts[0]
			container.Config.Domainname = parts[1]
		}

		content, err := ioutil.ReadFile("/etc/hosts")
		if os.IsNotExist(err) {
			return container.buildHostnameAndHostsFiles("")
		} else if err != nil {
			return err
		}

		if err := container.buildHostnameFile(); err != nil {
			return err
		}

		hostsPath, err := container.getRootResourcePath("hosts")
		if err != nil {
			return err
		}
		container.HostsPath = hostsPath

		return ioutil.WriteFile(container.HostsPath, content, 0644)
	}
	if container.hostConfig.NetworkMode.IsContainer() {
		// we need to get the hosts files from the container to join
		nc, err := container.getNetworkedContainer()
		if err != nil {
			return err
		}
		container.HostsPath = nc.HostsPath
		container.ResolvConfPath = nc.ResolvConfPath
		container.Config.Hostname = nc.Config.Hostname
		container.Config.Domainname = nc.Config.Domainname
		return nil
	}
	if container.daemon.config.DisableNetwork {
		container.Config.NetworkDisabled = true
		return container.buildHostnameAndHostsFiles("127.0.1.1")
	}
	if err := container.AllocateNetwork(); err != nil {
		return err
	}
	return container.buildHostnameAndHostsFiles(container.NetworkSettings.IPAddress)
}

func (container *Container) setupLinkedContainers() ([]string, error) {
	var (
		env    []string
		daemon = container.daemon
	)
	children, err := daemon.Children(container.Name)
	if err != nil {
		return nil, err
	}

	if len(children) > 0 {
		container.activeLinks = make(map[string]*links.Link, len(children))

		// If we encounter an error make sure that we rollback any network
		// config and ip table changes
		rollback := func() {
			for _, link := range container.activeLinks {
				link.Disable()
			}
			container.activeLinks = nil
		}

		for linkAlias, child := range children {
			if !child.IsRunning() {
				return nil, fmt.Errorf("Cannot link to a non running container: %s AS %s", child.Name, linkAlias)
			}

			link, err := links.NewLink(
				container.NetworkSettings.IPAddress,
				child.NetworkSettings.IPAddress,
				linkAlias,
				child.Config.Env,
				child.Config.ExposedPorts,
				daemon.eng)

			if err != nil {
				rollback()
				return nil, err
			}

			container.activeLinks[link.Alias()] = link
			if err := link.Enable(); err != nil {
				rollback()
				return nil, err
			}

			for _, envVar := range link.ToEnv() {
				env = append(env, envVar)
			}
		}
	}
	return env, nil
}

func (container *Container) createDaemonEnvironment(linkedEnv []string) []string {
	// if a domain name was specified, append it to the hostname (see #7851)
	fullHostname := container.Config.Hostname
	if container.Config.Domainname != "" {
		fullHostname = fmt.Sprintf("%s.%s", fullHostname, container.Config.Domainname)
	}
	// Setup environment
	env := []string{
		"PATH=" + DefaultPathEnv,
		"HOSTNAME=" + fullHostname,
		// Note: we don't set HOME here because it'll get autoset intelligently
		// based on the value of USER inside dockerinit, but only if it isn't
		// set already (ie, that can be overridden by setting HOME via -e or ENV
		// in a Dockerfile).
	}
	if container.Config.Tty {
		env = append(env, "TERM=xterm")
	}
	env = append(env, linkedEnv...)
	// because the env on the container can override certain default values
	// we need to replace the 'env' keys where they match and append anything
	// else.
	env = utils.ReplaceOrAppendEnvValues(env, container.Config.Env)

	return env
}

func (container *Container) setupWorkingDirectory() error {
	if container.Config.WorkingDir != "" {
		container.Config.WorkingDir = path.Clean(container.Config.WorkingDir)

		pth, err := container.getResourcePath(container.Config.WorkingDir)
		if err != nil {
			return err
		}

		pthInfo, err := os.Stat(pth)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}

			if err := os.MkdirAll(pth, 0755); err != nil {
				return err
			}
		}
		if pthInfo != nil && !pthInfo.IsDir() {
			return fmt.Errorf("Cannot mkdir: %s is not a directory", container.Config.WorkingDir)
		}
	}
	return nil
}

func (container *Container) startLoggingToDisk() error {
	// Setup logging of stdout and stderr to disk
	pth, err := container.logPath("json")
	if err != nil {
		return err
	}

	if err := container.daemon.LogToDisk(container.stdout, pth, "stdout"); err != nil {
		return err
	}

	if err := container.daemon.LogToDisk(container.stderr, pth, "stderr"); err != nil {
		return err
	}

	return nil
}

func (container *Container) waitForStart() error {
	container.monitor = newContainerMonitor(container, container.hostConfig.RestartPolicy)

	// block until we either receive an error from the initial start of the container's
	// process or until the process is running in the container
	select {
	case <-container.monitor.startSignal:
	case err := <-promise.Go(container.monitor.Start):
		return err
	}

	return nil
}

func (container *Container) GetProcessLabel() string {
	// even if we have a process label return "" if we are running
	// in privileged mode
	if container.hostConfig.Privileged {
		return ""
	}
	return container.ProcessLabel
}

func (container *Container) GetMountLabel() string {
	if container.hostConfig.Privileged {
		return ""
	}
	return container.MountLabel
}

func (container *Container) getIpcContainer() (*Container, error) {
	containerID := container.hostConfig.IpcMode.Container()
	c, err := container.daemon.Get(containerID)
	if err != nil {
		return nil, err
	}
	if !c.IsRunning() {
		return nil, fmt.Errorf("cannot join IPC of a non running container: %s", containerID)
	}
	return c, nil
}

func (container *Container) getNetworkedContainer() (*Container, error) {
	parts := strings.SplitN(string(container.hostConfig.NetworkMode), ":", 2)
	switch parts[0] {
	case "container":
		if len(parts) != 2 {
			return nil, fmt.Errorf("no container specified to join network")
		}
		nc, err := container.daemon.Get(parts[1])
		if err != nil {
			return nil, err
		}
		if !nc.IsRunning() {
			return nil, fmt.Errorf("cannot join network of a non running container: %s", parts[1])
		}
		return nc, nil
	default:
		return nil, fmt.Errorf("network mode not set to container")
	}
}

func (container *Container) Stats() (*execdriver.ResourceStats, error) {
	return container.daemon.Stats(container)
}
