//go:build windows

package console

import (
	"fmt"
	"my-vm/internal/rdpauth"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

var credentialUI = windows.NewLazySystemDLL("credui.dll")

const forgetLoginCommand = 0x1e00

type loginCredential struct {
	username string
	password []uint16
	remember bool
}

func (c *loginCredential) clear() {
	if c != nil {
		clear(c.password)
		c.password = nil
	}
}

// Credential keys follow the stable VM pipe, never the shared loopback address or transient port.
func loginTarget(pipe string) string { return rdpauth.Target(pipe) }

func readLogin(target string) (*loginCredential, error) {
	username, password, err := rdpauth.Read(target)
	if err != nil || password == nil {
		return nil, err
	}
	return &loginCredential{username: username, password: password}, nil
}

func writeLogin(target string, login *loginCredential) error {
	return rdpauth.Write(target, login.username, login.password)
}

func deleteLogin(target string) error { return rdpauth.Delete(target) }

func promptLogin(hwnd uintptr, target, title string) (*loginCredential, error) {
	info := struct {
		Size             uint32
		Parent           uintptr
		Message, Caption *uint16
		Banner           uintptr
	}{Parent: hwnd, Message: windows.StringToUTF16Ptr("请输入 Linux 远程桌面凭据。勾选保存后，此虚拟机下次将自动登录。"), Caption: windows.StringToUTF16Ptr("My VM · " + title)}
	info.Size = uint32(unsafe.Sizeof(info))
	var username [514]uint16
	var password [257]uint16
	defer clear(password[:])
	var save int32
	// Return credentials to this process only; persist explicitly after successful RDP login.
	code, _, _ := credentialUI.NewProc("CredUIPromptForCredentialsW").Call(uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(target))), 0, 0,
		uintptr(unsafe.Pointer(&username[0])), uintptr(len(username)), uintptr(unsafe.Pointer(&password[0])), uintptr(len(password)), uintptr(unsafe.Pointer(&save)), 0x40000|0x80|0x40|0x2)
	if code != 0 {
		return nil, fmt.Errorf("Linux 登录已取消或凭据输入失败: %w", syscall.Errno(code))
	}
	length := 0
	for length < len(password) && password[length] != 0 {
		length++
	}
	name := windows.UTF16ToString(username[:])
	if name == "" || length == 0 || length > 256 {
		return nil, fmt.Errorf("请输入 Linux 远程桌面的用户名和密码")
	}
	return &loginCredential{username: name, password: append([]uint16(nil), password[:length]...), remember: save != 0}, nil
}

func chooseLogin(target string, read func(string) (*loginCredential, error), prompt func() (*loginCredential, error)) (*loginCredential, error) {
	login, err := read(target)
	if err != nil || login != nil {
		return login, err
	}
	return prompt()
}

func (v *viewer) configureLogin() error {
	v.loginTarget = loginTarget(v.pipe)
	login := v.automaticLogin
	var err error
	if login == nil {
		login, err = chooseLogin(v.loginTarget, readLogin, func() (*loginCredential, error) {
			// Older GNOME installs may have a keyring entry without a host credential record.
			capability := probeDisplay(v.vmid, "wayland-login", v.log)
			if recovered := capability.login(); recovered != nil {
				return recovered, nil
			}
			return promptLogin(v.hwnd, v.loginTarget, v.title)
		})
	}
	if err != nil {
		return err
	}
	defer func() {
		if v.pendingLogin != login {
			login.clear()
		}
	}()
	if err = put(v.client, "UserName", login.username); err != nil {
		return err
	}
	if err = setLoginPassword(v.nonScriptable, login.password); err != nil {
		return err
	}
	if login.remember {
		v.pendingLogin = login
	}
	return nil
}

func setLoginPassword(client *ole.IUnknown, password []uint16) error {
	if len(password) == 0 {
		return fmt.Errorf("Linux 登录密码不能为空")
	}
	// Use a BSTR directly so no immutable Go string contains the password.
	var bstr *int16
	ok, _, _ := windows.NewLazySystemDLL("oleaut32.dll").NewProc("SysReAllocStringLen").Call(uintptr(unsafe.Pointer(&bstr)), uintptr(unsafe.Pointer(&password[0])), uintptr(len(password)))
	if ok == 0 {
		return fmt.Errorf("分配 RDP 登录凭据失败")
	}
	defer ole.SysFreeString(bstr)
	defer clear(unsafe.Slice((*uint16)(unsafe.Pointer(bstr)), len(password)))
	if err := call(client, 3, uintptr(unsafe.Pointer(bstr))); err != nil {
		return fmt.Errorf("设置 RDP 登录凭据: %w", err)
	}
	return nil
}

func (v *viewer) saveLogin(write func(string, *loginCredential) error) error {
	login := v.pendingLogin
	v.pendingLogin = nil
	if login == nil {
		return nil
	}
	defer login.clear()
	err := write(v.loginTarget, login)
	if err != nil {
		v.loginNotice = err.Error()
	}
	return err
}

func (v *viewer) forgetLogin() {
	if err := deleteLogin(v.loginTarget); err != nil {
		v.fail(err)
		return
	}
	v.pendingLogin.clear()
	v.pendingLogin = nil
	v.loginNotice = "主机保存的登录凭据已忘记"
	v.setStatus("已忘记主机保存的凭据 · 关闭窗口后重新连接；GNOME 专用凭据请在客户机远程桌面设置中修改")
}

func (v *viewer) addLoginMenu() {
	menu, _, _ := user32.NewProc("GetSystemMenu").Call(v.hwnd, 0)
	if menu != 0 {
		user32.NewProc("AppendMenuW").Call(menu, 0x800, 0, 0)
		user32.NewProc("AppendMenuW").Call(menu, 0, forgetLoginCommand, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("忘记 Linux 登录凭据"))))
	}
}
