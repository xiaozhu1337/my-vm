//go:build windows

package guestops

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func receiveTemporary(path string, overwrite bool) (*os.File, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	if overwrite {
		existing, e := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if e == nil {
			descriptor = existing
		} else if !errors.Is(e, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(e, windows.ERROR_PATH_NOT_FOUND) {
			return nil, e
		}
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	for range 20 {
		name := filepath.Join(filepath.Dir(path), ".my-vm-copy-"+rand.Text())
		native, e := windows.UTF16PtrFromString(name)
		if e != nil {
			return nil, e
		}
		handle, e := windows.CreateFile(native, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, &attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if errors.Is(e, windows.ERROR_FILE_EXISTS) || errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if e != nil {
			return nil, e
		}
		return os.NewFile(uintptr(handle), name), nil
	}
	return nil, os.ErrExist
}
