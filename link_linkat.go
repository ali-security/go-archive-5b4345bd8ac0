//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package archive

import (
	"os"

	"golang.org/x/sys/unix"
)

// linkNoFollow creates newname as a hard link to the oldname file. If oldname
// is a symbolic link, the hard link is created to the symbolic link itself,
// and not to its target; os.Link follows the symbolic link on some platforms.
func linkNoFollow(oldname, newname string) error {
	if err := unix.Linkat(unix.AT_FDCWD, oldname, unix.AT_FDCWD, newname, 0); err != nil {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: err}
	}
	return nil
}
