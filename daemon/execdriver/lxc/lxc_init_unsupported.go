// +build !linux,!windows

package lxc

func finalizeNamespace(args *InitArgs) error {
	panic("Not supported on this platform")
}
