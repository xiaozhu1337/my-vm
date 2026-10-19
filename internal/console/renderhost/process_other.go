//go:build !windows

package renderhost

import "os/exec"

func contain(_ *exec.Cmd) (func(), error) { return func() {}, nil }
