//go:build windows

package rdpauth

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"unicode/utf16"
	"unsafe"
)

var credentialAPI = windows.NewLazySystemDLL("advapi32.dll")

type nativeCredential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             windows.Filetime
	BlobSize                uint32
	Blob                    *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, UserName   *uint16
}

func Read(target string) (string, []uint16, error) {
	var native *nativeCredential
	ok, _, err := credentialAPI.NewProc("CredReadW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(target))), 1, 0, uintptr(unsafe.Pointer(&native)))
	if ok == 0 {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("读取 Linux 登录凭据: %w", err)
	}
	defer credentialAPI.NewProc("CredFree").Call(uintptr(unsafe.Pointer(native)))
	if native.Type != 1 || native.BlobSize == 0 || native.BlobSize%2 != 0 || native.BlobSize > 512 || native.Blob == nil || native.UserName == nil {
		return "", nil, errors.New("保存的 Linux 登录凭据无效，请先忘记登录凭据")
	}
	blob := unsafe.Slice((*uint16)(unsafe.Pointer(native.Blob)), int(native.BlobSize)/2)
	defer clear(blob)
	return windows.UTF16PtrToString(native.UserName), append([]uint16(nil), blob...), nil
}
func Write(target, username string, password []uint16) error {
	if len(password) == 0 || len(password) > 256 {
		return errors.New("Linux 登录密码长度无效")
	}
	native := nativeCredential{Type: 1, TargetName: windows.StringToUTF16Ptr(target), BlobSize: uint32(len(password) * 2), Blob: (*byte)(unsafe.Pointer(&password[0])), Persist: 2, UserName: windows.StringToUTF16Ptr(username)}
	ok, _, err := credentialAPI.NewProc("CredWriteW").Call(uintptr(unsafe.Pointer(&native)), 0)
	if ok == 0 {
		return fmt.Errorf("保存 Linux 登录凭据: %w", err)
	}
	return nil
}
func Delete(target string) error {
	ok, _, err := credentialAPI.NewProc("CredDeleteW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(target))), 1, 0)
	if ok == 0 && !errors.Is(err, windows.ERROR_NOT_FOUND) {
		return fmt.Errorf("忘记 Linux 登录凭据: %w", err)
	}
	return nil
}
func Save(pipe string, login *Login) error {
	if err := login.Validate(); err != nil {
		return err
	}
	password := utf16.Encode([]rune(string(login.Password)))
	defer clear(password)
	return Write(Target(pipe), login.Username, password)
}
