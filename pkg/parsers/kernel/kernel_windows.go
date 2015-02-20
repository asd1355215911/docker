package kernel

import (
	"fmt"
)

// TODO Windows. This is a placeholder for calling the right Win32 APIs.

type KernelVersionInfo struct {
	Build    int    // eg 9600
	Arch     string // eg AMD64
	BuildLab string // eg winmain
	DateTime string // eg 150219-1600
}

func (k *KernelVersionInfo) String() string {
	return fmt.Sprintf("%d.%s.%s.%s", k.Build, k.Arch, k.BuildLab, k.DateTime)
}

func GetKernelVersion() (*KernelVersionInfo, error) {
	return &KernelVersionInfo{
		Arch:     "amd64fre",
		Build:    12345,
		BuildLab: "ToBeImplemented",
		DateTime: "000000-0000",
	}, nil
}
