//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/oneo-me/onevra/source/shells/plain"
	"my-vm/assets"
	"my-vm/internal/console"
	"my-vm/internal/control"
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
	var controller *control.Server
	defer func() {
		if controller != nil {
			controller.Close()
		}
	}()
	mygo.App.WhenReady(func() {
		shell := plain.NewShell(plain.ShellOptions{Title: "My VM", Width: 1240, Height: 860, MinWidth: 1040, MinHeight: 700, Theme: appTheme, Content: a.view})
		a.window = shell.Window()
		controller, err = control.Listen(a.manager, a.beginExternal, a.finishExternal)
		if err != nil {
			a.report(fmt.Errorf("启动命令行接口: %w", err))
		}
		if err := a.window.SetIcon(assets.Icon); err != nil {
			a.report(err)
		}
		a.window.OnClose(func(e *mygo.CloseEvent) {
			allow, shutdown := a.prepareClose(a.manager.Active(), controller != nil && controller.Active(), a.leavePage, func() bool {
				return a.confirm("正常关机并退出", "仍有虚拟机在运行，是否正常关闭全部虚拟机后退出？", "暂停的虚拟机会先恢复，再正常关机。等待期间管理器保持打开，全部关机后自动退出；关机失败时不会强制断电。")
			})
			if !allow {
				e.PreventDefault()
				a.window.Invalidate()
			}
			if shutdown {
				a.shutdownForExit()
			}
		})
		a.window.OnClosed(func() { mygo.App.Quit() })
	})
	if err = mygo.App.Run(); err != nil {
		_, _ = fmt.Fprintln(logger, err)
	}
}

func (a *application) prepareClose(active, external bool, leave, confirm func() bool) (allow, shutdown bool) {
	if a.closeReady {
		return true, false
	}
	if a.closing {
		return false, false
	}
	if a.busy || len(a.operations) > 0 || len(a.diskJobs) > 0 || len(a.externalJobs) > 0 || external {
		a.message, a.messageError = "请等待当前操作完成后再退出。", false
		return false, false
	}
	if !leave() {
		return false, false
	}
	if !active {
		return true, false
	}
	if !confirm() {
		return false, false
	}
	a.closing, a.busy = true, true
	a.message, a.messageError = "正在正常关闭所有虚拟机，全部关机后将自动退出…", false
	return false, true
}

func (a *application) shutdownForExit() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := a.manager.ShutdownAll(ctx)
		mygo.RunOnMain(func() {
			if a.finishShutdown(err, a.manager.Active()) {
				a.window.Close()
			} else {
				a.window.Invalidate()
			}
		})
	}()
}

func (a *application) finishShutdown(err error, active bool) bool {
	if err == nil && active {
		err = fmt.Errorf("仍有虚拟机未关闭，管理器保持打开")
	}
	if err != nil {
		a.closing, a.busy = false, false
		a.report(fmt.Errorf("关闭所有虚拟机未完成，应用未退出: %w", err))
		return false
	}
	// Keep new operations blocked until the final close event has been accepted.
	a.closeReady = true
	return true
}
