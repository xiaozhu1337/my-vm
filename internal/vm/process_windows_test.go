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
	windowsFound := 0
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var owner uint32
		getPID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner != uint32(pid) {
			return 1
		}
		var name [256]uint16
		getClass.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		if strings.HasPrefix(windows.UTF16ToString(name[:]), "SDL") {
			windowsFound++
			shown, _, _ := isVisible.Call(hwnd)
			visible = visible || shown != 0
		}
		return 1
	})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		windowsFound = 0
		enumWindows.Call(callback, 0)
		if visible {
			if windowsFound != 1 {
				t.Fatalf("expected only the guest display, found %d SDL windows", windowsFound)
			}
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("QEMU is running but its SDL guest window is not visible")
		}
	}
}

func requirePerMonitorDPI(t *testing.T, hwnd uintptr) {
	t.Helper()
	user32 := windows.NewLazySystemDLL("user32.dll")
	context, _, _ := user32.NewProc("GetWindowDpiAwarenessContext").Call(hwnd)
	awareness, _, _ := user32.NewProc("GetAwarenessFromDpiAwarenessContext").Call(context)
	if awareness != 2 { // DPI_AWARENESS_PER_MONITOR_AWARE
		t.Fatalf("QEMU SDL window is not aware of per-monitor DPI: %d", awareness)
	}
}
