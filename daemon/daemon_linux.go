//  +build linux

package daemon

import (
	"github.com/docker/docker/daemon/execdriver/lxc"
	_ "github.com/docker/docker/daemon/graphdriver/vfs"
	_ "github.com/docker/docker/daemon/networkdriver/bridge"
	"github.com/docker/docker/pkg/parsers/kernel"
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
