//go:build windows

package hcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var virtualDisk = windows.NewLazySystemDLL("virtdisk.dll")

type nativeDiskProgress struct {
	Status         uint32
	_              uint32
	Current, Total uint64
}

func openDisk(path string, readOnly bool) (windows.Handle, error) {
	vendor, _ := windows.GUIDFromString("{EC984AEC-A0F9-47E9-901F-71415A66345B}")
	kind := storageType{Device: 3, Vendor: vendor}
	options := struct {
		Version               uint32
		GetInfoOnly, ReadOnly int32
		Resiliency            windows.GUID
	}{Version: 2}
	if readOnly {
		options.GetInfoOnly, options.ReadOnly = 1, 1
	}
	var handle windows.Handle
	code, _, _ := virtualDisk.NewProc("OpenVirtualDisk").Call(uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(wide(path))), 0, 0, uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&handle)))
	if code != 0 {
		return 0, windows.Errno(code)
	}
	return handle, nil
}

// ForkDisk switches the existing attached disk to a new differencing backing file.
// Guest CPUs and the HCS session keep running throughout the operation.
func ForkDisk(ctx context.Context, source, target, owner string, progress func(int64, int64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := virtualDisk.NewProc("ForkVirtualDisk").Find(); err != nil {
		return fmt.Errorf("此 Windows 版本不支持在线差分盘快照: %w", err)
	}
	handle, err := openDisk(source, false)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	// Pre-authorization is required when the VM worker activates the new backing file.
	if err = CreateDisk(target, 1); err != nil {
		return err
	}
	if err = PrepareFile(owner, target, ""); err != nil {
		return err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	path := wide(target)
	options := struct {
		Version uint32
		_       uint32
		Path    *uint16
	}{Version: 1, Path: path}
	code, _, _ := virtualDisk.NewProc("ForkVirtualDisk").Call(uintptr(handle), 1, uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&overlapped)))
	if code != 0 && code != uintptr(windows.ERROR_IO_PENDING) {
		return windows.Errno(code)
	}
	if code == uintptr(windows.ERROR_IO_PENDING) {
		err = waitDisk(ctx, handle, &overlapped, progress, func() error {
			result, _, _ := virtualDisk.NewProc("CompleteForkVirtualDisk").Call(uintptr(handle))
			if result != 0 {
				return windows.Errno(result)
			}
			return nil
		})
	}
	runtime.KeepAlive(path)
	return err
}

func waitDisk(ctx context.Context, handle windows.Handle, overlapped *windows.Overlapped, progress func(int64, int64), commit func() error) error {
	committed, cancelled := false, false
	var failure error
	for {
		var p nativeDiskProgress
		code, _, _ := virtualDisk.NewProc("GetVirtualDiskOperationProgress").Call(uintptr(handle), uintptr(unsafe.Pointer(overlapped)), uintptr(unsafe.Pointer(&p)))
		if code != 0 {
			// Drain native I/O before releasing the OVERLAPPED and file handle.
			if failure == nil {
				failure = windows.Errno(code)
			}
			_ = windows.CancelIoEx(handle, overlapped)
			state, waitErr := windows.WaitForSingleObject(overlapped.HEvent, 100)
			if state == windows.WAIT_OBJECT_0 {
				return failure
			}
			if waitErr != nil {
				return errors.Join(failure, waitErr)
			}
			continue
		}
		if p.Status != uint32(windows.ERROR_IO_PENDING) {
			if failure != nil {
				return failure
			}
			if cancelled {
				return ctx.Err()
			}
			if p.Status != 0 {
				return windows.Errno(p.Status)
			}
			if progress != nil {
				progress(int64(p.Total), int64(p.Total))
			}
			return nil
		}
		if progress != nil && p.Total > 0 {
			progress(int64(p.Current), int64(p.Total))
		}
		// Once the backing switch starts, finish it and let the caller commit its journal.
		if !committed && !cancelled && ctx.Err() != nil {
			failure = windows.CancelIoEx(handle, overlapped)
			if errors.Is(failure, windows.ERROR_NOT_FOUND) {
				failure = nil
			}
			cancelled = true
		}
		if commit != nil && !committed && !cancelled && p.Total > 0 && p.Current >= p.Total {
			committed = true
			if err := commit(); err != nil {
				failure = err
				_ = windows.CancelIoEx(handle, overlapped)
			}
		}
		if _, err := windows.WaitForSingleObject(overlapped.HEvent, 100); err != nil && failure == nil {
			failure = err
			_ = windows.CancelIoEx(handle, overlapped)
		}
	}
}

