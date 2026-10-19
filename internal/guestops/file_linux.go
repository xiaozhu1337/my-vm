//go:build linux

package guestops

import (
	"os"
	"path/filepath"
)

func receiveTemporary(path string, overwrite bool) (*os.File, error) {
	return os.CreateTemp(filepath.Dir(path), ".my-vm-copy-")
}
