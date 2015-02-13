// +build windows

package graphdriver

var (
	// Slice of drivers that should be used in an order
	priority = []string{
		"windows", // TODO Windows when implemented
	}

	FsNames = map[FsMagic]string{
		FsMagicUnsupported: "unsupported",
	}
)

func GetFSMagic(rootpath string) (FsMagic, error) {

	// TODO Windows
	return 0, nil
}
