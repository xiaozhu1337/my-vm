//go:build windows

package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"my-vm/internal/guestops"
	"my-vm/internal/sharing"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"golang.org/x/sys/windows"
)

func detectLinuxWayland(runtimeID string, log io.Writer) bool {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(log, "读取 Linux 桌面会话:", err)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// go-winio cannot cancel a pending ConnectEx; bound the probe by its process lifetime.
	command := exec.CommandContext(ctx, executable, "--console", "--detect-desktop", runtimeID)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := command.Output()
	kind := strings.TrimSpace(string(output))
	if err != nil || (kind != "wayland" && kind != "x11") {
		fmt.Fprintln(log, "未能读取 Linux 桌面会话，保留旧 X11 连接方式:", err, ctx.Err())
		return false
	}
	fmt.Fprintln(log, "Linux 增强会话类型:", kind)
	return kind == "wayland"
}

func queryLinuxSession(runtimeID string) (string, error) {
	id, err := guid.FromString(runtimeID)
	if err != nil {
		return "", err
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		connection, err := winio.Dial(ctx, &winio.HvsockAddr{VMID: id, ServiceID: winio.VsockServiceID(guestops.Port)})
		if err != nil {
			return nil, err
		}
		return sharing.SocketConnection(connection), nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	return linuxSessionType(client)
}

func linuxSessionType(client *http.Client) (string, error) {
	data, err := json.Marshal(guestops.Command{Args: []string{"/usr/bin/printenv", "XDG_SESSION_TYPE"}, Timeout: 1})
	if err != nil {
		return "", err
	}
	response, err := client.Post("http://guest/exec", "application/json", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("客户机代理返回 HTTP %d", response.StatusCode)
	}
	var result guestops.Result
	if err = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&result); err != nil {
		return "", err
	}
	if result.ExitCode != 0 || result.Error != "" || result.Truncated {
		return "", fmt.Errorf("客户机未能报告桌面会话类型: %s", result.Error)
	}
	kind := strings.TrimSpace(string(result.Stdout))
	if kind != "x11" && kind != "wayland" {
		return "", fmt.Errorf("不支持的 Linux 桌面会话类型 %q", kind)
	}
	return kind, nil
}
