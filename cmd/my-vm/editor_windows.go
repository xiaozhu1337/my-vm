//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

func field(c *ui.Context, label string, value *string) {
	ui.Column(c).Key(label).FillWidth().Gap(4).Children(func() {
		text(c, label)
		components.TextBox(c, value, components.TextBoxOptions{}).Label(label).FillWidth()
	})
}

func choice(c *ui.Context, label string, value *string, options []string, labels map[string]string) {
	ui.Column(c).Key(label).Gap(4).Children(func() {
		text(c, label)
		selectBox := components.Select(c, value)
		selectBox.Trigger.Width(320).Label(label).Children(func() { text(c, labels[*value]) })
		selectBox.Popup(func(panel ui.Element) {
			for _, option := range options {
				selectBox.Item(option).Children(func() { text(c, labels[option]) })
			}
		})
	})
}

func (a *application) editor(c *ui.Context) {
	title := "编辑虚拟机"
	if a.creating {
		title = "新建虚拟机"
	}
	components.Text(c, title).FontSize(24).Bold()
	field(c, "名称", &a.draft.Name)
	ui.Row(c).Gap(20).Children(func() {
		ui.Column(c).Key("cpu").Gap(4).Children(func() { text(c, "虚拟 CPU"); components.NumberInput(c, &a.cpu, 1, 256, 1).Label("虚拟 CPU 数量") })
		ui.Column(c).Key("memory").Gap(4).Children(func() {
			text(c, "内存 / MiB")
			components.NumberInput(c, &a.memory, 256, 1048576, 256).Label("内存 MiB")
		})
		ui.Column(c).Key("disk").Gap(4).Disabled(!a.creating).Children(func() { text(c, "磁盘 / GiB"); components.NumberInput(c, &a.disk, 1, 65536, 1).Label("磁盘 GiB") })
	})
	choice(c, "加速方式", &a.draft.Accelerator, []string{"whpx", "tcg"}, map[string]string{"whpx": "WHPX 硬件加速（推荐）", "tcg": "TCG 软件模拟（速度较慢）"})
	field(c, "安装 ISO（可留空）", &a.draft.ISO)
	ui.Row(c).Gap(8).Children(func() {
		if button(c, "选择 ISO", true) {
			a.pickISO()
		}
		if button(c, "卸载光盘", a.draft.ISO != "") {
			a.draft.ISO = ""
		}
	})
	choice(c, "网络方式", &a.draft.Network, []string{"nat", "bridge"}, map[string]string{"nat": "NAT · 共享主机网络", "bridge": "桥接 · 已配置的 TAP 网卡"})
	if a.draft.Network == "bridge" {
		field(c, "TAP 网卡名称", &a.draft.TAP)
		text(c, "填写 Windows 网络连接中 TAP 网卡的准确名称，并预先将其与物理网卡建立网络桥。")
	}
	text(c, "新磁盘采用 qcow2 按需占用空间；首版不支持调整已有磁盘容量。")
	ui.Row(c).Gap(8).Children(func() {
		if button(c, "保存", true) {
			a.saveDraft()
		}
		if button(c, "取消", true) {
			a.page = ""
		}
	})
}
