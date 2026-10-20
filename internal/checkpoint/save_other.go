//go:build !windows

package checkpoint

import "os"

func publishFile(source, target string) error { return os.Rename(source, target) }
