//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"go.yaml.in/yaml/v3"
)

type payloadManifest struct {
	OS           string   `yaml:"os"`
	Version      string   `yaml:"version"`
	Architecture string   `yaml:"architecture"`
	IndexSHA256  string   `yaml:"index_sha256"`
	Packages     []string `yaml:"packages"`
	Files        []struct {
		Name   string `yaml:"name"`
		SHA256 string `yaml:"sha256"`
	} `yaml:"files"`
}

func ubuntuVersion(data []byte) (string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	if values["ID"] != "ubuntu" || values["VERSION_ID"] != "26.04" && values["VERSION_ID"] != "26.10" {
		return "", errors.New("离线增强盘仅支持 Ubuntu/Kubuntu 26.04 与 26.10 amd64")
	}
	return values["VERSION_ID"], nil
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

func linuxPayload(media string) (string, payloadManifest, error) {
	var manifest payloadManifest
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", manifest, err
	}
	version, err := ubuntuVersion(data)
	if err != nil {
		return "", manifest, err
	}
	root, err := filepath.Abs(filepath.Join(media, "linux", "ubuntu-"+version))
	if err != nil {
		return "", manifest, err
	}
	source, err := os.Open(filepath.Join(root, "manifest.yaml"))
	if err != nil {
		return "", manifest, fmt.Errorf("光盘缺少 Ubuntu %s 离线依赖: %w", version, err)
	}
	defer source.Close()
	decoder := yaml.NewDecoder(io.LimitReader(source, 1024*1024))
	decoder.KnownFields(true)
	if err = decoder.Decode(&manifest); err != nil {
		return "", manifest, err
	}
	if manifest.OS != "ubuntu" || manifest.Version != version || manifest.Architecture != runtime.GOARCH || runtime.GOARCH != "amd64" {
		return "", manifest, errors.New("离线增强包与客户机发行版或架构不匹配")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > 5000 || len(manifest.Packages) == 0 {
		return "", manifest, errors.New("离线增强包清单不完整")
	}
	packageName := regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	for _, name := range manifest.Packages {
		if !packageName.MatchString(name) {
			return "", manifest, errors.New("离线软件包名称无效")
		}
	}
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		if filepath.Base(file.Name) != file.Name || !strings.HasSuffix(file.Name, ".deb") || seen[file.Name] {
			return "", manifest, errors.New("离线软件包路径无效或重复")
		}
		seen[file.Name] = true
		if err = checkDigest(filepath.Join(root, "pool", file.Name), file.SHA256); err != nil {
			return "", manifest, err
		}
	}
	if err = checkDigest(filepath.Join(root, "Packages"), manifest.IndexSHA256); err != nil {
		return "", manifest, err
	}
	return root, manifest, nil
}

func prepareLinux(media string) error {
	if os.Geteuid() != 0 {
		return errors.New("系统组件准备需要管理员授权")
	}
	root, manifest, err := linuxPayload(media)
	if err != nil {
		return err
	}
	if installDesktop != "gnome" && installDesktop != "kde" {
		return errors.New("未指定受支持的桌面类型")
	}
	packages := []string{}
	for _, name := range manifest.Packages {
		if gpuPackages[name] && !gpuSupported() {
			continue
		}
		if installDesktop == "gnome" && (name == "krdp" || name == "libkf6config-bin" || name == "kwallet6" || name == "xdg-desktop-portal-kde") {
			continue
		}
		if installDesktop == "kde" && (name == "gnome-remote-desktop" || name == "gnome-keyring" || name == "libsecret-tools") {
			continue
		}
		packages = append(packages, name)
	}
	state := "/var/lib/my-vm-guest/apt"
	archives := filepath.Join(state, "archives")
	for _, path := range []string{state, filepath.Join(state, "lists", "partial"), filepath.Join(archives, "partial")} {
		if err = os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	source := "deb [trusted=yes] " + (&url.URL{Scheme: "file", Path: root}).String() + " ./\n"
	sources := filepath.Join(state, "sources.list")
	if err = os.WriteFile(sources, []byte(source), 0644); err != nil {
		return err
	}
	options := []string{"-o", "Dir::Etc::sourcelist=" + sources, "-o", "Dir::Etc::sourceparts=-",
		"-o", "Dir::State::lists=" + filepath.Join(state, "lists"), "-o", "Dir::Cache::archives=" + archives,
		"-o", "APT::Get::List-Cleanup=0", "-o", "APT::Sandbox::User=root"}
	if err = guestCommand("apt-get", append(options, "update")...); err != nil {
		return err
	}
	args := append(append([]string{}, options...), "--no-remove", "--no-install-recommends", "--assume-yes", "install")
	if err = guestCommand("apt-get", append(args, packages...)...); err != nil {
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
