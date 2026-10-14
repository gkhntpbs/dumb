//go:build darwin || linux

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"syscall"
)

func pairHideInput(f *os.File) (func(), error) {
	s, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if s.Mode()&os.ModeCharDevice == 0 {
		return func() {}, nil
	}
	term, e := unix.IoctlGetTermios(int(f.Fd()), pairGetTerm)
	if e != nil {
		return nil, e
	}
	old := *term
	term.Lflag &^= unix.ECHO
	if e = unix.IoctlSetTermios(int(f.Fd()), pairSetTerm, term); e != nil {
		return nil, e
	}
	return func() { unix.IoctlSetTermios(int(f.Fd()), pairSetTerm, &old) }, nil
}

// pairDirectoryPolicy treats the current user and root as trusted owners.
// The selected parent must belong to the user and cannot be shared-writable.
// A shared-writable ancestor is safe only with the sticky protection that
// prevents another user from replacing a trusted owner's child directory.
func pairDirectoryPolicy(mode os.FileMode, owner, uid uint32, parent bool) bool {
	if parent {
		return owner == uid && mode.Perm()&0022 == 0
	}
	return (owner == uid || owner == 0) && (mode.Perm()&0022 == 0 || mode&os.ModeSticky != 0)
}
func pairParent(p string) error {
	uid := uint32(os.Geteuid())
	parent := true
	for {
		info, err := os.Lstat(p)
		if err != nil {
			return errors.New("cannot inspect credential directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || !pairDirectoryPolicy(info.Mode(), stat.Uid, uid, parent) {
			return errors.New("credential directories must have trusted owners and restricted write access")
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
		p = next
		parent = false
	}
	return nil
}
func pairRestrict(p string) error   { return os.Chmod(p, 0600) }
func pairReplace(a, b string) error { return os.Rename(a, b) }
func pairSyncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
