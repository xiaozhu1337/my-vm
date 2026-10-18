//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/oneo-me/onevra/source/shells/plain"
	"github.com/oneo-me/onevra/source/theme"
	"my-vm/internal/logging"
	"my-vm/internal/vm"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	logger, err := logging.Open(filepath.Join(filepath.Dir(exe), "logs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer logger.Close()
	mygo.App.SetName("My VM")
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	base, err := os.UserConfigDir()
	if err != nil {
		_, _ = fmt.Fprintln(logger, err)
		return
	}
	root := filepath.Join(base, "my-vm")
	a := &application{root: root, logger: logger}
	a.config, err = vm.Load(filepath.Join(root, "library.yaml"))
	if err != nil {
		a.loadError = err
		a.message = err.Error()
		_, _ = fmt.Fprintln(logger, err)
	}
	a.qemuDir = a.config.QEMUDir
	a.manager = vm.NewManager(root, logger)
	a.manager.Changed = func(err error) {
		mygo.RunOnMain(func() {
			if err != nil {
				a.report(err)
			}
			if a.window != nil {
				a.window.Invalidate()
			}
		})
	}
	if len(a.config.Machines) > 0 {
		a.selected = a.config.Machines[0].ID
	}
	mygo.App.SetPath(mygo.PathUserData, root)
	mygo.App.WhenReady(func() {
		shell := plain.NewShell(plain.ShellOptions{Title: "My VM · Windows 虚拟机管理器", Width: 1180, Height: 820, MinWidth: 960, MinHeight: 680, Theme: theme.Onevra, Content: a.view})
		a.window = shell.Window()
		a.window.OnClose(func(e *mygo.CloseEvent) {
			if a.busy || a.manager.Active() {
				e.PreventDefault()
				a.message = "请等待当前操作完成，并关闭所有虚拟机后再退出。"
				a.window.Invalidate()
			}
		})
		a.window.OnClosed(func() { mygo.App.Quit() })
	})
	if err = mygo.App.Run(); err != nil {
		_, _ = fmt.Fprintln(logger, err)
	}
}
