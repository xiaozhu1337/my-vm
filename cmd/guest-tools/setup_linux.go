//go:build linux

package main

import (
	"errors"
	"fmt"
	"my-vm/internal/sharing"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const preparationReportedExit = 1

func prepareSystem() error { return prepareLinux(mediaRoot) }

func installationResult(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "增强安装失败:", err)
		return
	}
	fmt.Println("安装完成。专用连接凭据已交由宿主管理器保存；请保持桌面登录，关闭安装用窗口后在主机点击“打开画面”，未知证书仍需确认。")
}
func installUser() error {
	if os.Geteuid() == 0 {
		return errors.New("请以已登录图形桌面的普通用户运行安装器，不要使用 sudo；安装器会自行请求管理员授权")
	}
	if err := installEnhancements(); err != nil {
		return err
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	target := filepath.Join(root, "MyVMGuestTools", "my-vm-guest")
	if err = stopPreviousAgent(target); err != nil {
		return err
	}
	startup := filepath.Join(root, "autostart")
	if err = os.MkdirAll(startup, 0700); err != nil {
		return err
	}
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(target)
	desktop := "[Desktop Entry]\nType=Application\nName=MyVM Guest Enhancements\nExec=systemctl --user start my-vm-guest.service\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	if err = os.WriteFile(filepath.Join(startup, "my-vm-guest.desktop"), []byte(desktop), 0600); err != nil {
		return err
	}
	if err = guestCommand("systemctl", "--user", "import-environment", "DISPLAY", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP"); err != nil {
		return err
	}
	unitDirectory := filepath.Join(root, "systemd", "user")
	if err = os.MkdirAll(unitDirectory, 0700); err != nil {
		return err
	}
	unitPath := filepath.Join(unitDirectory, "my-vm-guest.service")
	unit := "[Unit]\nDescription=MyVM guest enhancements\nAfter=graphical-session.target\nPartOf=graphical-session.target\n[Service]\nExecStart=\"" + quoted + "\" --install=false\nRestart=on-failure\nRestartSec=3\n[Install]\nWantedBy=graphical-session.target\n"
	if err = os.WriteFile(unitPath, []byte(unit), 0600); err != nil {
		return err
	}
	if err = guestCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err = guestCommand("systemctl", "--user", "enable", "my-vm-guest.service"); err != nil {
		return err
	}
	if err = copySelf(target); err != nil {
		return err
	}
	return guestCommand("systemctl", "--user", "restart", "my-vm-guest.service")
}
func stopPreviousAgent(target string) error {
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unit := filepath.Join(config, "systemd", "user", "my-vm-guest.service")
	if _, err = os.Stat(unit); err == nil {
		if err = guestCommand("systemctl", "--user", "stop", "my-vm-guest.service"); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	resolved, err := filepath.EvalSymlinks(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		info, err := os.Stat(filepath.Join("/proc", entry.Name()))
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Getuid() {
			continue
		}
		exe := filepath.Join("/proc", entry.Name(), "exe")
		path, err := os.Readlink(exe)
		if err != nil || strings.TrimSuffix(path, " (deleted)") != resolved {
			continue
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		err = process.Signal(os.Interrupt)
		process.Release()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err = os.Readlink(exe); os.IsNotExist(err) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if _, err = os.Readlink(exe); err == nil {
			return errors.New("旧客户机代理未退出，请退出旧代理后重试安装")
		}
	}
	return nil
}

func mapShares(shares []sharing.Share) error {
	for _, s := range shares {
		if !safeShare(s.Name) {
			return errors.New("主机返回无效的共享名称")
		}
		destination := "dav://" + sharing.ProxyAddress + "/dav/" + s.Name + "/"
		command := exec.Command("gio", "mount", destination)
		command.Env = append(os.Environ(), "LC_ALL=C")
		if data, err := command.CombinedOutput(); err != nil && !strings.Contains(string(data), "already mounted") {
			return fmt.Errorf("挂载 %s: %w: %s", s.Name, err, data)
		}
	}
	return nil
}
