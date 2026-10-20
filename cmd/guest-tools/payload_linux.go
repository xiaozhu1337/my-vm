//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func ubuntuVersion(data []byte) (string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	if values["ID"] != "ubuntu" || values["VERSION_ID"] != "26.04" && values["VERSION_ID"] != "26.10" {
		return "", errors.New("增强工具仅支持 Ubuntu/Kubuntu 26.04 与 26.10 amd64")
	}
	return values["VERSION_ID"], nil
}

func validateLinuxGuest() error {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return err
	}
	if _, err = ubuntuVersion(data); err != nil {
		return err
	}
	if runtime.GOARCH != "amd64" {
		return errors.New("增强工具仅支持 amd64 客户机")
	}
	return nil
}

func checkDigest(path, expected string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("增强载荷不是普通文件: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if len(expected) != 64 || hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("增强载荷校验失败: %s", path)
	}
	return nil
}

func linuxPackages(desktop string, gpu bool) ([]string, error) {
	packages := []string{"gvfs-backends", "gvfs-fuse", "libglib2.0-bin", "kmod", "pkexec", "polkitd", "xdg-desktop-portal"}
	switch desktop {
	case "gnome":
		packages = append(packages, "gnome-remote-desktop", "gnome-keyring", "libsecret-tools")
	case "kde":
		packages = append(packages, "krdp", "libkf6config-bin", "kwallet6", "xdg-desktop-portal-kde")
	default:
		return nil, errors.New("未指定受支持的桌面类型")
	}
	if gpu {
		packages = append(packages, "build-essential", "dkms", "libelf-dev", "gcc-15", "linux-headers-7.3.0-9", "linux-headers-7.3.0-9-generic", "python3")
	}
	return packages, nil
}

func installLinuxPackages(packages []string) error {
	options := []string{"-o", "Acquire::Retries=2", "-o", "Acquire::http::Timeout=30", "-o", "Acquire::https::Timeout=30"}
	update := append(append([]string{}, options...), "-o", "APT::Update::Error-Mode=any", "update")
	if err := guestCommand("apt-get", update...); err != nil {
		return fmt.Errorf("更新系统软件源失败，请检查客户机网络和 APT 源后重新安装: %w", err)
	}
	args := append(options, "--no-remove", "--no-install-recommends", "--assume-yes", "install")
	if err := guestCommand("apt-get", append(args, packages...)...); err != nil {
		return fmt.Errorf("下载或安装增强依赖失败，请修复客户机网络或软件包错误后重新安装: %w", err)
	}
	return nil
}

func prepareLinux(media string) error {
	if os.Geteuid() != 0 {
		return errors.New("系统组件准备需要管理员授权")
	}
	if err := validateLinuxGuest(); err != nil {
		return err
	}
	packages, err := linuxPackages(installDesktop, gpuSupported())
	if err != nil {
		return err
	}
	fmt.Println("正在从客户机系统软件源下载并安装增强依赖，请保持网络连接。")
	if err = installLinuxPackages(packages); err != nil {
		return err
	}
	if err = guestCommand("modprobe", "hv_sock"); err != nil {
		return err
	}
	if err = os.WriteFile("/etc/modules-load.d/my-vm-guest.conf", []byte("hv_sock\n"), 0644); err != nil {
		return err
	}
	return prepareGPU(media)
}

func guestCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s 执行失败: %w", name, err)
	}
	return nil
}