func CreateDifferencingDisk(path, parent string) error {
	return createDiskFrom(context.Background(), path, parent, "", nil)
}

// ExportDisk materializes the visible contents of a leaf and all its parents.
func ExportDisk(ctx context.Context, path, source string, progress func(int64, int64)) error {
	guard, err := windows.CreateFile(wide(source), windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(guard)
	if err = createDiskFrom(ctx, path, "", source, progress); err != nil {
		return err
	}
	if progress != nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		progress(info.Size(), info.Size())
	}
	return nil
}

func createDiskFrom(ctx context.Context, path, parent, source string, progress func(int64, int64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	vendor, _ := windows.GUIDFromString("{EC984AEC-A0F9-47E9-901F-71415A66345B}")
	kind := storageType{Device: 3, Vendor: vendor}
	options := diskOptions{Version: 2}
	var parentPath, sourcePath *uint16
	if parent != "" {
		parentPath = wide(parent)
		options.ParentPath = uintptr(unsafe.Pointer(parentPath))
		options.ParentType = kind
	}
	if source != "" {
		sourcePath = wide(source)
		options.SourcePath = uintptr(unsafe.Pointer(sourcePath))
		options.SourceType = kind
	}
	var handle windows.Handle
	var overlapped windows.Overlapped
	var nativeOverlapped uintptr
	if source != "" {
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(event)
		overlapped.HEvent = event
		nativeOverlapped = uintptr(unsafe.Pointer(&overlapped))
	}
	code, _, _ := virtualDisk.NewProc("CreateVirtualDisk").Call(uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(wide(path))), 0, 0, 0, 0, uintptr(unsafe.Pointer(&options)), nativeOverlapped, uintptr(unsafe.Pointer(&handle)))
	if handle != 0 {
		defer windows.CloseHandle(handle)
	}
	var err error
	if code == uintptr(windows.ERROR_IO_PENDING) {
		err = waitDisk(ctx, handle, &overlapped, progress, nil)
	} else if code != 0 {
		err = windows.Errno(code)
	}
	runtime.KeepAlive(parentPath)
	runtime.KeepAlive(sourcePath)
	return err
}

// DiskParent returns the resolved parent path, or an empty string for an independent disk.
func DiskParent(path string) (string, error) {
	handle, err := openDisk(path, true)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	kind := struct {
		Version uint32
		_       uint32
		Type    uint32
		Extra   [32]byte
	}{Version: 7}
	size := uint32(unsafe.Sizeof(kind))
	code, _, _ := virtualDisk.NewProc("GetVirtualDiskInformation").Call(uintptr(handle), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&kind)), 0)
	if code != 0 {
		return "", windows.Errno(code)
	}
	if kind.Type != 4 {
		return "", nil
	}
	parent := struct {
		Version  uint32
		_        uint32
		Resolved int32
		Path     [32768]uint16
	}{Version: 3}
	size = uint32(unsafe.Sizeof(parent))
	code, _, _ = virtualDisk.NewProc("GetVirtualDiskInformation").Call(uintptr(handle), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&parent)), 0)
	if code != 0 {
		return "", windows.Errno(code)
	}
	return filepath.Clean(windows.UTF16ToString(parent.Path[:])), nil
}
