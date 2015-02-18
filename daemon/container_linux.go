package daemon

import (
	"syscall"
	"time"

	//TODO Windows. Stefan - there'll be a bunch of Linux includes here.

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/pkg/networkfs/resolvconf"
	"github.com/docker/docker/utils"
)

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
