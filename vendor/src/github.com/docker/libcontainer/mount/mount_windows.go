// +build windows

package mount

import (
	"fmt"
)

func (m *Mount) Mount(rootfs, mountLabel string) error {

	switch m.Type {
	// TODO Windows - Type TBD. This allows compilation of daemon on windows initially.
	case "windows":
		return m.windowsMount(rootfs, mountLabel)
	default:
		return fmt.Errorf("unsupported mount type %s for %s", m.Type, m.Destination)
	}

}

// TODO Windows. This is a placeholder until Windows Containers are available.
func (m *Mount) windowsMount(rootfs, mountLabel string) error {
	return fmt.Errorf("Windows does not yet support mount")
}
