//go:build windows

package console

import (
	"fmt"
	ole "github.com/go-ole/go-ole"
	"unsafe"
)

func (v *viewer) configureInput() error {
	secured, err := property(v.client, "SecuredSettings3")
	if err != nil {
		return err
	}
	defer secured.Release()
	return put(secured, "KeyboardHookMode", 1)
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
