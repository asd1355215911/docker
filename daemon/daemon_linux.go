//  +build linux

package daemon

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api"
	"github.com/docker/docker/daemon/execdriver"
	"github.com/docker/docker/daemon/execdriver/execdrivers"
	"github.com/docker/docker/daemon/execdriver/lxc"
	_ "github.com/docker/docker/daemon/graphdriver/vfs"
	_ "github.com/docker/docker/daemon/networkdriver/bridge"
	"github.com/docker/docker/daemon/networkdriver/portallocator"
	"github.com/docker/docker/dockerversion"
	"github.com/docker/docker/pkg/networkfs/resolvconf"
	"github.com/docker/docker/pkg/parsers/kernel"
	"github.com/docker/docker/runconfig"
	"github.com/docker/libcontainer/label"
	"github.com/go-fsnotify/fsnotify"
)

func KillIfLxc(ID string) {
	lxc.KillLxc(ID, 9)
}

func checkKernel() error {
	// Check for unsupported kernel versions
	// FIXME: it would be cleaner to not test for specific versions, but rather
	// test for specific functionalities.
	// Unfortunately we can't test for the feature "does not cause a kernel panic"
	// without actually causing a kernel panic, so we need this workaround until
	// the circumstances of pre-3.8 crashes are clearer.
	// For details see http://github.com/docker/docker/issues/407
	if k, err := kernel.GetKernelVersion(); err != nil {
		log.Infof("WARNING: %s", err)
	} else {
		if kernel.CompareKernelVersion(k, &kernel.KernelVersionInfo{Kernel: 3, Major: 8, Minor: 0}) < 0 {
			if os.Getenv("DOCKER_NOWARN_KERNEL_VERSION") == "" {
				log.Infof("WARNING: You are running linux kernel version %s, which might be unstable running docker. Please upgrade your kernel to 3.8.0.", k.String())
			}
		}
	}
	return nil
}

