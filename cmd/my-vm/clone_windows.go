//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"my-vm/internal/vm"
)

func (a *application) beginClone(source vm.Machine) {
	if a.diskJob != nil || a.manager.State(source.ID) != "已关机" || !a.leavePage() {
		return
	}
	a.edit(vm.NewClone(source, a.config.Machines), true)
	a.cloneSource = &source
}

func (a *application) saveClone(source, target vm.Machine) {
	if a.diskJob != nil {
		return
	}
	for _, existing := range a.config.Machines {
		if strings.EqualFold(existing.Name, target.Name) {
			a.message, a.messageError = "此名称已存在，请为副本选择其他名称。", true
			return
		}
	}
	if !a.confirm("复制虚拟机", "从「"+source.Name+"」创建「"+target.Name+"」？", "将复制系统磁盘和固件状态。客户机的账户、主机名及系统身份也会保留。副本默认断网且不共享主机目录；联网前请准备客户机身份，避免名称、静态 IP 或系统身份冲突。") {
		return
	}
	a.runDisk("复制虚拟机", source, target.ID, func(progress func(int64, int64)) error {
		return a.manager.Clone(source, target, progress)
	}, func() { a.finishClone(target) })
	if a.diskJob != nil {
		a.page = ""
		a.cloneSource = nil
	}
}

func (a *application) finishClone(target vm.Machine) {
	for _, existing := range a.config.Machines {
		if strings.EqualFold(existing.Name, target.Name) {
			target.Name = vm.NewClone(target, a.config.Machines).Name
			a.message = "副本已完成，名称调整为「" + target.Name + "」，避免与新登记的虚拟机重名。"
			break
		}
	}
	cfg := a.config
	cfg.Machines = append(slices.Clone(cfg.Machines), target)
	if err := vm.Save(filepath.Join(a.root, "library.yaml"), cfg); err != nil {
		a.report(fmt.Errorf("配置保存失败，已复制的磁盘保留在 %s: %w", vm.DiskPath(a.root, target), err))
		return
	}
	a.config = cfg
}
