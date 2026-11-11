//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd)

package archive

import "os"

// linkNoFollow creates newname as a hard link to the oldname file.
func linkNoFollow(oldname, newname string) error {
	return os.Link(oldname, newname)
}
