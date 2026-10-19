//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

const preparationReportedExit = 20

var errPreparationReported = errors.New("系统组件准备失败，具体错误已在管理员安装窗口显示")

func installationResult(err error) {
	if errors.Is(err, errPreparationReported) {
		return
	}
	text := "增强安装完成。已启用共享目录、远程桌面和登录自启动。\n请确认此账户设置了登录密码，然后在主机点击“增强桌面”。"
	flags := uintptr(0x40)
	if err != nil {
		logDirectory := `MyVMGuestTools\logs`
		if root, e := os.UserConfigDir(); e == nil {
			logDirectory = filepath.Join(root, "MyVMGuestTools", "logs")
		}
		text = "增强安装失败：" + err.Error() + "\n详细记录位于：" + logDirectory
		flags = 0x10
	}
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(text))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("MyVM Guest Enhancements"))), flags)
}

func elevatePrepare(exe string) error {
	original, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	info := struct {
		Size, Mask                        uint32
		Window                            uintptr
		Verb, File, Parameters, Directory *uint16
		Show                              int32
		Instance, IDList                  uintptr
		Class                             *uint16
		ClassKey                          uintptr
		HotKey                            uint32
		Icon                              uintptr
		Process                           windows.Handle
	}{Mask: 0x40, Verb: windows.StringToUTF16Ptr("runas"), File: windows.StringToUTF16Ptr(exe),
		Parameters: windows.StringToUTF16Ptr("--prepare --user-sid " + original.User.Sid.String() + " --media " + syscall.EscapeArg(mediaRoot)), Show: 1}
	info.Size = uint32(unsafe.Sizeof(info))
	ok, _, err := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return fmt.Errorf("管理员授权未完成: %w", err)
	}
	if info.Process == 0 {
		return errors.New("未获得增强安装进程句柄")
	}
	defer windows.CloseHandle(info.Process)
	if _, err = windows.WaitForSingleObject(info.Process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err = windows.GetExitCodeProcess(info.Process, &code); err != nil {
		return err
	}
	return preparationExitError(code)
}

func preparationExitError(code uint32) error {
	if code == preparationReportedExit {
		return errPreparationReported
	}
	if code != 0 {
		return fmt.Errorf("系统组件准备失败（退出码 %d），请查看 MyVMGuestTools\\logs", code)
	}
	return nil
}

func grantDesktopUser(value string) error {
	sid, err := windows.StringToSid(value)
	if err != nil {
		return fmt.Errorf("原始桌面用户 SID 无效: %w", err)
	}
	if _, _, _, err = sid.LookupAccount(""); err != nil {
		return err
	}
	group, err := windows.CreateWellKnownSid(windows.WinBuiltinRemoteDesktopUsersSid)
	if err != nil {
		return err
	}
	name, _, _, err := group.LookupAccount("")
	if err != nil {
		return fmt.Errorf("客户机不支持远程桌面用户组: %w", err)
	}
	member := struct{ SID *windows.SID }{sid}
	status, _, _ := windows.NewLazySystemDLL("netapi32.dll").NewProc("NetLocalGroupAddMembers").Call(
		0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(name))), 0, uintptr(unsafe.Pointer(&member)), 1)
	if status != 0 && status != 1378 {
		return fmt.Errorf("授予原始桌面用户远程登录权限失败: %w", windows.Errno(status))
	}
	return nil
}

func prepareRemoteDesktop(services *mgr.Mgr) error {
	if err := grantDesktopUser(installUserSID); err != nil {
		return err
	}
	options := []struct {
		Path, Name string
		Value      uint32
	}{
		{`SYSTEM\CurrentControlSet\Control\Terminal Server`, "fDenyTSConnections", 0},
		{`SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp`, "UserAuthentication", 1},
	}
	for _, item := range options {
		key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, item.Path, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = key.SetDWordValue(item.Name, item.Value)
		key.Close()
		if err != nil {
			return err
		}
	}
	// The existing Hyper-V VM-session transport needs no new network firewall rules.
	return startService(services, "TermService")
}
