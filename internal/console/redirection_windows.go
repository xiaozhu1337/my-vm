//go:build windows

package console

import (
	"fmt"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

func (v *viewer) configureInput() error {
	if err := put(v.advanced, "RelativeMouseMode", !v.wayland); err != nil {
		return err
	}
	secured, err := property(v.client, "SecuredSettings3")
	if err != nil {
		return err
	}
	defer secured.Release()
	if err := put(secured, "KeyboardHookMode", 1); err != nil {
		return err
	}
	// The extended setting was introduced in Windows 11 24H2.
	if v.wayland && windows.RtlGetVersion().BuildNumber < 26100 {
		return nil
	}
	allow := ole.VARIANT{VT: ole.VT_BOOL, Val: -1}
	if v.wayland {
		// Absolute desktop input must not be overridden by relative mouse negotiation.
		allow.Val = 0
	}
	if err := extended(v.client, "AllowRelativeMouseMode", allow); err != nil && v.wayland {
		return fmt.Errorf("关闭 Linux 相对鼠标模式: %w", err)
	}
	return nil
}

type guiThreadInfo struct {
	size, flags                                   uint32
	active, focus, capture, menu, moveSize, caret uintptr
	caretRect                                     [4]int32
}

func rendererFocusAllowed(host, renderer, foreground uintptr, inputDown bool, guiFlags uint32) bool {
	return host != 0 && renderer != 0 && (foreground == host || foreground == renderer) && !inputDown && guiFlags&0x1e == 0
}

func pointerFocusTarget(host, renderer, foreground, pointerRoot uintptr) uintptr {
	if host != 0 && renderer != 0 && (foreground == host || foreground == renderer) && (pointerRoot == host || pointerRoot == renderer) {
		return pointerRoot
	}
	return 0
}

var (
	getCursorPosProc    = user32.NewProc("GetCursorPos")
	windowFromPointProc = user32.NewProc("WindowFromPoint")
	getAncestorProc     = user32.NewProc("GetAncestor")
)

func cursorRoot() uintptr {
	var point [2]int32
	if ok, _, _ := getCursorPosProc.Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
		return 0
	}
	packed := uintptr(uint64(uint32(point[0])) | uint64(uint32(point[1]))<<32)
	hit, _, _ := windowFromPointProc.Call(packed)
	root, _, _ := getAncestorProc.Call(hit, 2)
	return root
}

func inputKeysReleased(queued [256]byte, physical func(uintptr) uintptr) bool {
	for key := 1; key < len(queued); key++ {
		if queued[key]&0x80 != 0 || physical(uintptr(key))&0x8000 != 0 {
			return false
		}
	}
	return true
}

func inputHandoverIdle(host, renderer uintptr) bool {
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	if host == 0 || renderer == 0 {
		return false
	}
	// Keep key-down and key-up on the same side, including held letters and arrow keys.
	var queued [256]byte
	if ok, _, _ := user32.NewProc("GetKeyboardState").Call(uintptr(unsafe.Pointer(&queued))); ok == 0 {
		return false
	}
	asyncState := user32.NewProc("GetAsyncKeyState")
	if !inputKeysReleased(queued, func(key uintptr) uintptr {
		state, _, _ := asyncState.Call(key)
		return state
	}) {
		return false
	}
	for _, window := range []uintptr{host, renderer} {
		thread, _, _ := user32.NewProc("GetWindowThreadProcessId").Call(window, 0)
		if thread == 0 {
			return false
		}
		info := guiThreadInfo{size: uint32(unsafe.Sizeof(guiThreadInfo{}))}
		if ok, _, _ := user32.NewProc("GetGUIThreadInfo").Call(thread, uintptr(unsafe.Pointer(&info))); ok == 0 || info.capture != 0 || !rendererFocusAllowed(host, renderer, foreground, false, info.flags) {
			return false
		}
	}
	current, _, _ := user32.NewProc("GetForegroundWindow").Call()
	return current == foreground
}

func rendererCanFocus(host, renderer uintptr) bool {
	if !inputHandoverIdle(host, renderer) {
		return false
	}
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	return pointerFocusTarget(host, renderer, foreground, cursorRoot()) == renderer
}

func (v *viewer) focusRenderer() {
	if v.closed || v.host == nil || v.host.Active == nil || !rendererCanFocus(v.hwnd, uintptr(v.host.Active.Window)) {
		return
	}
	if allowed, _, _ := user32.NewProc("AllowSetForegroundWindow").Call(uintptr(v.host.Active.PID)); allowed != 0 {
		v.host.Active.Send("focus")
	}
}

func (v *viewer) releaseGuestFocus() {
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	if v.rendererMode && !v.closed && v.validParent() && foreground == v.hwnd && cursorRoot() == v.parent && inputHandoverIdle(v.parent, v.hwnd) && cursorRoot() == v.parent {
		user32.NewProc("SetForegroundWindow").Call(v.parent)
	}
}

func (v *viewer) syncPointerFocus(force bool) {
	if v.closed || v.host == nil && !v.rendererMode {
		return
	}
	root := cursorRoot()
	if !force && root == v.pointerRoot {
		return
	}
	v.pointerRoot = root
	if v.rendererMode {
		v.releaseGuestFocus()
		return
	}
	active := v.host.Active
	if active == nil || !active.Connected || active.Failed {
		return
	}
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	target := pointerFocusTarget(v.hwnd, uintptr(active.Window), foreground, root)
	if target == 0 || target == foreground {
		return
	}
	if target == uintptr(active.Window) {
		v.focusRenderer()
	} else if inputHandoverIdle(v.hwnd, uintptr(active.Window)) {
		active.Send("release-focus")
	}
}

func (v *viewer) configureClipboard() error {
	// Keep the file clipboard channel available while every host drive stays unselected.
	if err := put(v.advanced, "RedirectDrives", v.enhanced && v.clipboard); err != nil {
		return err
	}
	if err := call(v.nonScriptable, 25, 0); err != nil {
		return err
	}
	var drives *ole.IUnknown
	if err := call(v.nonScriptable, 30, uintptr(unsafe.Pointer(&drives))); err != nil {
		return err
	}
	if drives == nil {
		return fmt.Errorf("RDP 驱动器列表无效")
	}
	defer drives.Release()
	if err := call(drives, 3, 0); err != nil {
		return err
	}
	var count uint32
	if err := call(drives, 5, uintptr(unsafe.Pointer(&count))); err != nil {
		return err
	}
	if count > 256 {
		return fmt.Errorf("RDP 驱动器数量异常")
	}
	for i := uint32(0); i < count; i++ {
		var drive *ole.IUnknown
		if err := call(drives, 4, uintptr(i), uintptr(unsafe.Pointer(&drive))); err != nil {
			return err
		}
		if drive == nil {
			return fmt.Errorf("RDP 驱动器条目无效")
		}
		err := call(drive, 4, 0)
		drive.Release()
		if err != nil {
			return err
		}
	}
	return nil
}
