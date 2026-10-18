//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
)

func text(c *ui.Context, s string) { components.Text(c, s) }
func button(c *ui.Context, label string, enabled bool) bool {
	clicked := false
	ui.Row(c).Disabled(!enabled).Children(func() { clicked = components.Button(c, label, components.ButtonSecondary).Clicked() })
	return enabled && clicked
}

func (a *application) view(c *ui.Context) {
	ui.Column(c).Fill().Gap(8).Padding(16).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			components.Text(c, "虚拟机库").FontSize(24).Bold()
			ui.Spacer(c)
			if button(c, "新建虚拟机", !a.busy && a.loadError == nil) {
				a.edit(vm.NewMachine(), true)
			}
			if button(c, "QEMU 设置", !a.busy && a.loadError == nil) {
				a.page = "settings"
			}
		})
		ui.Row(c).Fill().Gap(20).Disabled(a.busy || a.loadError != nil).Children(func() {
			a.sidebar(c)
			components.ScrollView(c, components.ScrollViewOptions{}).Fill().Children(func() {
				ui.Column(c).FillWidth().Gap(14).Padding(8).Children(func() {
					switch a.page {
					case "settings":
						a.settings(c)
					case "edit":
						a.editor(c)
					default:
						a.details(c)
					}
				})
			})
		})
		ui.Column(c).FillWidth().Padding(10).Radius(6).Background(c.Theme().Surface).Children(func() {
			if a.message == "" {
				text(c, "就绪 · Windows / QEMU · 默认使用 WHPX 硬件加速")
			} else {
				text(c, a.message)
			}
		})
	})
}

func (a *application) sidebar(c *ui.Context) {
	ui.Column(c).Width(250).FillHeight().Gap(10).Padding(10).Background(c.Theme().Surface).Radius(8).Children(func() {
		if components.TextBox(c, &a.query, components.TextBoxOptions{}).Label("搜索虚拟机").Placeholder("搜索虚拟机").Changed() {
			a.listPage = 0
		}
		machines := []vm.Machine{}
		for _, m := range a.config.Machines {
			if strings.Contains(strings.ToLower(m.Name), strings.ToLower(a.query)) {
				machines = append(machines, m)
			}
		}
		pages := max(1, (len(machines)+19)/20)
		a.listPage = min(a.listPage, pages-1)
		components.ScrollView(c, components.ScrollViewOptions{}).Fill().Gap(6).Children(func() {
			for _, m := range machines[a.listPage*20 : min(len(machines), (a.listPage+1)*20)] {
				ui.Column(c).Key(m.ID).FillWidth().Children(func() {
					variant := components.ButtonGhost
					if m.ID == a.selected {
						variant = components.ButtonSecondary
					}
					if components.Button(c, m.Name, variant).FillWidth().Clicked() {
						a.selected = m.ID
						a.page = ""
						a.snapshots = nil
						a.snapshotPage = 0
					}
					components.Text(c, a.manager.State(m.ID)).FontSize(12).TextColor(c.Theme().TextMuted)
				})
			}
			if len(machines) == 0 {
				text(c, "暂无虚拟机")
			}
		})
		ui.Row(c).Gap(6).Children(func() {
			if button(c, "上一页", a.listPage > 0) {
				a.listPage--
			}
			text(c, fmt.Sprintf("%d / %d", a.listPage+1, pages))
			if button(c, "下一页", a.listPage+1 < pages) {
				a.listPage++
			}
		})
	})
}

