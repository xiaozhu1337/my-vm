//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

func gpuPresent(root string) bool {
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "class_id"))
		if err != nil {
			continue
		}
		id := strings.Trim(strings.TrimSpace(string(data)), "{}")
		if id == "dde9cbc0-5060-4436-9448-ea1254a5d177" || id == "6e382d18-3336-4f4b-acc4-2b7703d4df4a" {
			return true
		}
	}
	return false
}

func gpuSupported() bool {
	data, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(data)) == "7.3.0-9-generic" && gpuPresent("/sys/bus/vmbus/devices")
}

func prepareGPU(media string) error {
	if !gpuSupported() {
		fmt.Println("GPU-PV 未安装：未检测到共享 GPU，或当前内核不是已验证的 7.3.0-9-generic。其余增强能力继续安装。")
		return nil
	}
	root := filepath.Join(media, "gpu")
	var manifest struct {
		Kernel, Version, Driver string
		HostINF                 string `yaml:"host_inf"`
		HostINFSHA256           string `yaml:"host_inf_sha256"`
		Files                   []struct{ Name, SHA256 string }
	}
	source, err := os.Open(filepath.Join(root, "manifest.yaml"))
	if err != nil {
		return err
	}
	defer source.Close()
	decoder := yaml.NewDecoder(io.LimitReader(source, 1024*1024))
	decoder.KnownFields(true)
	if err = decoder.Decode(&manifest); err != nil {
		return err
	}
	if manifest.Kernel != "7.3.0-9-generic" || manifest.Version != "6.18-d504d40" || len(manifest.Files) != 34 || filepath.Base(manifest.Driver) != manifest.Driver || manifest.Driver == "." || manifest.Driver == "" {
		return errors.New("GPU 离线载荷不完整或版本未验证")
	}
	for _, file := range manifest.Files {
		if filepath.IsAbs(file.Name) || filepath.Clean(file.Name) != file.Name || strings.HasPrefix(file.Name, "../") {
			return errors.New("GPU 离线载荷路径无效")
		}
		if err = checkDigest(filepath.Join(root, file.Name), file.SHA256); err != nil {
			return err
		}
	}
	module := "/usr/src/dxgkrnl-6.18-d504d40"
	for _, file := range manifest.Files {
		destination := ""
		if strings.HasPrefix(file.Name, "source/") {
			destination = filepath.Join(module, strings.TrimPrefix(file.Name, "source/"))
		}
		if strings.HasPrefix(file.Name, "lib/") || strings.HasPrefix(file.Name, "drivers/") {
			destination = filepath.Join("/usr/lib/wsl", file.Name)
		}
		if destination == "" {
			continue
		}
		if _, err = os.Lstat(destination); err == nil {
			if err = checkDigest(destination, file.SHA256); err != nil {
				return fmt.Errorf("已有 GPU 文件与离线盘不同，未覆盖: %w", err)
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		input, openErr := os.Open(filepath.Join(root, file.Name))
		if openErr != nil {
			return openErr
		}
		output, openErr := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if openErr != nil {
			input.Close()
			return openErr
		}
		_, copyErr := io.Copy(output, input)
		input.Close()
		closeErr := output.Close()
		if err = errors.Join(copyErr, closeErr); err != nil {
			os.Remove(destination)
			return err
		}
		if filepath.Base(destination) == "nvidia-smi" {
			if err = os.Chmod(destination, 0755); err != nil {
				return err
			}
		}
	}
	status := exec.Command("dkms", "status", "-m", "dxgkrnl", "-v", manifest.Version)
	data, err := status.Output()
	if err != nil {
		return err
	}
	steps := []string{"build", "install"}
	if len(strings.TrimSpace(string(data))) == 0 {
		steps = append([]string{"add"}, steps...)
	}
	for _, step := range steps {
		if err = guestCommand("dkms", step, "-m", "dxgkrnl", "-v", manifest.Version, "-k", manifest.Kernel); err != nil {
			return err
		}
	}
	if err = guestCommand("modprobe", "dxgkrnl"); err != nil {
		return err
	}
	if _, err = os.Stat("/dev/dxg"); err != nil {
		return err
	}
	if err = os.WriteFile("/etc/modules-load.d/my-vm-gpu.conf", []byte("dxgkrnl\n"), 0644); err != nil {
		return err
	}
	cmd := exec.Command("python3", filepath.Join(root, "verify.py"))
	libraryPath := "/usr/lib/wsl/lib\n/usr/lib/wsl/drivers/" + manifest.Driver + "\n"
	if err = os.WriteFile("/etc/ld.so.conf.d/my-vm-gpu.conf", []byte(libraryPath), 0644); err != nil {
		return err
	}
	if err = guestCommand("ldconfig"); err != nil {
		return err
	}
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/usr/lib/wsl/lib:/usr/lib/wsl/drivers/"+manifest.Driver)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("GPU 模块已加载，CUDA 计算自检未通过: %w", err)
	}
	fmt.Println("GPU-PV 已安装，CUDA 三轮计算自检通过。")
	return nil
}
