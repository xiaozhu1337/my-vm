//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
	"my-vm/internal/sharing"
)

var mpr = windows.NewLazySystemDLL("mpr.dll")

func runDesktop(context.Context, io.Writer) {}

func installUser() error {
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	target := filepath.Join(root, "MyVMGuestTools", "my-vm-guest.exe")
	if err = copySelf(target); err != nil {
		return err
	}
	run, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	err = run.SetStringValue("MyVMGuestTools", `"`+target+`"`)
	run.Close()
	if err != nil {
		return err
	}
	if err = elevatePrepare(target); err != nil {
		return err
	}
	cmd := exec.Command(target)
	if err = cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
func prepareSystem() error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\WebClient\Parameters`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	err = key.SetDWordValue("FileSizeLimitInBytes", 0xffffffff)
	key.Close()
	if err != nil {
		return err
	}
	services, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer services.Disconnect()
	if err = startService(services, "WebClient"); err != nil {
		return err
	}
	return prepareRemoteDesktop(services)
}
func startService(manager *mgr.Mgr, name string) error {
	service, err := manager.OpenService(name)
	if err != nil {
		return fmt.Errorf("客户机缺少 %s: %w", name, err)
	}
	defer service.Close()
	cfg, err := service.Config()
	if err != nil {
		return err
	}
	cfg.StartType = mgr.StartAutomatic
	if err = service.UpdateConfig(cfg); err != nil {
		return err
	}
	err = service.Start()
	if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return nil
	}
	return err
}
func connection(drive string) (string, error) {
	var buffer [2048]uint16
	size := uint32(len(buffer))
	code, _, _ := mpr.NewProc("WNetGetConnectionW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(drive))), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if code == 2250 {
		return "", nil
	}
	if code != 0 {
		return "", windows.Errno(code)
	}
	return windows.UTF16ToString(buffer[:]), nil
}
func samePath(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, `\`), strings.TrimRight(b, `\`))
}
func mapShares(shares []sharing.Share) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\MyVMGuestTools\MappedFolders`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	desired := map[string]bool{}
	for _, s := range shares {
		if !safeShare(s.Name) {
			return errors.New("主机返回无效的共享名称")
		}
		desired[`\\127.0.0.1@18389\DavWWWRoot\dav\`+s.Name] = true
	}
	names, err := key.ReadValueNames(0)
	if err != nil {
		return err
	}
	mapped := map[string]bool{}
	for _, drive := range names {
		if len(drive) != 2 || drive[0] < 'D' || drive[0] > 'Z' || drive[1] != ':' {
			continue
		}
		previous, _, err := key.GetStringValue(drive)
		if err != nil {
			return err
		}
		current, err := connection(drive)
		if err != nil {
			return err
		}
		keep := false
		for path := range desired {
			if samePath(path, previous) {
				if samePath(current, previous) {
					mapped[path] = true
					keep = true
				}
				break
			}
		}
		if keep {
			continue
		}
		if samePath(current, previous) && strings.HasPrefix(previous, `\\127.0.0.1@18389\DavWWWRoot\dav\`) {
			code, _, _ := mpr.NewProc("WNetCancelConnection2W").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(drive))), 0, 0)
			if code != 0 {
				return fmt.Errorf("无法断开旧共享 %s: %w", drive, windows.Errno(code))
			}
		}
		if err = key.DeleteValue(drive); err != nil {
			return err
		}
	}
	for _, share := range shares {
		remote := `\\127.0.0.1@18389\DavWWWRoot\dav\` + share.Name
		if mapped[remote] {
			continue
		}
		mask, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetLogicalDrives").Call()
		drive := ""
		for letter := byte('Z'); letter >= 'D'; letter-- {
			if mask&(1<<uint(letter-'A')) == 0 {
				candidate := string(letter) + ":"
				current, err := connection(candidate)
				if err == nil && current == "" {
					drive = candidate
					break
				}
			}
		}
		if drive == "" {
			return errors.New("没有可用的 D 到 Z 盘符")
		}
		resource := struct {
			Scope, Type, DisplayType, Usage  uint32
			Local, Remote, Comment, Provider *uint16
		}{Type: 1, Local: windows.StringToUTF16Ptr(drive), Remote: windows.StringToUTF16Ptr(remote)}
		code, _, _ := mpr.NewProc("WNetAddConnection2W").Call(uintptr(unsafe.Pointer(&resource)), 0, 0, 4)
		if code != 0 {
			return fmt.Errorf("映射 %s: %w", remote, windows.Errno(code))
		}
		if err = key.SetStringValue(drive, remote); err != nil {
			return err
		}
	}
	return nil
}
