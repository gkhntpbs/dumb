//go:build !windows

package worker

import (
	"fmt"
	"os"
)

func checkTokenPermissions(path string, fi os.FileInfo) error {
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("worker: token file %s has mode %v; it must not be group or world accessible (chmod 600)", path, fi.Mode().Perm())
	}
	return nil
}
