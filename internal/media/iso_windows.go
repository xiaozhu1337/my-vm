//go:build windows

package media

import (
	"fmt"
	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// CreateISO uses Windows' ISO/Joliet writer; it never replaces an existing image.
func CreateISO(directory, target string) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err = ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		return err
	}
	defer ole.CoUninitialize()
	unknown, err := oleutil.CreateObject("IMAPI2FS.MsftFileSystemImage")
	if err != nil {
		return err
	}
	defer unknown.Release()
	image, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer image.Release()
	value, err := oleutil.CallMethod(image, "ChooseImageDefaultsForMediaType", 12)
	if value != nil {
		value.Clear()
	}
	if err != nil {
		return err
	}
	value, err = oleutil.PutProperty(image, "FileSystemsToCreate", 3)
	if value != nil {
		value.Clear()
	}
	if err != nil {
		return err
	}
	value, err = oleutil.PutProperty(image, "VolumeName", "MYVMTOOLS")
	if value != nil {
		value.Clear()
	}
	if err != nil {
		return err
	}
	root, err := oleutil.GetProperty(image, "Root")
	if err != nil {
		return err
	}
	defer root.Clear()
	value, err = oleutil.CallMethod(root.ToIDispatch(), "AddTree", directory, false)
	if value != nil {
		value.Clear()
	}
	if err != nil {
		return err
	}
	result, err := oleutil.CallMethod(image, "CreateResultImage")
	if err != nil {
		return err
	}
	defer result.Clear()
	stream, err := oleutil.GetProperty(result.ToIDispatch(), "ImageStream")
	if err != nil {
		return err
	}
	defer stream.Clear()
	raw := stream.ToIUnknown()
	if raw == nil {
		return fmt.Errorf("ISO 数据流无效")
	}
	table := (*[14]uintptr)(unsafe.Pointer(raw.RawVTable))
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	buffer := make([]byte, 65536)
	for {
		var size uint32
		hr, _, _ := syscall.SyscallN(table[3], uintptr(unsafe.Pointer(raw)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), uintptr(unsafe.Pointer(&size)))
		if int32(hr) < 0 {
			return fmt.Errorf("读取 ISO 数据流失败: HRESULT 0x%08X", uint32(hr))
		}
		if size == 0 {
			break
		}
		if size > uint32(len(buffer)) {
			return fmt.Errorf("ISO 数据流长度无效")
		}
		if _, err = file.Write(buffer[:size]); err != nil {
			return err
		}
	}
	return file.Sync()
}
