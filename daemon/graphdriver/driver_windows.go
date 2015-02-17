// +build windows

package graphdriver

import (
	log "github.com/Sirupsen/logrus"
)

const (
	FsMagicWindows = FsMagic(0xa1b1830f) // I have just made this up for now. NTFS=0x5346544E
)

var (
	// Slice of drivers that should be used in an order
	priority = []string{
		"windows",
	}

	FsNames = map[FsMagic]string{

		FsMagicWindows:     "windows",
		FsMagicUnsupported: "unsupported",
	}
)

func GetFSMagic(rootpath string) (FsMagic, error) {
	log.Debugln("WindowsGraphDriver GetFSMagic()")
	// TODO Windows
	return 0, nil
}
