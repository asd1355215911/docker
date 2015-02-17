// +build !linux

package system

func ReadMemInfo() (*MemInfo, error) {
	// TODO Windows. This needs implementing.
	return nil, ErrNotSupportedPlatform
}
