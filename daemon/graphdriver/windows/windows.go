//+build windows

package windowsgraphdriver

import (
	"sync"

	"github.com/docker/docker/daemon/graphdriver"
	"github.com/docker/docker/pkg/archive"
)

func init() {
	graphdriver.Register("windows", Init)
}

type Driver struct {
	root       string
	sync.Mutex // Protects concurrent modification to active
	active     map[string]int
}

// New returns a new WINDOWS driver.
func Init(root string, options []string) (graphdriver.Driver, error) {

	a := &Driver{
		root:   root,
		active: make(map[string]int),
	}

	return a, nil
}

func (*Driver) String() string {
	return "windows"
}

func (a *Driver) Status() [][2]string {

	return [][2]string{
		{"Root Dir", "Not implemented"},
		{"Backing Filesystem", "Not implemented"},
		{"Dirs", "Not implemented"},
	}
}

// Exists returns true if the given id is registered with
// this driver
func (a *Driver) Exists(id string) bool {
	return false
}

// Three folders are created for each id
// mnt, layers, and diff
func (a *Driver) Create(id, parent string) error {

	return nil
}

// Unmount and remove the dir information
func (a *Driver) Remove(id string) error {
	return nil
}

// Return the rootfs path for the id
// This will mount the dir at it's given path
func (a *Driver) Get(id, mountLabel string) (string, error) {
	return "Not implemented path", nil
}

func (a *Driver) Put(id string) error {
	return nil
}

// Diff produces an archive of the changes between the specified
// layer and its parent layer which may be "".
func (a *Driver) Diff(id, parent string) (archive.Archive, error) {
	return nil, nil
}

// DiffSize calculates the changes between the specified id
// and its parent and returns the size in bytes of the changes
// relative to its base filesystem directory.
func (a *Driver) DiffSize(id, parent string) (size int64, err error) {
	return 0, nil
}

// ApplyDiff extracts the changeset from the given diff into the
// layer with the specified id and parent, returning the size of the
// new layer in bytes.
func (a *Driver) ApplyDiff(id, parent string, diff archive.ArchiveReader) (size int64, err error) {
	return 0, nil
}

// Changes produces a list of changes between the specified layer
// and its parent layer. If parent is "", then all changes will be ADD changes.
func (a *Driver) Changes(id, parent string) ([]archive.Change, error) {
	return nil, nil
}

// During cleanup aufs needs to unmount all mountpoints
func (a *Driver) Cleanup() error {
	return nil
}
