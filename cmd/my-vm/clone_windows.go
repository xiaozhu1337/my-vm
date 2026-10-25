//go:build windows

package main

import (
	"strings"

	vm "my-vm/library"
)

func (a *application) beginClone(source vm.Machine) {
	if a.diskBusy(source.ID) || a.manager.State(source.ID) != "已关机" || !a.leavePage() {
		return
	}
	a.edit(vm.NewClone(source, a.config.Machines), true)
	a.cloneSource = &source
}

func (a *application) saveClone(source, target vm.Machine) {
	if a.diskBusy(source.ID) {
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
		return a.action(vm.Request{Action: "clone", VM: source.ID, Machine: &target})
	}, func() { a.finishClone(target) })
	if a.diskJobs[source.ID] != nil {
		a.page = ""
		a.cloneSource = nil
	}
}

func (a *application) finishClone(target vm.Machine) {
	a.selected = target.ID
	a.resetSnapshots()
}
