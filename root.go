package archive

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moby/sys/sequential"
)

// errPathEscapes is returned by extractRoot when a path resolves to a location
// outside of the root. It uses the same message as the (unexported) error that
// os.Root uses for this purpose, so that both are detected by isPathEscapes.
var errPathEscapes = errors.New("path escapes from parent")

// maxSymlinks is the maximum number of symbolic links that are followed when
// resolving a path within an extractRoot.
const maxSymlinks = 255

// extractRoot provides access to the filesystem below a directory, and is used
// to confine the filesystem operations performed when extracting an archive to
// the extraction destination.
//
// It is modeled after [os.Root], which is not available in all Go versions
// supported by this module. Names passed to its methods are relative to the
// root. Symbolic links in path components are followed, but resolving a name
// fails with an error matching [isPathEscapes] when the name, or a symbolic link
// encountered while resolving it, refers to a location outside of the root.
// Like os.Root, symbolic links with an absolute target are considered to escape
// the root, as their target is relative to the host's root, not the extraction
// root.
//
// Unlike os.Root, extractRoot resolves a name to a path before operating on that
// path. It prevents links that are created by an archive from redirecting the
// extraction outside of the destination, but it does not protect against other
// processes concurrently modifying the destination.
type extractRoot struct {
	name string
}

// openRoot opens the named directory as an extractRoot.
func openRoot(name string) (*extractRoot, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, &os.PathError{Op: "openroot", Path: name, Err: syscall.ENOTDIR}
	}
	return &extractRoot{name: name}, nil
}

// Name returns the name of the directory presented to openRoot.
func (r *extractRoot) Name() string {
	return r.name
}

// Close releases the root. It is a no-op, and provided for parity with os.Root.
func (r *extractRoot) Close() error {
	return nil
}

// Lstat returns a FileInfo describing the named file in the root. If the file
// is a symbolic link, the returned FileInfo describes the link.
func (r *extractRoot) Lstat(name string) (os.FileInfo, error) {
	p, err := r.resolve("lstat", name, false)
	if err != nil {
		return nil, err
	}
	return os.Lstat(p)
}

// Stat returns a FileInfo describing the named file in the root.
func (r *extractRoot) Stat(name string) (os.FileInfo, error) {
	p, err := r.resolve("stat", name, true)
	if err != nil {
		return nil, err
	}
	return os.Stat(p)
}

// Mkdir creates a new directory in the root.
func (r *extractRoot) Mkdir(name string, perm os.FileMode) error {
	p, err := r.resolve("mkdir", name, false)
	if err != nil {
		return err
	}
	return os.Mkdir(p, perm)
}

// OpenFile opens the named file in the root. It uses sequential file access to
// avoid depleting the standby list on Windows. On other platforms, this equates
// to a regular os.OpenFile.
func (r *extractRoot) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	p, err := r.resolve("open", name, true)
	if err != nil {
		return nil, err
	}
	return sequential.OpenFile(p, flag, perm)
}

