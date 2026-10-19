//go:build windows

package vm

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"
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
		var name [128]uint16
		displayUser32.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if windows.UTF16ToString(name[:]) != "" {
			var title [256]uint16
			displayUser32.NewProc("GetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
			if strings.HasPrefix(windows.UTF16ToString(title[:]), "VM · ") {
				displayFound = hwnd
				return 0
			}
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
func (m *Manager) ShowDisplay(id string) error { return m.showDisplay(id, false, true) }
func (m *Manager) ShowDisplayMode(id string, enhanced bool) error {
	return m.showDisplay(id, enhanced, false)
}
func (m *Manager) showDisplay(id string, enhanced, automatic bool) error {
	lock := m.operation(id)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return errors.New("虚拟机未运行")
	}
	modeSpecified := !automatic
	if s.paused {
		automatic = false
	}
	if modeSpecified && s.console != nil && (s.consoleAutomatic || s.consoleEnhanced != enhanced) {
		return errors.New("请先手动关闭现有控制台窗口，再切换基础/增强模式；虚拟机将继续运行")
	}
	if s.console != nil {
		hwnd := guestWindow(s.console.Process.Pid)
		if hwnd != 0 {
			restoreDisplay.Call(hwnd, 9)
			focusDisplay.Call(hwnd)
			return nil
		}
		return errors.New("客户机控制台正在启动，请稍后重试")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	pipe := basicPipe(s.machine)
	if enhanced {
		pipe = `\\.\pipe\my-vm-` + id + ".enhanced"
	}
	args := []string{"--console", "--pipe", pipe, "--vmid", s.runtimeID, "--title", s.machine.Name}
	if automatic {
		args = append(args, "--auto")
	}
	if enhanced {
		args = append(args, "--enhanced")
	}
	if (enhanced || automatic) && s.machine.IsLinux() {
		args = append(args, "--linux")
		if s.machine.DesktopMode() == "wayland" {
			args = append(args, "--wayland")
		} else if s.machine.LinuxDesktop == "x11" {
			args = append(args, "--x11")
		}
	}
	if (enhanced || automatic) && s.machine.Clipboard {
		args = append(args, "--clipboard")
	}
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	command.Stdout = m.Log
	command.Stderr = m.Log
	if err = command.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	s.console = command
	s.consoleEnhanced = enhanced
	s.consoleAutomatic = automatic
	m.mu.Unlock()
	go func() {
		err := command.Wait()
		lock.Lock()
		m.mu.Lock()
		if s.console == command {
			s.console = nil
		}
		m.mu.Unlock()
		lock.Unlock()
		if err != nil && m.Log != nil {
			fmt.Fprintln(m.Log, "控制台退出:", err)
		}
	}()
	return nil
}
