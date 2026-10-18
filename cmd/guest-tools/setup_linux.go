//go:build linux

package main

import (
	"errors"
	"fmt"
	"my-vm/internal/sharing"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func prepareSystem() error {
	return errors.New("Linux 需先加载 hv_sock 内核模块；桌面用户运行 --install")
}
func installUser() error {
	if os.Geteuid() == 0 {
		return errors.New("请使用需要访问共享目录的桌面用户运行 --install")
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	target := filepath.Join(root, "MyVMGuestTools", "my-vm-guest")
	if err = copySelf(target); err != nil {
		return err
	}
	startup := filepath.Join(root, "autostart")
	if err = os.MkdirAll(startup, 0700); err != nil {
		return err
	}
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", `\`+"`", `$`, `\$`, `%`, `%%`).Replace(target)
	desktop := "[Desktop Entry]\nType=Application\nName=MyVM Shared Folders\nExec=\"" + quoted + "\"\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	if err = os.WriteFile(filepath.Join(startup, "my-vm-guest.desktop"), []byte(desktop), 0600); err != nil {
		return err
	}
	cmd := exec.Command(target)
	if err = cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
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