func (a *application) details(c *ui.Context) {
	m, ok := a.machine()
	if !ok {
		components.Text(c, "创建你的第一台虚拟机").FontSize(24).Bold()
		text(c, "选择安装 ISO，分配 CPU、内存与磁盘，然后启动安装。")
		text(c, "NAT 可直接访问外网；桥接需要预先配置 Windows TAP 网卡。")
		return
	}
	state := a.manager.State(m.ID)
	off := state == "已关机"
	components.Text(c, m.Name).FontSize(28).Bold()
	text(c, "状态："+state)
	ui.Row(c).Gap(6).Children(func() {
		if button(c, "启动", off) {
			dir := a.config.QEMUDir
			a.run("启动虚拟机", func() error { return a.manager.Start(dir, m) }, nil)
		}
		if button(c, "正常关机", !off) {
			a.run("发送关机请求", func() error { return a.manager.Control(m.ID, "system_powerdown") }, func() {
				a.message = "已发送 ACPI 关机请求，等待客户机退出；无响应时可强制断电。"
			})
		}
		label, command := "暂停", "stop"
		if state == "已暂停" {
			label, command = "继续", "cont"
		}
		if button(c, label, !off) {
			a.run(label+"虚拟机", func() error { return a.manager.Control(m.ID, command) }, nil)
		}
		if button(c, "强制断电", !off) && a.confirm("强制关闭 "+m.Name+"？", "未保存的数据可能丢失，建议优先使用正常关机。") {
			a.run("强制断电", func() error { return a.manager.Control(m.ID, "kill") }, nil)
		}
	})
	text(c, fmt.Sprintf("%d 个 CPU · %d MiB 内存 · %d GiB 磁盘 · %s", m.CPUs, m.MemoryMiB, m.DiskGiB, strings.ToUpper(m.Accelerator)))
	network := "NAT（QEMU 用户网络）"
	if m.Network == "bridge" {
		network = "桥接 · " + m.TAP
	}
	text(c, "网络："+network)
	text(c, "磁盘："+vm.DiskPath(a.root, m))
	iso := m.ISO
	if iso == "" {
		iso = "未挂载"
	}
	text(c, "光盘："+iso)
	text(c, "客户机画面显示在独立的 QEMU 窗口。安装完成后可编辑配置卸载 ISO。")
	ui.Row(c).Gap(8).Children(func() {
		if button(c, "编辑配置", off) {
			a.edit(m, false)
		}
		if button(c, "从列表移除", off) {
			a.remove(m)
		}
	})
	ui.Column(c).FillWidth().Gap(10).Padding(16).Border(1, c.Theme().Border).Radius(8).Children(func() {
		components.Text(c, "磁盘快照").FontSize(20).Bold()
		text(c, "关机后可管理 qcow2 内部快照，保存磁盘状态。恢复会覆盖当前磁盘内容。")
		ui.Row(c).Gap(8).Disabled(!off).Children(func() {
			components.TextBox(c, &a.snapshotName, components.TextBoxOptions{}).Label("快照名称").Placeholder("例如 before-update").Width(250)
			if button(c, "创建快照", off) {
				a.snapshot(m, "-c", a.snapshotName)
			}
			if button(c, "刷新列表", off) {
				a.refreshSnapshots(m)
			}
		})
		pages := max(1, (len(a.snapshots)+9)/10)
		a.snapshotPage = min(a.snapshotPage, pages-1)
		for _, snapshot := range a.snapshots[a.snapshotPage*10 : min(len(a.snapshots), (a.snapshotPage+1)*10)] {
			ui.Row(c).Key(snapshot.ID).Gap(8).AlignItems(ui.Center).Children(func() {
				text(c, snapshot.Name)
				text(c, time.Unix(snapshot.DateSeconds, 0).Format("2006-01-02 15:04"))
				ui.Spacer(c)
				if button(c, "恢复", off) {
					a.snapshot(m, "-a", snapshot.Name)
				}
				if button(c, "删除", off) {
					a.snapshot(m, "-d", snapshot.Name)
				}
			})
		}
		if len(a.snapshots) == 0 {
			text(c, "点击“刷新列表”读取已有快照。")
		}
		if pages > 1 {
			ui.Row(c).Gap(8).Children(func() {
				if button(c, "上一组", a.snapshotPage > 0) {
					a.snapshotPage--
				}
				text(c, fmt.Sprintf("%d / %d", a.snapshotPage+1, pages))
				if button(c, "下一组", a.snapshotPage+1 < pages) {
					a.snapshotPage++
				}
			})
		}
	})
}

func (a *application) settings(c *ui.Context) {
	components.Text(c, "QEMU 设置").FontSize(24).Bold()
	text(c, "指定包含 qemu-system-x86_64.exe 和 qemu-img.exe 的目录；留空时使用 PATH。")
	components.TextBox(c, &a.qemuDir, components.TextBoxOptions{}).Label("QEMU 安装目录").FillWidth()
	text(c, "WHPX 需要在 Windows 功能中启用 Windows Hypervisor Platform，并在固件中开启硬件虚拟化。")
	text(c, "桥接使用 TAP-Windows 网卡，需在 Windows 网络连接中将其与物理网卡建立网络桥。")
	text(c, "配置与磁盘目录："+a.root)
	ui.Row(c).Gap(8).Children(func() {
		if button(c, "保存设置", true) {
			cfg := a.config
			cfg.QEMUDir = strings.TrimSpace(a.qemuDir)
			a.run("保存设置", func() error { return vm.Save(filepath.Join(a.root, "library.yaml"), cfg) }, func() { a.config = cfg; a.page = "" })
		}
		if button(c, "返回", true) {
			a.page = ""
		}
	})
}