// Open opens the named file in the root for reading.
func (r *extractRoot) Open(name string) (*os.File, error) {
	p, err := r.resolve("open", name, true)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// RemoveAll removes the named file or directory in the root, and any children
// it contains. A symbolic link in the final path component is removed, and not
// followed. The root itself cannot be removed.
func (r *extractRoot) RemoveAll(name string) error {
	p, err := r.resolve("removeall", name, false)
	if err != nil {
		return err
	}
	if p == r.hostPath(nil) {
		return &os.PathError{Op: "removeall", Path: name, Err: syscall.EINVAL}
	}
	return os.RemoveAll(p)
}

// Link creates newname as a hard link to the oldname file. If oldname is a
// symbolic link, the hard link is created to the symbolic link, and not to its
// target.
func (r *extractRoot) Link(oldname, newname string) error {
	oldPath, err := r.resolve("link", oldname, false)
	if err != nil {
		return err
	}
	newPath, err := r.resolve("link", newname, false)
	if err != nil {
		return err
	}
	return linkNoFollow(oldPath, newPath)
}

// Symlink creates newname as a symbolic link to oldname. The link target
// (oldname) is stored as-is; it is not resolved within the root.
func (r *extractRoot) Symlink(oldname, newname string) error {
	p, err := r.resolve("symlink", newname, false)
	if err != nil {
		return err
	}
	return os.Symlink(oldname, p)
}

// Lchown changes the numeric uid and gid of the named file in the root. If the
// file is a symbolic link, it changes the uid and gid of the link itself.
func (r *extractRoot) Lchown(name string, uid, gid int) error {
	p, err := r.resolve("lchown", name, false)
	if err != nil {
		return err
	}
	return os.Lchown(p, uid, gid)
}

// Chmod changes the mode of the named file in the root.
func (r *extractRoot) Chmod(name string, mode os.FileMode) error {
	p, err := r.resolve("chmod", name, true)
	if err != nil {
		return err
	}
	return os.Chmod(p, mode)
}

// Chtimes changes the access and modification times of the named file in the
// root.
func (r *extractRoot) Chtimes(name string, atime, mtime time.Time) error {
	p, err := r.resolve("chtimes", name, true)
	if err != nil {
		return err
	}
	return chtimes(p, atime, mtime)
}

// Lsetxattr sets an extended attribute on the named file in the root. If the
// file is a symbolic link, the attribute is set on the link itself.
func (r *extractRoot) Lsetxattr(name, attr string, data []byte, flags int) error {
	p, err := r.resolve("lsetxattr", name, false)
	if err != nil {
		return err
	}
	return lsetxattr(p, attr, data, flags)
}

// resolve resolves name within the root, and returns the corresponding path on
// the host's filesystem. Symbolic links in intermediate path components are
// always followed; a symbolic link in the final path component is followed only
// if followFinal is set. The final path component is not required to exist, as
// the caller may be about to create it.
func (r *extractRoot) resolve(op, name string, followFinal bool) (string, error) {
	if name == "" {
		return "", &os.PathError{Op: op, Path: name, Err: syscall.ENOENT}
	}
	if isRootedPath(name) {
		return "", &os.PathError{Op: op, Path: name, Err: errPathEscapes}
	}

	var (
		// resolved holds the path components below the root that were
		// resolved so far. It contains no symbolic links, "." or "..".
		resolved []string
		links    int
	)
	pending := splitPathComponents(name)
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return "", &os.PathError{Op: op, Path: name, Err: errPathEscapes}
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}

		final := len(pending) == 0
		if final && !followFinal {
			resolved = append(resolved, c)
			break
		}

		p := filepath.Join(r.hostPath(resolved), c)
		fi, err := os.Lstat(p)
		if err != nil {
			if final && errors.Is(err, os.ErrNotExist) {
				resolved = append(resolved, c)
				break
			}
			return "", &os.PathError{Op: op, Path: name, Err: underlyingError(err)}
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			if !final && !fi.IsDir() {
				return "", &os.PathError{Op: op, Path: name, Err: syscall.ENOTDIR}
			}
			resolved = append(resolved, c)
			continue
		}

		links++
		if links > maxSymlinks {
			return "", &os.PathError{Op: op, Path: name, Err: syscall.ELOOP}
		}
		target, err := os.Readlink(p)
		if err != nil {
			return "", &os.PathError{Op: op, Path: name, Err: underlyingError(err)}
		}
		if target == "" {
			return "", &os.PathError{Op: op, Path: name, Err: syscall.ENOENT}
		}
		if isRootedPath(target) {
			// The target of an absolute symbolic link is relative to the
			// host's root, which is outside of this root.
			return "", &os.PathError{Op: op, Path: name, Err: errPathEscapes}
		}
		// Continue resolving with the components of the link's target, which
		// are relative to the directory containing the link.
		pending = append(splitPathComponents(target), pending...)
	}
	return r.hostPath(resolved), nil
}

// hostPath returns the path on the host's filesystem for the given (resolved)
// path components below the root.
func (r *extractRoot) hostPath(components []string) string {
	return filepath.Join(r.name, filepath.Join(components...))
}

// splitPathComponents splits p into its path components, omitting empty ones.
// Both '/' and the platform's path separator are treated as separators.
func splitPathComponents(p string) []string {
	return strings.FieldsFunc(p, func(c rune) bool {
		return c == '/' || c == os.PathSeparator
	})
}

// isRootedPath reports whether p is an absolute path, or (on Windows) a path
// that is relative to a volume, or to the root of the current volume.
func isRootedPath(p string) bool {
	return filepath.IsAbs(p) || filepath.VolumeName(p) != "" || (p != "" && os.IsPathSeparator(p[0]))
}

// underlyingError returns the error wrapped by a *os.PathError or
// *os.LinkError, or err itself otherwise.
func underlyingError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}
