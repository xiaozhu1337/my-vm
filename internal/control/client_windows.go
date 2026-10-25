//go:build windows

package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func pipeName() (string, string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", "", err
	}
	sid := user.User.Sid.String()
	return `\\.\pipe\my-vm-control-` + sid, "D:P(A;;GA;;;SY)(A;;GA;;;" + sid + ")", nil
}

func dialManager(ctx context.Context, name string) (net.Conn, error) {
	connection, err := winio.DialPipeContext(ctx, name)
	if err != nil {
		return nil, err
	}
	if err = verifyManager(connection); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func verifyManager(connection net.Conn) error {
	file, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("管理器管道不支持身份验证")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &pid); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if !user.User.Sid.Equals(current.User.Sid) || !token.IsElevated() {
		return errors.New("管理器管道服务端必须属于当前用户并具有管理员权限")
	}
	return nil
}

func Client(ctx context.Context) (*http.Client, error) {
	name, _, err := pipeName()
	if err != nil {
		return nil, err
	}
	probe, err := dialManager(ctx, name)
	if err != nil {
		executable, e := os.Executable()
		if e != nil {
			return nil, e
		}
		manager := exec.Command(filepath.Join(filepath.Dir(executable), "my-vm-cli.exe"), "serve")
		manager.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		if e = manager.Start(); e != nil {
			return nil, fmt.Errorf("启动管理器: %w", e)
		}
		manager.Process.Release()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			attempt, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			probe, err = dialManager(attempt, name)
			cancel()
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("无法连接管理器，请以相同用户和管理员权限运行: %w", err)
	}
	probe.Close()
	return ExistingClient()
}

// ExistingClient never starts a process; background subscriptions reconnect independently.
func ExistingClient() (*http.Client, error) {
	name, _, err := pipeName()
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialManager(ctx, name)
	}}
	return &http.Client{Transport: transport}, nil
}
