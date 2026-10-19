//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/oneo-me/onevra/source/shells/plain"
	"my-vm/internal/console"
	"my-vm/internal/logging"
	"my-vm/internal/media"
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
	if len(os.Args) > 1 && os.Args[1] == "--build-guest-media" {
		if len(os.Args) != 4 {
			err = fmt.Errorf("需要指定客户机工具目录和输出 ISO 路径")
		} else {
			err = media.CreateISO(os.Args[2], os.Args[3])
		}
		if err != nil {
			fmt.Fprintln(logger, err)
			fmt.Fprintln(os.Stderr, err)
			logger.Close()
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--console" {
		if err = console.Run(os.Args[2:], logger); err != nil {
			fmt.Fprintln(logger, err)
		}
		return
	}
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
		a.messageError = true
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
		shell := plain.NewShell(plain.ShellOptions{Title: "My VM · Windows 虚拟机管理器", Width: 1240, Height: 860, MinWidth: 1040, MinHeight: 700, Theme: appTheme, Content: a.view})
		a.window = shell.Window()
		a.window.OnClose(func(e *mygo.CloseEvent) {
			if a.busy || a.diskJob != nil || a.manager.Active() {
				e.PreventDefault()
				a.messageError = false
				a.message = "请等待当前操作完成，并关闭所有虚拟机后再退出。"
				a.window.Invalidate()
				return
			}
			if !a.leavePage() {
				e.PreventDefault()
			}
		})
		a.window.OnClosed(func() { mygo.App.Quit() })
	})
	if err = mygo.App.Run(); err != nil {
		_, _ = fmt.Fprintln(logger, err)
	}
}
