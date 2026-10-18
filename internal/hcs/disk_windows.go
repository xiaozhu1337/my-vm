//go:build windows

package hcs

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

type storageType struct {
	Device uint32
	Vendor windows.GUID
}
type diskOptions struct {
	Version                                   uint32
	_                                         uint32
	Unique                                    windows.GUID
	MaximumSize                               uint64
	BlockSize, SectorSize, PhysicalSectorSize uint32
	_                                         uint32
	ParentPath, SourcePath                    uintptr
	OpenFlags                                 uint32
	ParentType, SourceType                    storageType
	Resiliency                                windows.GUID
}

func CreateDisk(path string, sizeGiB int) error {
	vendor, _ := windows.GUIDFromString("{EC984AEC-A0F9-47E9-901F-71415A66345B}")
	kind := storageType{Device: 3, Vendor: vendor}
	options := diskOptions{Version: 2, MaximumSize: uint64(sizeGiB) << 30, SectorSize: 512, PhysicalSectorSize: 4096}
	var handle windows.Handle
	code, _, _ := windows.NewLazySystemDLL("virtdisk.dll").NewProc("CreateVirtualDisk").Call(uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(wide(path))), 0, 0, 0, 0, uintptr(unsafe.Pointer(&options)), 0, uintptr(unsafe.Pointer(&handle)))
	if code != 0 {
		return windows.Errno(code)
	}
	return windows.CloseHandle(handle)
}
