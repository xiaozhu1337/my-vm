//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
)

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
			if button(c, "应用内存", state == "运行中") {
				size := int(a.runtimeMemory)
				a.run("调整运行内存", func() error { return a.manager.SetMemory(m.ID, size) }, nil)
			}
		})
	})
}
