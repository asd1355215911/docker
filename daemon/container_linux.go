package daemon

import (
	"bytes"
	"syscall"
	"time"

	//TODO Windows. Stefan - there'll be a bunch of Linux includes here.

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/pkg/networkfs/resolvconf"
	"github.com/docker/docker/utils"
)

func (container *Container) prepareVolumes() error {
	if container.Volumes == nil || len(container.Volumes) == 0 {
		container.Volumes = make(map[string]string)
		container.VolumesRW = make(map[string]bool)
	}

	return container.createVolumes()
}

func (container *Container) Kill() error {
	if !container.IsRunning() {
		return nil
	}

	// 1. Send SIGKILL
	if err := container.killPossiblyDeadProcess(9); err != nil {
		return err
	}

	// 2. Wait for the process to die, in last resort, try to kill the process directly
	if _, err := container.WaitStop(10 * time.Second); err != nil {
		// Ensure that we don't kill ourselves
		if pid := container.GetPid(); pid != 0 {
			log.Infof("Container %s failed to exit within 10 seconds of kill - trying direct SIGKILL", utils.TruncateID(container.ID))
			if err := syscall.Kill(pid, 9); err != nil {
				if err != syscall.ESRCH {
					return err
				}
				log.Debugf("Cannot kill process (pid=%d) with signal 9: no such process.", pid)
			}
		}
	}

	container.WaitStop(-1 * time.Second)
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

	if err := container.setupContainerDns(); err != nil {
		return err
	}
	if err := container.Mount(); err != nil {
		return err
	}
	if err := container.initializeNetworking(); err != nil {
		return err
	}
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

func (container *Container) setupContainerDns() error {
	if container.ResolvConfPath != "" {
		// check if this is an existing container that needs DNS update:
		if container.UpdateDns {
			// read the host's resolv.conf, get the hash and call updateResolvConf
			log.Debugf("Check container (%s) for update to resolv.conf - UpdateDns flag was set", container.ID)
			latestResolvConf, latestHash := resolvconf.GetLastModified()

			// clean container resolv.conf re: localhost nameservers and IPv6 NS (if IPv6 disabled)
			updatedResolvConf, modified := resolvconf.FilterResolvDns(latestResolvConf, container.daemon.config.EnableIPv6)
			if modified {
				// changes have occurred during resolv.conf localhost cleanup: generate an updated hash
				newHash, err := utils.HashData(bytes.NewReader(updatedResolvConf))
				if err != nil {
					return err
				}
				latestHash = newHash
			}

			if err := container.updateResolvConf(updatedResolvConf, latestHash); err != nil {
				return err
			}
			// successful update of the restarting container; set the flag off
			container.UpdateDns = false
		}
		return nil
	}

	var (
		config = container.hostConfig
		daemon = container.daemon
	)

	resolvConf, err := resolvconf.Get()
	if err != nil {
		return err
	}
	container.ResolvConfPath, err = container.getRootResourcePath("resolv.conf")
	if err != nil {
		return err
	}

	if config.NetworkMode != "host" {
		// check configurations for any container/daemon dns settings
		if len(config.Dns) > 0 || len(daemon.config.Dns) > 0 || len(config.DnsSearch) > 0 || len(daemon.config.DnsSearch) > 0 {
			var (
				dns       = resolvconf.GetNameservers(resolvConf)
				dnsSearch = resolvconf.GetSearchDomains(resolvConf)
			)
			if len(config.Dns) > 0 {
				dns = config.Dns
			} else if len(daemon.config.Dns) > 0 {
				dns = daemon.config.Dns
			}
			if len(config.DnsSearch) > 0 {
				dnsSearch = config.DnsSearch
			} else if len(daemon.config.DnsSearch) > 0 {
				dnsSearch = daemon.config.DnsSearch
			}
			return resolvconf.Build(container.ResolvConfPath, dns, dnsSearch)
		}

		// replace any localhost/127.*, and remove IPv6 nameservers if IPv6 disabled in daemon
		resolvConf, _ = resolvconf.FilterResolvDns(resolvConf, daemon.config.EnableIPv6)
	}
	//get a sha256 hash of the resolv conf at this point so we can check
	//for changes when the host resolv.conf changes (e.g. network update)
	resolvHash, err := utils.HashData(bytes.NewReader(resolvConf))
	if err != nil {
		return err
	}
	resolvHashFile := container.ResolvConfPath + ".hash"
	if err = ioutil.WriteFile(resolvHashFile, []byte(resolvHash), 0644); err != nil {
		return err
	}
	return ioutil.WriteFile(container.ResolvConfPath, resolvConf, 0644)
}

func (container *Container) updateParentsHosts() error {
	refs := container.daemon.ContainerGraph().RefPaths(container.ID)
	for _, ref := range refs {
		if ref.ParentID == "0" {
			continue
		}

		c, err := container.daemon.Get(ref.ParentID)
		if err != nil {
			log.Error(err)
		}

		if c != nil && !container.daemon.config.DisableNetwork && container.hostConfig.NetworkMode.IsPrivate() {
			log.Debugf("Update /etc/hosts of %s for alias %s with ip %s", c.ID, ref.Name, container.NetworkSettings.IPAddress)
			if err := etchosts.Update(c.HostsPath, container.NetworkSettings.IPAddress, ref.Name); err != nil {
				log.Errorf("Failed to update /etc/hosts in parent container %s for alias %s: %v", c.ID, ref.Name, err)
			}
		}
	}
	return nil
}

// Make sure the config is compatible with the current kernel
func (container *Container) verifyDaemonSettings() {
	if container.Config.Memory > 0 && !container.daemon.sysInfo.MemoryLimit {
		log.Infof("WARNING: Your kernel does not support memory limit capabilities. Limitation discarded.")
		container.Config.Memory = 0
	}
	if container.Config.Memory > 0 && !container.daemon.sysInfo.SwapLimit {
		log.Infof("WARNING: Your kernel does not support swap limit capabilities. Limitation discarded.")
		container.Config.MemorySwap = -1
	}
	if container.daemon.sysInfo.IPv4ForwardingDisabled {
		log.Infof("WARNING: IPv4 forwarding is disabled. Networking will not work")
	}
}

func (container *Container) AllocateNetwork() error {
	mode := container.hostConfig.NetworkMode
	if container.Config.NetworkDisabled || !mode.IsPrivate() {
		return nil
	}

	var (
		env *engine.Env
		err error
		eng = container.daemon.eng
	)

	job := eng.Job("allocate_interface", container.ID)
	job.Setenv("RequestedMac", container.Config.MacAddress)
	if env, err = job.Stdout.AddEnv(); err != nil {
		return err
	}
	if err = job.Run(); err != nil {
		return err
	}

	// Error handling: At this point, the interface is allocated so we have to
	// make sure that it is always released in case of error, otherwise we
	// might leak resources.

	if container.Config.PortSpecs != nil {
		if err = migratePortMappings(container.Config, container.hostConfig); err != nil {
			eng.Job("release_interface", container.ID).Run()
			return err
		}
		container.Config.PortSpecs = nil
		if err = container.WriteHostConfig(); err != nil {
			eng.Job("release_interface", container.ID).Run()
			return err
		}
	}

	var (
		portSpecs = make(nat.PortSet)
		bindings  = make(nat.PortMap)
	)

	if container.Config.ExposedPorts != nil {
		portSpecs = container.Config.ExposedPorts
	}

	if container.hostConfig.PortBindings != nil {
		for p, b := range container.hostConfig.PortBindings {
			bindings[p] = []nat.PortBinding{}
			for _, bb := range b {
				bindings[p] = append(bindings[p], nat.PortBinding{
					HostIp:   bb.HostIp,
					HostPort: bb.HostPort,
				})
			}
		}
	}

	container.NetworkSettings.PortMapping = nil

	for port := range portSpecs {
		if err = container.allocatePort(eng, port, bindings); err != nil {
			eng.Job("release_interface", container.ID).Run()
			return err
		}
	}
	container.WriteHostConfig()

	container.NetworkSettings.Ports = bindings
	container.NetworkSettings.Bridge = env.Get("Bridge")
	container.NetworkSettings.IPAddress = env.Get("IP")
	container.NetworkSettings.IPPrefixLen = env.GetInt("IPPrefixLen")
	container.NetworkSettings.MacAddress = env.Get("MacAddress")
	container.NetworkSettings.Gateway = env.Get("Gateway")
	container.NetworkSettings.LinkLocalIPv6Address = env.Get("LinkLocalIPv6")
	container.NetworkSettings.LinkLocalIPv6PrefixLen = 64
	container.NetworkSettings.GlobalIPv6Address = env.Get("GlobalIPv6")
	container.NetworkSettings.GlobalIPv6PrefixLen = env.GetInt("GlobalIPv6PrefixLen")
	container.NetworkSettings.IPv6Gateway = env.Get("IPv6Gateway")

	return nil
}

func (container *Container) allocatePort(eng *engine.Engine, port nat.Port, bindings nat.PortMap) error {
	binding := bindings[port]
	if container.hostConfig.PublishAllPorts && len(binding) == 0 {
		binding = append(binding, nat.PortBinding{})
	}

	for i := 0; i < len(binding); i++ {
		b := binding[i]

		job := eng.Job("allocate_port", container.ID)
		job.Setenv("HostIP", b.HostIp)
		job.Setenv("HostPort", b.HostPort)
		job.Setenv("Proto", port.Proto())
		job.Setenv("ContainerPort", port.Port())

		portEnv, err := job.Stdout.AddEnv()
		if err != nil {
			return err
		}
		if err := job.Run(); err != nil {
			return err
		}
		b.HostIp = portEnv.Get("HostIP")
		b.HostPort = portEnv.Get("HostPort")

		binding[i] = b
	}
	bindings[port] = binding
	return nil
}

func (container *Container) RestoreNetwork() error {
	mode := container.hostConfig.NetworkMode
	// Don't attempt a restore if we previously didn't allocate networking.
	// This might be a legacy container with no network allocated, in which case the
	// allocation will happen once and for all at start.
	if !container.isNetworkAllocated() || container.Config.NetworkDisabled || !mode.IsPrivate() {
		return nil
	}

	eng := container.daemon.eng

	// Re-allocate the interface with the same IP and MAC address.
	job := eng.Job("allocate_interface", container.ID)
	job.Setenv("RequestedIP", container.NetworkSettings.IPAddress)
	job.Setenv("RequestedMac", container.NetworkSettings.MacAddress)
	if err := job.Run(); err != nil {
		return err
	}

	// Re-allocate any previously allocated ports.
	for port := range container.NetworkSettings.Ports {
		if err := container.allocatePort(eng, port, container.NetworkSettings.Ports); err != nil {
			return err
		}
	}
	return nil
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

	processConfig.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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
		LxcConfig:          lxcConfig,
		AppArmorProfile:    c.AppArmorProfile,
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

	if _, err = os.Stat(container.basefs); err != nil {
		if sizeRootfs, err = utils.TreeSize(container.basefs); err != nil {
			sizeRootfs = -1
		}
	}
	return sizeRw, sizeRootfs
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
