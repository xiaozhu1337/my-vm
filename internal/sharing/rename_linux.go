//go:build linux

package sharing

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func renameNoReplace(root *os.Root, source, target string) error {
	from, err := root.Open(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := root.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer to.Close()
	return unix.Renameat2(int(from.Fd()), filepath.Base(source), int(to.Fd()), filepath.Base(target), unix.RENAME_NOREPLACE)
}
