// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"syscall"
)

func sigpipe() // implemented in package runtime

func epipecheck(file *File, e int) {
	if e == syscall.EPIPE {
		file.nepipe++
		if file.nepipe >= 10 {
			sigpipe()
		}
	} else {
		file.nepipe = 0
	}
}

// Stat returns a FileInfo structure describing the named file and an error, if any.
// If name names a valid symbolic link, the returned FileInfo describes
// the file pointed at by the link and has fi.FollowedSymlink set to true.
// If name names an invalid symbolic link, the returned FileInfo describes
// the link itself and has fi.FollowedSymlink set to false.
func Stat(name string) (fi *FileInfo, err Error) {
	var lstat, stat syscall.Stat_t
	e := syscall.Lstat(name, &lstat)
	if iserror(e) {
		return nil, &PathError{"stat", name, Errno(e)}
	}
	statp := &lstat
	if lstat.Mode&syscall.S_IFMT == syscall.S_IFLNK {
		e := syscall.Stat(name, &stat)
		if !iserror(e) {
			statp = &stat
		}
	}
	return fileInfoFromStat(name, new(FileInfo), &lstat, statp), nil
}

// Lstat returns the FileInfo structure describing the named file and an
// error, if any.  If the file is a symbolic link, the returned FileInfo
// describes the symbolic link.  Lstat makes no attempt to follow the link.
func Lstat(name string) (fi *FileInfo, err Error) {
	var stat syscall.Stat_t
	e := syscall.Lstat(name, &stat)
	if iserror(e) {
		return nil, &PathError{"lstat", name, Errno(e)}
	}
	return fileInfoFromStat(name, new(FileInfo), &stat, &stat), nil
}

// Remove removes the named file or directory.
func Remove(name string) Error {
	// System call interface forces us to know
	// whether name is a file or directory.
	// Try both: it is cheaper on average than
	// doing a Stat plus the right one.
	e := syscall.Unlink(name)
	if !iserror(e) {
		return nil
	}
	e1 := syscall.Rmdir(name)
	if !iserror(e1) {
		return nil
	}

	// Both failed: figure out which error to return.
	// OS X and Linux differ on whether unlink(dir)
	// returns EISDIR, so can't use that.  However,
	// both agree that rmdir(file) returns ENOTDIR,
	// so we can use that to decide which error is real.
	// Rmdir might also return ENOTDIR if given a bad
	// file path, like /etc/passwd/foo, but in that case,
	// both errors will be ENOTDIR, so it's okay to
	// use the error from unlink.
	// For windows syscall.ENOTDIR is set
	// to syscall.ERROR_DIRECTORY, hopefully it should
	// do the trick.
	if e1 != syscall.ENOTDIR {
		e = e1
	}
	return &PathError{"remove", name, Errno(e)}
}

// LinkError records an error during a link or symlink or rename
// system call and the paths that caused it.
type LinkError struct {
	Op    string
	Old   string
	New   string
	Error Error
}

func (e *LinkError) String() string {
	return e.Op + " " + e.Old + " " + e.New + ": " + e.Error.String()
}

// Link creates a hard link.
func Link(oldname, newname string) Error {
	e := syscall.Link(oldname, newname)
	if iserror(e) {
		return &LinkError{"link", oldname, newname, Errno(e)}
	}
	return nil
}

// Symlink creates a symbolic link.
func Symlink(oldname, newname string) Error {
	e := syscall.Symlink(oldname, newname)
	if iserror(e) {
		return &LinkError{"symlink", oldname, newname, Errno(e)}
	}
	return nil
}

// Readlink reads the contents of a symbolic link: the destination of
// the link.  It returns the contents and an Error, if any.
func Readlink(name string) (string, Error) {
	for len := 128; ; len *= 2 {
		b := make([]byte, len)
		n, e := syscall.Readlink(name, b)
		if iserror(e) {
			return "", &PathError{"readlink", name, Errno(e)}
		}
		if n < len {
			return string(b[0:n]), nil
		}
	}
	// Silence 6g.
	return "", nil
}

// Rename renames a file.
func Rename(oldname, newname string) Error {
	e := syscall.Rename(oldname, newname)
	if iserror(e) {
		return &LinkError{"rename", oldname, newname, Errno(e)}
	}
	return nil
}

// Chmod changes the mode of the named file to mode.
// If the file is a symbolic link, it changes the mode of the link's target.
func Chmod(name string, mode uint32) Error {
	if e := syscall.Chmod(name, mode); iserror(e) {
		return &PathError{"chmod", name, Errno(e)}
	}
	return nil
}

// Chmod changes the mode of the file to mode.
func (f *File) Chmod(mode uint32) Error {
	if e := syscall.Fchmod(f.fd, mode); iserror(e) {
		return &PathError{"chmod", f.name, Errno(e)}
	}
	return nil
}

// Chown changes the numeric uid and gid of the named file.
// If the file is a symbolic link, it changes the uid and gid of the link's target.
func Chown(name string, uid, gid int) Error {
	if e := syscall.Chown(name, uid, gid); iserror(e) {
		return &PathError{"chown", name, Errno(e)}
	}
	return nil
}

// Lchown changes the numeric uid and gid of the named file.
// If the file is a symbolic link, it changes the uid and gid of the link itself.
func Lchown(name string, uid, gid int) Error {
	if e := syscall.Lchown(name, uid, gid); iserror(e) {
		return &PathError{"lchown", name, Errno(e)}
	}
	return nil
}

// Chown changes the numeric uid and gid of the named file.
func (f *File) Chown(uid, gid int) Error {
	if e := syscall.Fchown(f.fd, uid, gid); iserror(e) {
		return &PathError{"chown", f.name, Errno(e)}
	}
	return nil
}

// Truncate changes the size of the file.
// It does not change the I/O offset.
func (f *File) Truncate(size int64) Error {
	if e := syscall.Ftruncate(f.fd, size); iserror(e) {
		return &PathError{"truncate", f.name, Errno(e)}
	}
	return nil
}

// Sync commits the current contents of the file to stable storage.
// Typically, this means flushing the file system's in-memory copy
// of recently written data to disk.
func (file *File) Sync() (err Error) {
	if file == nil {
		return EINVAL
	}
	if e := syscall.Fsync(file.fd); iserror(e) {
		return NewSyscallError("fsync", e)
	}
	return nil
}

// Chtimes changes the access and modification times of the named
// file, similar to the Unix utime() or utimes() functions.
//
// The argument times are in nanoseconds, although the underlying
// filesystem may truncate or round the values to a more
// coarse time unit.
func Chtimes(name string, atime_ns int64, mtime_ns int64) Error {
	var utimes [2]syscall.Timeval
	utimes[0] = syscall.NsecToTimeval(atime_ns)
	utimes[1] = syscall.NsecToTimeval(mtime_ns)
	if e := syscall.Utimes(name, utimes[0:]); iserror(e) {
		return &PathError{"chtimes", name, Errno(e)}
	}
	return nil
}
