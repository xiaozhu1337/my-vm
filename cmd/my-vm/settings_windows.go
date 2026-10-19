//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/hcs"
)

func (a *application) settings(c *ui.Context) {
	pageHeading(c, "Hyper-V 设置", "使用 Windows 原生硬件虚拟化与本地控制台。")
	scrollBody(c, "settings", func() {
		card(c, "Windows 运行环境", "需要以管理员身份运行，并启用 Hyper-V 及硬件虚拟化。", func() {
			infoRow(c, "引擎", "Hyper-V Host Compute Service / HCS")
			infoRow(c, "客户机", "第二代 UEFI，x64；建议 Windows 10/11 或受支持的 Linux 发行版。")
			infoRow(c, "控制台", "基础控制台显示固件和安装过程；增强会话提供桌面分辨率与剪贴板重定向。")
		})
		card(c, "虚拟交换机", "NAT 使用系统 Default Switch；桥接需先在 Hyper-V 中创建外部交换机。", func() {
			if button(c, "刷新交换机", !a.diskBusy("host-networks")) {
				var networks []hcs.Network
				a.run("host-networks", "读取 Hyper-V 交换机", func() error { var err error; networks, err = hcs.Networks(); return err }, func() { a.networks = networks })
			}
			for _, n := range a.networks {
				infoRow(c, n.Name, n.Type+" / "+n.ID)
			}
		})
		card(c, "数据存储", "HCS 与旧 QEMU 磁盘格式不同。", func() {
			infoRow(c, "数据目录", a.root)
			muted(c, "新虚拟机使用 VHDX。旧 qcow2 文件保留原位，旧虚拟机只供查看与移除；不会自动转换或覆盖。")
			muted(c, "离线快照复制整个磁盘文件，请预留与磁盘已用空间相当的额外空间。")
		})
	})
	ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
		if action(c, "返回", true, components.ButtonGhost) && a.leavePage() {
			a.page = ""
		}
	})
}
