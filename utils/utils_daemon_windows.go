// +build daemon

package utils

// TreeSize walks a directory tree and returns its total size in bytes.
func TreeSize(dir string) (size int64, err error) {
	//TODO Windows. Implement this.
	return 0, nil
}

// IsFileOwner checks whether the current user is the owner of the given file.
func IsFileOwner(f string) bool {
	// TODO Windows. Implement this, or deprecate. TBD.
	return false
}
