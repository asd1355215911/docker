package daemon

import (
	"net"
	"os"

	"github.com/docker/docker/opts"
	flag "github.com/docker/docker/pkg/mflag"
)

// Config define the configuration of a docker daemon
// These are the configuration settings that you pass
// to the docker daemon when you launch it with say: `docker -d -e lxc`
// FIXME: separate runtime configuration from http api configuration
type Config struct {
	Pidfile                     string
	Root                        string
	AutoRestart                 bool
	Dns                         []string
	DnsSearch                   []string
	DefaultIp                   net.IP
	BridgeIface                 string
	InterContainerCommunication bool
	GraphDriver                 string
	GraphOptions                []string
	ExecDriver                  string
	Mtu                         int
	SocketGroup                 string
	EnableCors                  bool
	DisableNetwork              bool

	Context      map[string][]string
	TrustKeyPath string
	Labels       []string
}

// InstallFlags adds command-line options to the top-level flag parser for
// the current process.
// Subsequent calls to `flag.Parse` will populate config with values parsed
// from the command-line.
func (config *Config) InstallFlags() {

	flag.StringVar(&config.Pidfile, []string{"p", "-pidfile"}, os.Getenv("temp")+"docker.pid", "Path to use for daemon PID file")
	flag.StringVar(&config.Root, []string{"g", "-graph"}, os.Getenv("temp")+"/docker", "Path to use as the root of the Docker runtime")
	flag.StringVar(&config.ExecDriver, []string{"e", "-exec-driver"}, "windows", "Exec driver to use")

	flag.BoolVar(&config.AutoRestart, []string{"#r", "#-restart"}, true, "--restart on the daemon has been deprecated in favor of --restart policies on docker run")
	flag.BoolVar(&config.InterContainerCommunication, []string{"#icc", "-icc"}, true, "Enable inter-container communication")
	flag.StringVar(&config.GraphDriver, []string{"s", "-storage-driver"}, "", "Storage driver to use")
	flag.IntVar(&config.Mtu, []string{"#mtu", "-mtu"}, 0, "Set the containers network MTU")
	flag.StringVar(&config.SocketGroup, []string{"G", "-group"}, "docker", "Group for the unix socket")
	flag.BoolVar(&config.EnableCors, []string{"#api-enable-cors", "-api-enable-cors"}, false, "Enable CORS headers in the remote API")
	opts.IPVar(&config.DefaultIp, []string{"#ip", "-ip"}, "0.0.0.0", "Default IP when binding container ports")
	opts.ListVar(&config.GraphOptions, []string{"-storage-opt"}, "Set storage driver options")
	// FIXME: why the inconsistency between "hosts" and "sockets"?
	opts.IPListVar(&config.Dns, []string{"#dns", "-dns"}, "DNS server to use")
	opts.DnsSearchListVar(&config.DnsSearch, []string{"-dns-search"}, "DNS search domains to use")
	opts.LabelListVar(&config.Labels, []string{"-label"}, "Set key=value labels to the daemon")
}
