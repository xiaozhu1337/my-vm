//go:build windows

package guestagent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"
	"my-vm/assets"
	"my-vm/internal/sharing"
)

func Run(ctx context.Context, cancel context.CancelFunc, log io.Writer, mapShares func([]sharing.Share) error, desktop func(context.Context, io.Writer), readyEvent string) error {
	release, active, err := acquire(ctx, cancel)
	if err != nil {
		return err
	}
	if !active {
		return errors.New("增强代理已在当前会话运行")
	}
	defer release()
	return runTray(ctx, cancel, log, func(update func(string)) error {
		return services(ctx, cancel, log, mapShares, desktop, func() error {
			update("代理运行中 · 正在连接主机共享目录")
			return signalReady(readyEvent)
		}, update)
	})
}

func runTray(ctx context.Context, cancel context.CancelFunc, log io.Writer, work func(func(string)) error) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	logDir := filepath.Join(root, "MyVMGuestTools", "logs")
	mygo.App.SetName("MyVMGuestTools")
	done := make(chan error, 1)
	started := false
	var tray *mygo.Tray
	mygo.App.OnQuit(func() { cancel() })
	mygo.App.WhenReady(func() {
		status := &mygo.MenuItem{Label: "代理启动中", Disabled: true}
		enabled, e := Startup(exe)
		report(log, e)
		startup := &mygo.MenuItem{Label: "登录时自动启动", Type: mygo.MenuItemCheckbox, Checked: enabled}
		menu := mygo.NewMenu([]*mygo.MenuItem{status, mygo.Separator(), {Label: "打开日志文件夹", Click: func(*mygo.MenuItem, *mygo.Window) { report(log, mygo.Shell.OpenPath(logDir)) }}, startup, mygo.Separator(), {Label: "退出增强工具", Click: func(*mygo.MenuItem, *mygo.Window) { cancel() }}})
		startup.Click = func(item *mygo.MenuItem, _ *mygo.Window) {
			if e := SetStartup(exe, item.IsChecked()); e != nil {
				item.SetChecked(!item.IsChecked())
				report(log, e)
			}
			tray.SetMenu(menu)
		}
		tray, err = mygo.NewTray(mygo.TrayOptions{Icon: assets.Icon, ToolTip: "MyVM 客户机增强工具", Menu: menu})
		if err != nil {
			cancel()
			mygo.App.Quit()
			return
		}
		tray.OnClick(tray.PopUpMenu)
		update := func(text string) {
			if ctx.Err() == nil {
				mygo.RunOnMain(func() {
					if ctx.Err() == nil {
						status.SetLabel(text)
						tray.SetToolTip(text)
						tray.SetMenu(menu)
					}
				})
			}
		}
		started = true
		go func() {
			result := work(update)
			cancel()
			done <- result
		}()
		go func() { <-ctx.Done(); mygo.RunOnMain(func() { tray.Destroy(); mygo.App.Quit() }) }()
	})
	loopErr := mygo.App.Run()
	cancel()
	if started {
		err = errors.Join(err, <-done)
	}
	return errors.Join(err, loopErr)
}

// restartPrevious also supports agents predating the private --ready-event flag.
func restartPrevious(target string) (err error) {
	command := exec.Command(target, "--install=false")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err = command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()
	defer func() {
		if err != nil {
			command.Process.Kill()
			<-done
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case e := <-done:
			return fmt.Errorf("原代理启动后退出: %v", e)
		default:
		}
		owners, e := listenerOwners(18389)
		if e != nil {
			return e
		}
		if owners[uint32(command.Process.Pid)] {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("原代理未重新监听共享服务，请查看 MyVMGuestTools\\logs")
}

func signalReady(name string) error {
	if name == "" {
		return nil
	}
	event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, windows.StringToUTF16Ptr(name))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	return windows.SetEvent(event)
}

// StartAgent waits for this child to bind both private command and local folder listeners.
func StartAgent(target string) (func() error, error) {
	name := "Local\\MyVMGuestReady." + rand.Text()
	_, attributes, err := sessionName(target)
	if err != nil {
		return nil, err
	}
	event, err := windows.CreateEvent(attributes, 1, 0, windows.StringToUTF16Ptr(name))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(event)
	command := exec.Command(target, "--install=false", "--ready-event", name)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err = command.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()
	stop := func() error {
		select {
		case <-done:
			return nil
		default:
		}
		if e := command.Process.Kill(); e != nil && !errors.Is(e, os.ErrProcessDone) {
			return e
		}
		select {
		case <-done:
			return nil
		case <-time.After(3 * time.Second):
			return errors.New("新代理未退出，保留旧程序以供恢复")
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case e := <-done:
			return nil, fmt.Errorf("增强代理启动后退出: %v", e)
		default:
		}
		state, e := windows.WaitForSingleObject(event, 100)
		if e != nil {
			return nil, errors.Join(e, stop())
		}
		if state == windows.WAIT_OBJECT_0 {
			return stop, nil
		}
	}
	return nil, errors.Join(errors.New("增强代理未报告就绪，请查看 MyVMGuestTools\\logs"), stop())
}
