//go:build windows

package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var copyFileEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("CopyFileExW")
var copyCallbacks sync.Map
var copySequence atomic.Uint64
var copyCallback = syscall.NewCallback(func(total, transferred, streamSize, streamTransferred int64, stream, reason uint32, source, target, data uintptr) uintptr {
	if progress, ok := copyCallbacks.Load(data); ok {
		progress.(func(int64, int64))(transferred, total)
	}
	return 0
})

func copyExclusive(source, target string, progress func(int64, int64)) error {
	path, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	// Keep writers out even if another manager owns the disk.
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		if err == nil {
			err = os.ErrExist
		}
		return err
	}
	// Publish only a complete, flushed copy; never overwrite an existing destination.
	pending, err := os.CreateTemp(filepath.Dir(target), ".disk-copy-")
	if err != nil {
		return err
	}
	name := pending.Name()
	defer os.Remove(name)
	if err = pending.Close(); err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var callback, context uintptr
	if progress != nil {
		context = uintptr(copySequence.Add(1))
		copyCallbacks.Store(context, progress)
		defer copyCallbacks.Delete(context)
		callback = copyCallback
	}
	// The native copier handles sparse files and uses the OS's optimized I/O path.
	ok, _, copyErr := copyFileEx.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(destination)), callback, context, 0, 0)
	if ok == 0 {
		return fmt.Errorf("复制磁盘失败（原文件保留）: %w", copyErr)
	}
	output, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	err = output.Sync()
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	final, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(destination, final, windows.MOVEFILE_WRITE_THROUGH)
}
