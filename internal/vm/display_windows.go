package vm

import (
	"errors"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	displayUser32  = windows.NewLazySystemDLL("user32.dll")
	enumDisplays   = displayUser32.NewProc("EnumWindows")
	displayPID     = displayUser32.NewProc("GetWindowThreadProcessId")
	displayClass   = displayUser32.NewProc("GetClassNameW")
	restoreDisplay = displayUser32.NewProc("ShowWindow")
	focusDisplay   = displayUser32.NewProc("SetForegroundWindow")
)

var (
	displayMu    sync.Mutex
	displayFound uintptr
)

// Windows callbacks consume a permanent slot, so reuse one for every search.
var displayCallback = syscall.NewCallback(func(hwnd, owner uintptr) uintptr {
	var pid uint32
	displayPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != uint32(owner) {
		return 1
	}
	var class [256]uint16
	displayClass.Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
	if strings.HasPrefix(windows.UTF16ToString(class[:]), "SDL") {
		displayFound = hwnd
		return 0
	}
	return 1
})

func guestWindow(pid int) uintptr {
	displayMu.Lock()
	defer displayMu.Unlock()
	displayFound = 0
	enumDisplays.Call(displayCallback, uintptr(pid))
	return displayFound
}

func (m *Manager) ShowDisplay(id string) error {
	m.mu.Lock()
	s := m.sessions[id]
	pid := 0
	if s != nil {
		pid = s.cmd.Process.Pid
	}
	m.mu.Unlock()
	if pid == 0 {
		return errors.New("虚拟机未运行")
	}
	hwnd := guestWindow(pid)
	if hwnd == 0 {
		return errors.New("客户机窗口尚未就绪，请稍后重试")
	}
	restoreDisplay.Call(hwnd, 9) // SW_RESTORE also restores a minimized guest window.
	focusDisplay.Call(hwnd)
	return nil
}
