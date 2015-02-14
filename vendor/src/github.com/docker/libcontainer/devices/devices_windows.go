// +build windows

package devices

import (
	"os"
	"syscall"
)

// Given the path to a device and it's cgroup_permissions(which cannot be easilly queried) look up the information about a linux device and return that information as a Device struct.
func GetDevice(path, cgroupPermissions string) (*Device, error) {
	fileInfo, err := osLstat(path)
	if err != nil {
		return nil, err
	}

	var (
		devType                rune
		mode                   = fileInfo.Mode()
		fileModePermissionBits = os.FileMode.Perm(mode)
	)

	switch {
	case mode&os.ModeDevice == 0:
		return nil, ErrNotADeviceNode
	case mode&os.ModeCharDevice != 0:
		fileModePermissionBits |= syscall.S_IFCHR
		devType = 'c'
	default:
		fileModePermissionBits |= syscall.S_IFBLK
		devType = 'b'
	}

	return &Device{
		Type:              devType,
		Path:              path,
		CgroupPermissions: cgroupPermissions,
		FileMode:          fileModePermissionBits,
	}, nil
}
