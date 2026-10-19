//go:build windows

package console

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

func TestLoginTargetsAreStableAndSeparate(t *testing.T) {
	first := loginTarget(`\\.\pipe\my-vm-012345.enhanced`)
	if first != loginTarget(`\\.\PIPE\my-vm-012345.enhanced`) || first == loginTarget(`\\.\pipe\my-vm-012346.enhanced`) || !strings.HasPrefix(first, "MyVM/LinuxRDP/") {
		t.Fatal("credential identity is unstable or shared between VMs")
	}
}

func TestChooseLoginReusesSavedCredential(t *testing.T) {
	stored := &loginCredential{username: "cat", password: []uint16{1, 2, 3}}
	prompted := false
	prompt := func() (*loginCredential, error) { prompted = true; return stored, nil }
	got, err := chooseLogin("vm", func(string) (*loginCredential, error) { return stored, nil }, prompt)
	if err != nil || got != stored || prompted {
		t.Fatal("saved credentials prompted again", err)
	}
	got, err = chooseLogin("vm", func(string) (*loginCredential, error) { return nil, nil }, prompt)
	if err != nil || got != stored || !prompted {
		t.Fatal("first connection did not prompt", err)
	}
	failure := errors.New("store unavailable")
	prompted = false
	_, err = chooseLogin("vm", func(string) (*loginCredential, error) { return nil, failure }, prompt)
	if err != failure || prompted {
		t.Fatal("credential-store failure was hidden", err)
	}
}

func TestLoginSavedOnceAfterSuccessAndErased(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			password := []uint16{11, 22, 33}
			v := &viewer{loginTarget: "vm", pendingLogin: &loginCredential{username: "cat", password: password, remember: true}}
			v.sessionEvent(1)
			v.sessionEvent(2)
			if v.pendingLogin == nil || password[0] == 0 {
				t.Fatal("login credentials cleared before authentication")
			}
			calls := 0
			write := func(target string, login *loginCredential) error {
				calls++
				if target != "vm" || login.username != "cat" || len(login.password) != 3 {
					t.Fatal("wrong credentials saved")
				}
				if failure {
					return errors.New("save failed")
				}
				return nil
			}
			if err := v.saveLogin(write); (err != nil) != failure {
				t.Fatal("save failure was hidden", err)
			}
			if v.pendingLogin != nil || password[0] != 0 || password[1] != 0 || password[2] != 0 {
				t.Fatal("password was retained after save")
			}
			if err := v.saveLogin(write); err != nil || calls != 1 {
				t.Fatal("credentials saved more than once", err)
			}
			for _, status := range []string{"已连接", "已连接 · 客户机显示尚未就绪，稍后自动调整分辨率", "已连接 · 关闭控制台后虚拟机继续运行"} {
				v.setStatus(status)
				if strings.Contains(v.status, "save failed") != failure {
					t.Fatal("connection status hid the save result", v.status)
				}
			}
		})
	}
	password := []uint16{99}
	v := &viewer{pendingLogin: &loginCredential{password: password}}
	v.close()
	if password[0] != 0 || v.pendingLogin != nil {
		t.Fatal("closing an unsuccessful session retained a password")
	}
}

func TestCredentialManagerRoundTrip(t *testing.T) {
	// Use a unique test-only credential, never a user's VM or TERMSRV record.
	target := "MyVM/Test/" + t.Name() + "/" + t.TempDir()
	t.Cleanup(func() {
		if err := deleteLogin(target); err != nil {
			t.Error(err)
		}
	})
	if got, err := readLogin(target); err != nil || got != nil {
		t.Fatal("unexpected pre-existing credential", err)
	}
	password := utf16.Encode([]rune("fixture-only 密码"))
	defer clear(password)
	want := &loginCredential{username: "test-user", password: password}
	if err := writeLogin(target, want); err != nil {
		t.Fatal(err)
	}
	got, err := readLogin(target)
	if err != nil || got == nil {
		t.Fatal("read failed", err)
	}
	defer got.clear()
	if got.username != want.username || string(utf16.Decode(got.password)) != string(utf16.Decode(want.password)) || got.remember {
		t.Fatal("stored credential did not round trip")
	}
	if err := deleteLogin(target); err != nil {
		t.Fatal(err)
	}
	if got, err := readLogin(target); err != nil || got != nil {
		t.Fatal("deleted record remained", err)
	}
}

func TestLoginPasswordSetter(t *testing.T) {
	password := utf16.Encode([]rune("fixture 密码"))
	defer clear(password)
	var received []uint16
	var result uint32
	var table [160]uintptr
	table[3] = syscall.NewCallback(func(_ *ole.IUnknown, value *uint16) uintptr {
		received = append(received[:0], unsafe.Slice(value, len(password))...)
		return uintptr(result)
	})
	client := &ole.IUnknown{RawVTable: (*interface{})(unsafe.Pointer(&table))}
	for _, code := range []uint32{0, 0x80070057} {
		result = code
		err := setLoginPassword(client, password)
		if (err != nil) != (code != 0) || string(utf16.Decode(received)) != "fixture 密码" {
			t.Fatal("BSTR setter failed or swallowed COM failure", err)
		}
	}
	if err := setLoginPassword(client, nil); err == nil {
		t.Fatal("empty password reached the control")
	}
	clear(received)
}

func TestNativeCredentialLayout(t *testing.T) {
	var c nativeCredential
	if unsafe.Sizeof(c) != 80 || unsafe.Offsetof(c.Blob) != 40 || unsafe.Offsetof(c.UserName) != 72 {
		t.Fatal("CREDENTIALW layout does not match Windows x64")
	}
}
