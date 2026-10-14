//go:build windows

package main

import (
	"github.com/gkhntpbs/dumb/internal/worker"
	"golang.org/x/sys/windows"
	"os"
)

func pairHideInput(f *os.File) (func(), error) {
	var mode uint32
	h := windows.Handle(f.Fd())
	if e := windows.GetConsoleMode(h, &mode); e != nil {
		s, err := f.Stat()
		if err == nil && s.Mode()&os.ModeCharDevice == 0 {
			return func() {}, nil
		}
		return nil, e
	}
	if e := windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT); e != nil {
		return nil, e
	}
	return func() { windows.SetConsoleMode(h, mode) }, nil
}
func pairParent(p string) error   { return worker.CheckCredentialPathPermissions(p) }
func pairRestrict(p string) error { return restrictDesktopPath(p) }
func pairReplace(a, b string) error {
	from, e := windows.UTF16PtrFromString(a)
	if e != nil {
		return e
	}
	to, e := windows.UTF16PtrFromString(b)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func pairSyncDir(p string) error { return nil }