func NewDaemonFromDirectory(config *Config, eng *engine.Engine) (*Daemon, error) {
	if config.Mtu == 0 {
		config.Mtu = getDefaultNetworkMtu()
	}

	// Check for mutually incompatible config options
	if config.BridgeIface != "" && config.BridgeIP != "" {
		return nil, fmt.Errorf("You specified -b & --bip, mutually exclusive options. Please specify only one.")
	}
	if !config.EnableIptables && !config.InterContainerCommunication {
		return nil, fmt.Errorf("You specified --iptables=false with --icc=false. ICC uses iptables to function. Please set --icc or --iptables to true.")
	}
	if !config.EnableIptables && config.EnableIpMasq {
		config.EnableIpMasq = false
	}

	config.DisableNetwork = config.BridgeIface == disableNetworkBridge

	// Claim the pidfile first, to avoid any and all unexpected race conditions.
	// Some of the init doesn't need a pidfile lock - but let's not try to be smart.
	if config.Pidfile != "" {
		if err := utils.CreatePidFile(config.Pidfile); err != nil {
			return nil, err
		}
		eng.OnShutdown(func() {
			// Always release the pidfile last, just in case
			utils.RemovePidFile(config.Pidfile)
		})
	}

	// Check that the system is supported and we have sufficient privileges
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return nil, fmt.Errorf("The Docker daemon is only supported on Linux and Windows")
	}
	if os.Geteuid() != 0 && runtime.GOOS == "linux" {
		return nil, fmt.Errorf("The Docker daemon needs to be run as root")
	}

	if err := checkKernel(); err != nil {
		return nil, err
	}

	// set up the TempDir to use a canonical path
	tmp, err := utils.TempDir(config.Root)
	if err != nil {
		return nil, fmt.Errorf("Unable to get the TempDir under %s: %s", config.Root, err)
	}
	realTmp, err := utils.ReadSymlinkedDirectory(tmp)
	if err != nil {
		return nil, fmt.Errorf("Unable to get the full path to the TempDir (%s): %s", tmp, err)
	}
	os.Setenv("TMPDIR", realTmp)

	if !config.EnableSelinuxSupport {
		selinuxSetDisabled()
	}

	// get the canonical path to the Docker root directory
	var realRoot string
	if _, err := os.Stat(config.Root); err != nil && os.IsNotExist(err) {
		realRoot = config.Root
	} else {
		realRoot, err = utils.ReadSymlinkedDirectory(config.Root)
		if err != nil {
			return nil, fmt.Errorf("Unable to get the full path to root (%s): %s", config.Root, err)
		}
	}
	config.Root = realRoot
	// Create the root directory if it doesn't exists
	if err := os.MkdirAll(config.Root, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}

	// Set the default driver
	graphdriver.DefaultDriver = config.GraphDriver

	// Load storage driver
	driver, err := graphdriver.New(config.Root, config.GraphOptions)
	if err != nil {
		return nil, err
	}
	log.Debugf("Using graph driver %s", driver)

	// As Docker on btrfs and SELinux are incompatible at present, error on both being enabled
	if selinuxEnabled() && config.EnableSelinuxSupport && driver.String() == "btrfs" {
		return nil, fmt.Errorf("SELinux is not supported with the BTRFS graph driver!")
	}

	daemonRepo := path.Join(config.Root, "containers")

	if err := os.MkdirAll(daemonRepo, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}

	// Migrate the container if it is aufs and aufs is enabled
	if err = migrateIfAufs(driver, config.Root); err != nil {
		return nil, err
	}

	log.Debugf("Creating images graph")
	g, err := graph.NewGraph(path.Join(config.Root, "graph"), driver)
	if err != nil {
		return nil, err
	}

	driverName := "vfs"
	volumesDriver, err := graphdriver.GetDriver(driverName, config.Root, config.GraphOptions)
	if err != nil {
		return nil, err
	}

	volumes, err := volumes.NewRepository(filepath.Join(config.Root, "volumes"), volumesDriver)
	if err != nil {
		return nil, err
	}

	trustKey, err := api.LoadOrCreateTrustKey(config.TrustKeyPath)
	if err != nil {
		return nil, err
	}

	log.Debugf("Creating repository list")
	repositories, err := graph.NewTagStore(path.Join(config.Root, "repositories-"+driver.String()), g, trustKey)
	if err != nil {
		return nil, fmt.Errorf("Couldn't create Tag store: %s", err)
	}

	trustDir := path.Join(config.Root, "trust")
	if err := os.MkdirAll(trustDir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	t, err := trust.NewTrustStore(trustDir)
	if err != nil {
		return nil, fmt.Errorf("could not create trust store: %s", err)
	}

	if !config.DisableNetwork {
		job := eng.Job("init_networkdriver")

		job.SetenvBool("EnableIptables", config.EnableIptables)
		job.SetenvBool("InterContainerCommunication", config.InterContainerCommunication)
		job.SetenvBool("EnableIpForward", config.EnableIpForward)
		job.SetenvBool("EnableIpMasq", config.EnableIpMasq)
		job.SetenvBool("EnableIPv6", config.EnableIPv6)
		job.Setenv("BridgeIface", config.BridgeIface)
		job.Setenv("BridgeIP", config.BridgeIP)
		job.Setenv("FixedCIDR", config.FixedCIDR)
		job.Setenv("FixedCIDRv6", config.FixedCIDRv6)
		job.Setenv("DefaultBindingIP", config.DefaultIp.String())

		if err := job.Run(); err != nil {
			return nil, err
		}
	}

	graphdbPath := path.Join(config.Root, "linkgraph.db")
	graph, err := graphdb.NewSqliteConn(graphdbPath)
	if err != nil {
		return nil, err
	}

	localCopy := path.Join(config.Root, "init", fmt.Sprintf("dockerinit-%s", dockerversion.VERSION))
	sysInitPath := utils.DockerInitPath(localCopy)
	if sysInitPath == "" {
		return nil, fmt.Errorf("Could not locate dockerinit: This usually means docker was built incorrectly. See http://docs.docker.com/contributing/devenvironment for official build instructions.")
	}

	if sysInitPath != localCopy {
		// When we find a suitable dockerinit binary (even if it's our local binary), we copy it into config.Root at localCopy for future use (so that the original can go away without that being a problem, for example during a package upgrade).
		if err := os.Mkdir(path.Dir(localCopy), 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if _, err := utils.CopyFile(sysInitPath, localCopy); err != nil {
			return nil, err
		}
		if err := os.Chmod(localCopy, 0700); err != nil {
			return nil, err
		}
		sysInitPath = localCopy
	}

	sysInfo := sysinfo.New(false)
	ed, err := execdrivers.NewDriver(config.ExecDriver, config.Root, sysInitPath, sysInfo)
	if err != nil {
		return nil, err
	}

	daemon := &Daemon{
		ID:             trustKey.PublicKey().KeyID(),
		repository:     daemonRepo,
		containers:     &contStore{s: make(map[string]*Container)},
		execCommands:   newExecStore(),
		graph:          g,
		repositories:   repositories,
		idIndex:        truncindex.NewTruncIndex([]string{}),
		sysInfo:        sysInfo,
		volumes:        volumes,
		config:         config,
		containerGraph: graph,
		driver:         driver,
		sysInitPath:    sysInitPath,
		execDriver:     ed,
		eng:            eng,
		trustStore:     t,
		statsCollector: newStatsCollector(1 * time.Second),
	}
	if err := daemon.restore(); err != nil {
		return nil, err
	}

	// set up filesystem watch on resolv.conf for network changes
	if err := daemon.setupResolvconfWatcher(); err != nil {
		return nil, err
	}

	// Setup shutdown handlers
	// FIXME: can these shutdown handlers be registered closer to their source?
	eng.OnShutdown(func() {
		// FIXME: if these cleanup steps can be called concurrently, register
		// them as separate handlers to speed up total shutdown time
		if err := daemon.shutdown(); err != nil {
			log.Errorf("daemon.shutdown(): %s", err)
		}
		if err := portallocator.ReleaseAll(); err != nil {
			log.Errorf("portallocator.ReleaseAll(): %s", err)
		}
		if err := daemon.driver.Cleanup(); err != nil {
			log.Errorf("daemon.driver.Cleanup(): %s", err.Error())
		}
		if err := daemon.containerGraph.Close(); err != nil {
			log.Errorf("daemon.containerGraph.Close(): %s", err.Error())
		}
	})

	return daemon, nil
}

// set up the watch on the host's /etc/resolv.conf so that we can update container's
// live resolv.conf when the network changes on the host
func (daemon *Daemon) setupResolvconfWatcher() error {

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	//this goroutine listens for the events on the watch we add
	//on the resolv.conf file on the host
	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&fsnotify.Write == fsnotify.Write {
					// verify a real change happened before we go further--a file write may have happened
					// without an actual change to the file
					updatedResolvConf, newResolvConfHash, err := resolvconf.GetIfChanged()
					if err != nil {
						log.Debugf("Error retrieving updated host resolv.conf: %v", err)
					} else if updatedResolvConf != nil {
						// because the new host resolv.conf might have localhost nameservers..
						updatedResolvConf, modified := resolvconf.FilterResolvDns(updatedResolvConf, daemon.config.EnableIPv6)
						if modified {
							// changes have occurred during localhost cleanup: generate an updated hash
							newHash, err := utils.HashData(bytes.NewReader(updatedResolvConf))
							if err != nil {
								log.Debugf("Error generating hash of new resolv.conf: %v", err)
							} else {
								newResolvConfHash = newHash
							}
						}
						log.Debugf("host network resolv.conf changed--walking container list for updates")
						contList := daemon.containers.List()
						for _, container := range contList {
							if err := container.updateResolvConf(updatedResolvConf, newResolvConfHash); err != nil {
								log.Debugf("Error on resolv.conf update check for container ID: %s: %v", container.ID, err)
							}
						}
					}
				}
			case err := <-watcher.Errors:
				log.Debugf("host resolv.conf notify error: %v", err)
			}
		}
	}()

	if err := watcher.Add("/etc/resolv.conf"); err != nil {
		return err
	}
	return nil
}

func parseSecurityOpt(container *Container, config *runconfig.HostConfig) error {
	var (
		labelOpts []string
		err       error
	)

	for _, opt := range config.SecurityOpt {
		con := strings.SplitN(opt, ":", 2)
		if len(con) == 1 {
			return fmt.Errorf("Invalid --security-opt: %q", opt)
		}
		switch con[0] {
		case "label":
			labelOpts = append(labelOpts, con[1])
		case "apparmor":
			container.AppArmorProfile = con[1]
		default:
			return fmt.Errorf("Invalid --security-opt: %q", opt)
		}
	}

	container.ProcessLabel, container.MountLabel, err = label.InitLabels(labelOpts)
	return err
}
