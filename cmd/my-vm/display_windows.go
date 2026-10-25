//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	vm "my-vm/library"
)

var displayUser32 = windows.NewLazySystemDLL("user32.dll")
var restoreDisplay = displayUser32.NewProc("ShowWindow")
var focusDisplay = displayUser32.NewProc("SetForegroundWindow")
var displayMu sync.Mutex
var displayFound uintptr
var displayCallback = syscall.NewCallback(func(hwnd, owner uintptr) uintptr {
	var pid uint32
	displayUser32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == uint32(owner) {
		var title [256]uint16
		displayUser32.NewProc("GetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
		if strings.HasPrefix(windows.UTF16ToString(title[:]), "VM · ") {
			displayFound = hwnd
			return 0
		}
	}
	return 1
})

func guestWindow(pid int) uintptr {
	displayMu.Lock()
	defer displayMu.Unlock()
	displayFound = 0
	displayUser32.NewProc("EnumWindows").Call(displayCallback, uintptr(pid))
	return displayFound
}

type displayWindow struct {
	command             *exec.Cmd
	runtime             string
	enhanced, automatic bool
}

// Display processes and native window calls are owned exclusively by the GUI.
func (a *application) showDisplay(id string, enhanced, automatic bool) error {
	data, err := a.manager.Execute(context.Background(), vm.Request{Action: "display-info", VM: id})
	if err != nil {
		return err
	}
	var info vm.DisplayInfo
	if err = json.Unmarshal(data, &info); err != nil {
		return err
	}
	a.displayLock.Lock()
	defer a.displayLock.Unlock()
	return a.openDisplay(id, info, enhanced, automatic)
}

// openDisplay requires displayLock, including while replacing a stale console process.
func (a *application) openDisplay(id string, info vm.DisplayInfo, enhanced, automatic bool) error {
	if current := a.displays[id]; current != nil {
		if current.runtime == info.RuntimeID {
			if !automatic {
				return errors.New("请在现有控制台工具栏切换基础/增强画面，或手动关闭窗口后重新打开；虚拟机将继续运行")
			}
			if hwnd := guestWindow(current.command.Process.Pid); hwnd != 0 {
				restoreDisplay.Call(hwnd, 9)
				focusDisplay.Call(hwnd)
				return nil
			}
			return errors.New("客户机控制台正在启动，请稍后重试")
		}
		_ = current.command.Process.Kill()
	}
	if info.Paused {
		automatic = false
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	pipe := `\\.\pipe\my-vm-` + id + ".basic"
	if enhanced {
		pipe = `\\.\pipe\my-vm-` + id + ".enhanced"
	}
	machine := info.Machine
	args := []string{"--console", "--pipe", pipe, "--vmid", info.RuntimeID, "--machine-id", id, "--title", machine.Name}
	if automatic {
		args = append(args, "--auto")
	}
	if enhanced {
		args = append(args, "--enhanced")
	}
	if machine.IsLinux() {
		args = append(args, "--linux")
		if machine.DesktopMode() == "wayland" {
			args = append(args, "--wayland")
		} else if machine.LinuxDesktop == "x11" {
			args = append(args, "--x11")
		}
	}
	if machine.Clipboard {
		args = append(args, "--clipboard")
	}
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	command.Stdout, command.Stderr = a.logger, a.logger
	if err := command.Start(); err != nil {
		return err
	}
	if a.displays == nil {
		a.displays = map[string]*displayWindow{}
	}
	current := &displayWindow{command: command, runtime: info.RuntimeID, enhanced: enhanced, automatic: automatic}
	a.displays[id] = current
	go func() {
		err := command.Wait()
		a.displayLock.Lock()
		if a.displays[id] == current {
			delete(a.displays, id)
		}
		a.displayLock.Unlock()
		if err != nil && a.logger != nil {
			fmt.Fprintln(a.logger, "控制台退出:", err)
		}
	}()
	return nil
}

type displayRestart struct {
	id       string
	previous *displayWindow
	info     vm.DisplayInfo
}

// Watch runtimes for both GUI and CLI operations, including snapshot rollback.
func (a *application) displayRestarts(ctx context.Context) []displayRestart {
	a.displayLock.Lock()
	current := make(map[string]*displayWindow, len(a.displays))
	for id, display := range a.displays {
		current[id] = display
	}
	a.displayLock.Unlock()
	var restarts []displayRestart
	for id, display := range current {
		if a.manager.State(id) == "已关机" || a.manager.Progress(id) != nil {
			continue
		}
		data, err := a.manager.Execute(ctx, vm.Request{Action: "display-info", VM: id})
		if err != nil {
			continue
		}
		var info vm.DisplayInfo
		if json.Unmarshal(data, &info) == nil && info.RuntimeID != "" && info.RuntimeID != display.runtime {
			restarts = append(restarts, displayRestart{id: id, previous: display, info: info})
		}
	}
	return restarts
}

func (a *application) syncDisplays(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, restart := range a.displayRestarts(ctx) {
		a.displayLock.Lock()
		var err error
		if a.displays[restart.id] == restart.previous {
			err = a.openDisplay(restart.id, restart.info, restart.previous.enhanced, restart.previous.automatic)
		}
		a.displayLock.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
