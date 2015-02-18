//  +build windows

package daemon

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/docker/docker/api"
	"github.com/docker/docker/daemon/execdriver/execdrivers"
	"github.com/docker/docker/daemon/graphdriver"
	_ "github.com/docker/docker/daemon/graphdriver/windows"
	"github.com/docker/docker/dockerversion"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/graph"
	"github.com/docker/docker/pkg/graphdb"
	"github.com/docker/docker/pkg/sysinfo"
	"github.com/docker/docker/pkg/truncindex"
	"github.com/docker/docker/runconfig"
	"github.com/docker/docker/trust"
	"github.com/docker/docker/utils"
	"github.com/docker/docker/volumes"
	"github.com/docker/libcontainer/label"
)

// TODO Windows This can probably be factored out better
func KillIfLxc(ID string) {
	// No-op on Windows as Lxc execution driver is not used.
}

func NewDaemonFromDirectory(config *Config, eng *engine.Engine) (*Daemon, error) {
	if config.Mtu == 0 {
		config.Mtu = getDefaultNetworkMtu()
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
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("The Docker daemon is only supported on Linux and Windows")
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

	daemonRepo := path.Join(config.Root, "containers")

	if err := os.MkdirAll(daemonRepo, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}

	log.Debugf("Creating images graph")
	g, err := graph.NewGraph(path.Join(config.Root, "graph"), driver)
	if err != nil {
		return nil, err
	}

	volumesDriver, err := graphdriver.GetDriver("windows", config.Root, config.GraphOptions)
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

	// TODO Windows. This is temporary.
	sysInitPath = os.Getenv("TEMP")

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

	// Setup shutdown handlers
	// FIXME: can these shutdown handlers be registered closer to their source?
	eng.OnShutdown(func() {
		// FIXME: if these cleanup steps can be called concurrently, register
		// them as separate handlers to speed up total shutdown time
		if err := daemon.shutdown(); err != nil {
			log.Errorf("daemon.shutdown(): %s", err)
		}

		// TODO Windows. Don't believe PortAllocator will be needed.
		//if err := portallocator.ReleaseAll(); err != nil {
		//	log.Errorf("portallocator.ReleaseAll(): %s", err)
		//}
		if err := daemon.driver.Cleanup(); err != nil {
			log.Errorf("daemon.driver.Cleanup(): %s", err.Error())
		}
		if err := daemon.containerGraph.Close(); err != nil {
			log.Errorf("daemon.containerGraph.Close(): %s", err.Error())
		}
	})

	return daemon, nil
}

// Factored out AppArmor.
// TODO Windows: What about label? Is this valid?
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
		default:
			return fmt.Errorf("Invalid --security-opt: %q", opt)
		}
	}

	container.ProcessLabel, container.MountLabel, err = label.InitLabels(labelOpts)
	return err
}
