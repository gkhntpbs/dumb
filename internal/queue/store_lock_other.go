//go:build !darwin && !linux

package queue

import (
	"errors"
	"os"
)

func lockStateFile(file *os.File) error {
	return errors.New("durable queue storage requires Linux or macOS")
}
func validateStoreOwner(info os.FileInfo) error {
	return errors.New("durable queue storage requires Linux or macOS")
}
