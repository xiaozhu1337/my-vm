package main

import (
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var validShare = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func safeShare(name string) bool { return validShare.MatchString(name) }
func copySelf(target string) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	if filepath.Clean(source) == filepath.Clean(target) {
		return nil
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary := target + ".update-" + rand.Text()
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary, target)
}
