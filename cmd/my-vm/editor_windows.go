//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
)

func field(c *ui.Context, label string, value *string) {
	ui.Column(c).Key(label).FillWidth().MinWidth(0).Gap(8).Children(func() {
		components.Text(c, label).FontWeight(500)
		components.TextBox(c, value, components.TextBoxOptions{}).Label(label).FillWidth().MinWidth(0).MinHeight(38)
	})
}

func choice(c *ui.Context, label string, value *string, options []string, labels map[string]string) {
	ui.Column(c).Key(label).FillWidth().Gap(8).Children(func() {
		components.Text(c, label).FontWeight(500)
		selectBox := components.Select(c, value)
		selectBox.Trigger.FillWidth().MinHeight(38).Label(label).Children(func() { text(c, labels[*value]) })
		selectBox.Popup(func(panel ui.Element) {
			for _, option := range options {
				selectBox.Item(option).Children(func() { text(c, labels[option]) })
			}
		})
	})
}

func (a *application) editor(c *ui.Context) {
	title, description, saveLabel := "编辑虚拟机", "按工作需要调整资源与设备。保存后在下次启动时生效。", "保存更改"
	if a.creating {
		title, description, saveLabel = "新建虚拟机", "为你的新环境设置名称、资源和安装镜像。", "创建虚拟机"
	}
	pageHeading(c, title, description)
	scrollBody(c, "editor-"+a.draft.ID, func() {
		card(c, "基本信息", "使用容易辨认的名称来管理不同的工作环境。", func() {
			field(c, "虚拟机名称", &a.draft.Name)
		})
		card(c, "硬件资源", "资源分配影响客户机性能，请为 Windows 主机保留足够的 CPU 与内存。", func() {
			ui.Row(c).FillWidth().Gap(20).Children(func() {
				ui.Column(c).Key("cpu").Grow(1).Basis(0).MinWidth(0).Gap(8).Children(func() {
					text(c, "处理器 / 核")
					components.NumberInput(c, &a.cpu, 1, 256, 1).Label("虚拟 CPU 数量").FillWidth()
				})
				ui.Column(c).Key("memory").Grow(1).Basis(0).MinWidth(0).Gap(8).Children(func() {
					text(c, "内存 / MiB")
					components.NumberInput(c, &a.memory, 256, 1048576, 256).Label("内存 MiB").FillWidth()
				})
				ui.Column(c).Key("disk").Grow(1).Basis(0).MinWidth(0).Gap(8).Disabled(!a.creating).Children(func() {
					text(c, "磁盘 / GiB")
					components.NumberInput(c, &a.disk, 1, 65536, 1).Label("磁盘 GiB").FillWidth()
				})
			})
			choice(c, "加速方式", &a.draft.Accelerator, []string{"whpx", "tcg"}, map[string]string{"whpx": "WHPX 硬件加速（推荐）", "tcg": "TCG 软件模拟（速度较慢）"})
			muted(c, "qcow2 磁盘按实际使用占用空间；已有磁盘暂不支持调整容量。")
		})
		card(c, "安装镜像", "安装系统时挂载 ISO；安装完成后关机，再卸载光盘。", func() {
			field(c, "ISO 文件路径（可留空）", &a.draft.ISO)
			ui.Row(c).Gap(10).Children(func() {
				if button(c, "浏览 ISO 文件", true) {
					a.pickISO()
				}
				if action(c, "卸载光盘", a.draft.ISO != "", components.ButtonGhost) {
					a.draft.ISO = ""
				}
			})
		})
		card(c, "网络连接", "默认 NAT 直接共享主机网络，无需配置额外网卡。", func() {
			choice(c, "网络方式", &a.draft.Network, []string{"nat", "bridge"}, map[string]string{"nat": "NAT / 共享主机网络", "bridge": "桥接 / 已配置的 TAP 网卡"})
			if a.draft.Network == "bridge" {
				field(c, "TAP 网卡名称", &a.draft.TAP)
				muted(c, "填写 Windows 网络连接中 TAP 网卡的准确名称，并预先将其与物理网卡建立网络桥。")
			}
		})
	})
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		muted(c, "配置仅在关机时可修改")
		ui.Spacer(c)
		if action(c, "取消", true, components.ButtonGhost) && a.leavePage() {
			a.page = ""
		}
		if action(c, saveLabel, true, components.ButtonPrimary) {
			a.saveDraft()
		}
	})
}
