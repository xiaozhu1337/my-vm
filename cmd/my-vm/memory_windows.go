//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
)

func (a *application) memoryPage(c *ui.Context) {
	m, ok := a.machine()
	if !ok {
		a.page = ""
		return
	}
	pageHeading(c, "调整运行内存", m.Name+" / 仅调整本次运行的内存。")
	scrollBody(c, "memory-"+m.ID, func() {
		state := a.manager.State(m.ID)
		if state == "已关机" || !m.DynamicMemory {
			muted(c, "此虚拟机当前无法调整运行内存；请启用内存按需分配并启动虚拟机。")
			return
		}
		a.memoryCard(c, m, state)
	})
	if action(c, "返回", true, components.ButtonGhost) {
		a.selectMachine(m, "", 0)
	}
}

func (a *application) memoryCard(c *ui.Context, m vm.Machine, state string) {
	if state == "已关机" || !m.DynamicMemory {
		return
	}
	if a.runtimeMemoryID != m.ID {
		a.runtimeMemoryID = m.ID
		a.runtimeMemory = float64(a.manager.Memory(m.ID))
	}
	card(c, "运行内存", "立即调整当前启动的内存，关机后恢复配置值；客户机需要支持内存集成。", func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			components.NumberInput(c, &a.runtimeMemory, 256, 1048576, 256).Label("运行内存 MiB").Width(220)
			if button(c, "应用内存", state == "运行中" && !a.diskBusy(m.ID)) {
				size := int(a.runtimeMemory)
				a.run(m.ID, "调整运行内存「"+m.Name+"」", func() error { return a.manager.SetMemory(m.ID, size) }, nil)
			}
		})
	})
}
