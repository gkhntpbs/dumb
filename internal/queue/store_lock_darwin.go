package queue

import (
	"errors"
	"os"
	"syscall"
)

func lockStateFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func validateStoreOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("queue state must be owned by the current user")
	}
	return nil
}
