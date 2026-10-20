//go:build windows

package hostmedia

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/windows"
)

type linuxManifest struct {
	Kernel        string       `yaml:"kernel"`
	Version       string       `yaml:"version"`
	Driver        string       `yaml:"driver,omitempty"`
	HostINF       string       `yaml:"host_inf,omitempty"`
	HostINFSHA256 string       `yaml:"host_inf_sha256,omitempty"`
	Files         []fileRecord `yaml:"files"`
}

func collectLinux(stage *staging, adapters []Adapter) error {
	data, err := os.ReadFile(filepath.Join(stage.root, "gpu", "manifest.yaml"))
	if err != nil {
		return fmt.Errorf("通用增强光盘缺少 dxgkrnl 源码: %w", err)
	}
	var manifest linuxManifest
	if err = yaml.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest.Kernel != "7.3.0-9-generic" || manifest.Version != "6.18-d504d40" || len(manifest.Files) != 20 {
		return errors.New("Linux GPU 通用源码版本或文件数不匹配")
	}
	for _, file := range manifest.Files {
		if !safeRelative(file.Name) || !(strings.HasPrefix(file.Name, "source/") || file.Name == "COPYING" || file.Name == "GPL-2.0" || file.Name == "verify.py") {
			return errors.New("Linux GPU 通用载荷包含非源码文件")
		}
		digest, err := fileDigest(filepath.Join(stage.root, "gpu", filepath.FromSlash(file.Name)))
		if err != nil {
			return err
		}
		if digest != file.SHA256 {
			return fmt.Errorf("Linux GPU 源码校验失败: %s", file.Name)
		}
	}
	var selected *Adapter
	for i := range adapters {
		if strings.HasPrefix(strings.ToLower(adapters[i].HardwareID), `pci\ven_10de`) {
			if selected != nil && selected.Driver != adapters[i].Driver {
				return errors.New("多个 NVIDIA 驱动包，无法安全确定 Linux GPU 驱动")
			}
			selected = &adapters[i]
		}
	}
	if selected == nil {
		return errors.New("Linux GPU 自动准备目前只支持 NVIDIA，未找到匹配宿主显卡")
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return err
	}
	groups := []struct {
		source, dir string
		names       []string
	}{
		{filepath.Join(system, "lxss", "lib"), "lib", []string{"libcuda.so", "libcuda.so.1", "libcuda.so.1.1", "libnvidia-ml.so.1", "nvidia-smi"}},
		{filepath.Join(programFiles, "WSL", "lib"), "lib", []string{"libd3d12.so", "libd3d12core.so", "libdxcore.so"}},
		{selected.directory, "drivers/" + selected.Driver, []string{"libcuda.so.1.1", "libnvidia-ml.so.1", "libnvdxgdmal.so.1", "libnvidia-ptxjitcompiler.so.1", "libnvidia-gpucomp.so", "nvcubins.bin"}},
	}
	for _, group := range groups {
		for _, name := range group.names {
			source := filepath.Join(group.source, name)
			if _, err := os.Stat(source); os.IsNotExist(err) && group.source == filepath.Join(programFiles, "WSL", "lib") {
				source = filepath.Join(system, "lxss", "lib", name)
			}
			record, err := stage.copy(source, "gpu/"+group.dir+"/"+name)
			if err != nil {
				return fmt.Errorf("复制 Linux GPU 宿主文件 %s: %w", source, err)
			}
			record.Name = strings.TrimPrefix(record.Name, "gpu/")
			manifest.Files = append(manifest.Files, record)
		}
	}
	manifest.Driver = selected.Driver
	manifest.HostINF = selected.inf
	manifest.HostINFSHA256, err = fileDigest(selected.inf)
	if err != nil {
		return err
	}
	return stage.writeYAML("gpu/manifest.yaml", manifest)
}
