//go:build linux

package guestops

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func runCommand(command *exec.Cmd) error {
	// shortcut: A child that deliberately creates a new session escapes the group; use a cgroup for daemon control.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Cancel = kill
	if err := command.Start(); err != nil {
		return err
	}
	defer kill()
	return command.Wait()
}
