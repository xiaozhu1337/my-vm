//go:build windows

package main

import (
	"errors"
	"fmt"
	"my-vm/internal/hostmedia"
	"my-vm/internal/vm"
	"os"
	"path/filepath"
)

func enhancementDisc(machine vm.Machine) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	platform := "windows"
	if machine.IsLinux() {
		platform = "linux"
	}
	target := filepath.Join(filepath.Dir(executable), "my-vm-guest-tools-"+platform+".iso")
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("增强光盘不可用，请先运行构建脚本: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("增强光盘不是普通文件")
	}
	return target, nil
}

func (a *application) prepareEnhancementDisc(machine vm.Machine) (string, error) {
	source, err := enhancementDisc(machine)
	if err != nil || !machine.GPUShared {
		return source, err
	}
	platform := "windows"
	if machine.IsLinux() {
		platform = "linux"
	}
	return hostmedia.Prepare(source, platform, filepath.Dir(vm.DiskPath(a.root, machine)))
}

func (a *application) installEnhancements(machine vm.Machine) {
	if a.diskBusy(machine.ID) {
		return
	}
	var target string
	running := a.manager.State(machine.ID) != "已关机"
	a.runConfig(machine.ID, "准备并插入增强光盘「"+machine.Name+"」", func() error {
		var err error
		target, err = a.prepareEnhancementDisc(machine)
		if err != nil {
			return err
		}
		if running {
			return a.manager.MountGuestMedia(machine.ID, target)
		}
		return nil
	}, func(cfg *vm.Config) error {
		for index := range cfg.Machines {
			if cfg.Machines[index].ID == machine.ID {
				cfg.Machines[index].DriverISO = target
				return nil
			}
		}
		return errors.New("虚拟机不在当前配置中")
	}, func() {
		a.message = "Windows 增强光盘已插入，请在光盘中双击 install.exe。"
		if machine.IsLinux() {
			a.message = "Linux 增强光盘已插入，请在桌面终端运行光盘内的 install.py。"
		}
		if !running {
			a.message = "已配置增强光盘，启动虚拟机后运行光盘中的安装程序。"
		}
	}, func() {
		if !machine.GPUShared || target == "" {
			return
		}
		if a.preparedMedia == nil {
			a.preparedMedia = map[string]bool{}
		}
		// Only paths returned by Prepare in this process are owned by this collector.
		a.preparedMedia[target] = true
	})
}

func (a *application) cleanupGuestMedia() {
	if a.busy || len(a.configurationSaves) > 0 || len(a.operations) > 0 || len(a.diskJobs) > 0 || len(a.externalJobs) > 0 {
		return
	}
	for path := range a.preparedMedia {
		used := a.page == "edit" && (vm.SameMediaFile(a.draft.ISO, path) || vm.SameMediaFile(a.draft.DriverISO, path))
		for _, machine := range a.config.Machines {
			used = used || vm.SameMediaFile(machine.ISO, path) || vm.SameMediaFile(machine.DriverISO, path)
		}
		if used || a.manager != nil && a.manager.GuestMediaInUse(path) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			if a.logger != nil {
				_, _ = fmt.Fprintf(a.logger, "清理未使用的增强光盘 %s: %v\n", path, err)
			}
			continue
		}
		delete(a.preparedMedia, path)
	}
}
