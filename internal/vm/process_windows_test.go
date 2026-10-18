package vm

import (
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func requireVisibleQEMUWindow(t *testing.T, pid int) {
	t.Helper()
	user32 := windows.NewLazySystemDLL("user32.dll")
	enumWindows := user32.NewProc("EnumWindows")
	getPID := user32.NewProc("GetWindowThreadProcessId")
	isVisible := user32.NewProc("IsWindowVisible")
	getClass := user32.NewProc("GetClassNameW")
	visible := false
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var owner uint32
		getPID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner != uint32(pid) {
			return 1
		}
		shown, _, _ := isVisible.Call(hwnd)
		if shown == 0 {
			return 1
		}
		var name [256]uint16
		getClass.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if strings.HasPrefix(windows.UTF16ToString(name[:]), "SDL") {
			visible = true
			return 0
		}
		return 1
	})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		enumWindows.Call(callback, 0)
		if visible {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("QEMU is running but its SDL guest window is not visible")
		}
	}
}
