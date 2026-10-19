//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func waylandDesktop(value string) (string, error) {
	for _, name := range strings.Split(strings.ToLower(value), ":") {
		switch name {
		case "kde", "plasma":
			return "kde", nil
		case "gnome", "ubuntu", "ubuntu-gnome":
			return "gnome", nil
		}
	}
	return "", errors.New("Wayland 增强会话需要当前已登录的 GNOME 或 KDE Plasma 桌面")
}

func kdeAddress(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "kreadconfig6", "--file", "krdpserverrc", "--group", "General", "--key", "ListenPort", "--default", "3389").Output()
	if err != nil {
		return "", fmt.Errorf("读取 KDE 远程桌面端口: %w", err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("KDE 远程桌面端口无效")
	}
	data, err = exec.CommandContext(ctx, "systemctl", "--user", "show", "app-org.kde.krdpserver.service", "--property=MainPID", "--value").Output()
	if err != nil {
		return "", fmt.Errorf("查询 KDE 远程桌面服务: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return "", errors.New("KDE 远程桌面未启动，请先在当前 Plasma 桌面安装增强能力")
	}
	return kdeListener("/proc", pid, port)
}

// Match the listener to KRdp's own descriptors; a configured port alone can name another service.
func kdeListener(proc string, pid, port int) (string, error) {
	directory := filepath.Join(proc, strconv.Itoa(pid))
	info, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return "", errors.New("KDE 远程桌面不属于当前桌面用户")
	}
	executable, err := os.Readlink(filepath.Join(directory, "exe"))
	if err != nil || filepath.Base(executable) != "krdpserver" {
		return "", errors.New("KDE 远程桌面进程身份无效")
	}
	descriptors, err := os.ReadDir(filepath.Join(directory, "fd"))
	if err != nil {
		return "", err
	}
	sockets := map[string]bool{}
	for _, fd := range descriptors {
		link, err := os.Readlink(filepath.Join(directory, "fd", fd.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			sockets[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	data, err := os.ReadFile(filepath.Join(proc, "net", "tcp"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" || !sockets[fields[9]] {
			continue
		}
		address := strings.Split(fields[1], ":")
		if len(address) != 2 || address[0] != "0100007F" && address[0] != "00000000" {
			continue
		}
		number, err := strconv.ParseUint(address[1], 16, 16)
		if err == nil && int(number) == port {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
		}
	}
	return "", errors.New("KDE 配置端口未由当前 KRdp 服务监听，拒绝连接其他服务")
}
