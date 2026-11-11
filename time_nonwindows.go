//go:build !windows

package archive

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// chtimes changes the access time and modified time of a file at the given path.
// If the modified time is prior to the Unix Epoch (unixMinTime), or after the
// end of Unix Time (unixEpochTime), os.Chtimes has undefined behavior. In this
// case, Chtimes defaults to Unix Epoch, just in case.
func chtimes(name string, atime time.Time, mtime time.Time) error {
	return os.Chtimes(name, atime, mtime)
}

func timeToTimespec(time time.Time) unix.Timespec {
	if time.IsZero() {
		// Return UTIME_OMIT special value
		return unix.Timespec{
			Sec:  0,
			Nsec: (1 << 30) - 2,
		}
	}
	return unix.NsecToTimespec(time.UnixNano())
}

// lchtimes changes the access and modified time of the named file within
// root, without following a symlink in the final path component.
func lchtimes(root *extractRoot, name string, atime time.Time, mtime time.Time) error {
	p, err := root.resolve("lchtimes", name, false)
	if err != nil {
		return err
	}
	utimes := [2]unix.Timespec{
		timeToTimespec(atime),
		timeToTimespec(mtime),
	}
	err = unix.UtimesNanoAt(unix.AT_FDCWD, p, utimes[0:], unix.AT_SYMLINK_NOFOLLOW)
	if err != nil && err != unix.ENOSYS {
		return err
	}
	return err
}
