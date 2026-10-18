//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
)

func (a *application) settings(c *ui.Context) {
	pageHeading(c, "QEMU 设置", "连接本机虚拟化引擎，配置 Windows 运行环境。")
	scrollBody(c, "settings", func() {
		card(c, "引擎位置", "选择包含 qemu-system-x86_64.exe 和 qemu-img.exe 的安装目录。", func() {
			field(c, "QEMU 安装目录", &a.qemuDir)
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				if button(c, "选择文件夹", true) {
					a.busy = true
					paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: "选择 QEMU 安装目录", Directory: true})
					a.busy = false
					if err != nil {
						a.report(err)
					} else if len(paths) > 0 {
						a.qemuDir = paths[0]
					}
				}
				muted(c, "留空时自动从 PATH 查找")
			})
		})
		card(c, "Windows 运行环境", "启动前请确认所选加速方式的系统要求。", func() {
			infoRow(c, "硬件加速", "WHPX 需要启用 Windows Hypervisor Platform，并在 BIOS / UEFI 中开启 CPU 虚拟化。")
			infoRow(c, "软件模拟", "TCG 无需硬件加速，运行速度较慢。可在每台虚拟机的配置中选择。")
			infoRow(c, "桥接网络", "使用已配置的 TAP-Windows 网卡，并在 Windows 网络连接中将其与物理网卡建立网络桥。")
		})
		card(c, "数据存储", "配置与虚拟磁盘保存在当前 Windows 用户目录下。", func() {
			infoRow(c, "数据目录", a.root)
			muted(c, "从列表移除虚拟机时，磁盘和快照会继续保留在此目录。")
		})
	})
	ui.Row(c).FillWidth().Gap(10).Justify(ui.End).Children(func() {
		if action(c, "返回", true, components.ButtonGhost) && a.leavePage() {
			a.page = ""
		}
		if action(c, "保存设置", true, components.ButtonPrimary) {
			cfg := a.config
			cfg.QEMUDir = strings.TrimSpace(a.qemuDir)
			a.run("保存设置", func() error { return vm.Save(filepath.Join(a.root, "library.yaml"), cfg) }, func() {
				a.config = cfg
				a.qemuDir = cfg.QEMUDir
				a.page = ""
			})
		}
	})
}
