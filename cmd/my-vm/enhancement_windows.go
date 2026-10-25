//go:build windows

package main

import (
	vm "my-vm/library"
)

var enhancementDisc = vm.EnhancementDisc

func (a *application) installEnhancements(machine vm.Machine) {
	if a.diskBusy(machine.ID) {
		return
	}
	running := a.manager.State(machine.ID) != "已关机"
	a.run(machine.ID, "准备并插入增强光盘「"+machine.Name+"」", func() error {
		return a.action(vm.Request{Action: "enhancements", VM: machine.ID})
	}, func() {
		a.message = "Windows 增强光盘已插入，请在光盘中双击 install.exe。"
		if machine.IsLinux() {
			a.message = "Linux 增强光盘已插入，请在桌面终端运行光盘内的 install.py。"
		}
		if !running {
			a.message = "已配置增强光盘，启动虚拟机后运行光盘中的安装程序。"
		}
	})
}
