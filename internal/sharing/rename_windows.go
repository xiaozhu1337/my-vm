//go:build windows

package sharing

import (
	"encoding/binary"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

func renameNoReplace(root *os.Root, source, target string) error {
	sourceName, targetName := filepath.Base(source), filepath.Base(target)
	for _, name := range []string{sourceName, targetName} {
		if name == "." || name == ".." || strings.ContainsAny(name, ":\\/\x00") {
			return errors.New("无效的重命名目标")
		}
	}
	from, err := root.Open(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := root.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer to.Close()
	name, err := windows.NewNTUnicodeString(sourceName)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(from.Fd()), ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var file windows.Handle
	var status windows.IO_STATUS_BLOCK
	// Open the leaf itself relative to the already confined parent, including reparse points.
	err = windows.NtCreateFile(&file, windows.DELETE|windows.SYNCHRONIZE, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(file)
	utf16, err := windows.UTF16FromString(targetName)
	if err != nil {
		return err
	}
	utf16 = utf16[:len(utf16)-1]
	buffer := make([]byte, 20+2*len(utf16))
	// FILE_RENAME_INFO uses ReplaceIfExists=false; the kernel arbitrates destination races.
	binary.LittleEndian.PutUint64(buffer[8:16], uint64(to.Fd()))
	binary.LittleEndian.PutUint32(buffer[16:20], uint32(len(utf16)*2))
	for i, value := range utf16 {
		binary.LittleEndian.PutUint16(buffer[20+2*i:], value)
	}
	return windows.NtSetInformationFile(file, &status, &buffer[0], uint32(len(buffer)), 10)
}
