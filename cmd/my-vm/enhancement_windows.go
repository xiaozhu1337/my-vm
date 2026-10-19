//go:build windows

package main

import (
	"errors"
	"fmt"
	"my-vm/internal/vm"
	"os"
	"path/filepath"
)

func enhancementDisc() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	target := filepath.Join(filepath.Dir(executable), "my-vm-guest-tools.iso")
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("增强光盘不可用，请先运行构建脚本: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("增强光盘不是普通文件")
	}
	return target, nil
}

func (a *application) installEnhancements(machine vm.Machine) {
	if a.diskBusy(machine.ID) {
		return
	}
	var target string
	running := a.manager.State(machine.ID) != "已关机"
	a.runConfig(machine.ID, "插入增强光盘「"+machine.Name+"」", func() error {
		var err error
		target, err = enhancementDisc()
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
		a.message = "增强光盘已插入：Windows 双击 install.exe；Ubuntu/Kubuntu 在桌面终端运行光盘内的 install.py。"
		if !running {
			a.message = "已配置增强光盘，启动虚拟机后运行光盘中的安装程序。"
		}
	})
}
