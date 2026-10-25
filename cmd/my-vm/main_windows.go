//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/egoist/mygo"
	"github.com/oneo-me/onevra/source/shells/plain"
	"my-vm/assets"
	"my-vm/internal/console"
	"my-vm/internal/logging"
	"my-vm/internal/media"
	vm "my-vm/library"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a.manager, err = vm.Connect(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(logger, err)
		return
	}
	defer a.manager.Close()
	a.config, err = a.refresh()
	if err != nil {
		a.loadError = err
		a.messageError = true
		a.message = err.Error()
		fmt.Fprintln(logger, err)
	}
	a.qemuDir = a.config.QEMUDir
	pollContext, stopPolling := context.WithCancel(context.Background())
	defer stopPolling()

	if len(a.config.Machines) > 0 {
		a.selected = a.config.Machines[0].ID
	}
	mygo.App.SetPath(mygo.PathUserData, root)

	mygo.App.WhenReady(func() {
		shell := plain.NewShell(plain.ShellOptions{Title: "My VM", Width: 1240, Height: 860, MinWidth: 1040, MinHeight: 700, Theme: appTheme, Content: a.view})
		a.window = shell.Window()
		go a.poll(pollContext)
		if err := a.window.SetIcon(assets.Icon); err != nil {
			a.report(err)
		}
		a.window.OnClose(func(e *mygo.CloseEvent) {
			allow, shutdown := a.prepareClose(a.manager.Active(), false, a.leavePage, func() bool {
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
		_, err := a.manager.Execute(ctx, vm.Request{Action: "shutdown-all"})
		if refreshErr := a.manager.Refresh(ctx); err == nil {
			err = refreshErr
		}
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

func (a *application) poll(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cfg, err := a.refresh()
		var displayErr error
		if err == nil {
			displayErr = a.syncDisplays(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		mygo.RunOnMain(func() {
			if a.applyRefresh(cfg, err, displayErr) && a.window != nil {
				a.window.Invalidate()
			}
		})
	}
}

func (a *application) applyRefresh(cfg vm.Config, err, displayErr error) bool {
	if err != nil {
		changed := a.loadError == nil || a.loadError.Error() != err.Error()
		if changed {
			a.report(err)
		}
		a.loadError = err
		return changed
	}
	revision := a.manager.StateRevision()
	changed := a.stateRevision != revision
	a.stateRevision = revision
	if !reflect.DeepEqual(a.config, cfg) {
		a.config = cfg
		a.resetSnapshots()
		changed = true
	}
	if changed {
		a.syncJobs()
	}
	if displayErr != nil {
		a.report(displayErr)
		changed = true
	}
	if a.loadError != nil {
		a.loadError = nil
		a.message = "已重新连接核心服务"
		a.messageError = false
		changed = true
	}
	return changed
}
