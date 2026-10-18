//go:build windows

package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
)

func (a *application) details(c *ui.Context) {
	m, ok := a.machine()
	if !ok {
		ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(20).Children(func() {
			components.Text(c, "从一台虚拟机开始").FontSize(30).FontWeight(600)
			muted(c, "选择安装镜像，分配资源，在独立的环境中工作。")
			ui.Row(c).Gap(14).Children(func() {
				components.Badge(c, "WHPX 硬件加速", components.BadgeOptions{Tone: components.BadgeAccent})
				components.Badge(c, "按需分配磁盘")
				components.Badge(c, "磁盘快照")
			})
			if action(c, "创建第一台虚拟机", true, components.ButtonPrimary) {
				a.edit(vm.NewMachine(), true)
			}
			muted(c, "首次使用可先到左下角设置 QEMU 安装目录。")
		})
		return
	}
	state := a.manager.State(m.ID)
	off := state == "已关机"
	ui.Row(c).FillWidth().Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
			muted(c, "虚拟机库 / 工作空间")
			components.Text(c, m.Name).FontSize(30).FontWeight(600).SingleLine().FillWidth()
		})
		stateBadge(c, state)
	})
	ui.Row(c).FillWidth().Gap(14).AlignItems(ui.Center).Children(func() {
		tabs := components.Tabs(c, &a.detailTab, components.TabsUnderline, "概览", "磁盘快照")
		if tabs.List.Changed() && a.detailTab == 1 && off {
			a.refreshSnapshots(m)
		}
		ui.Spacer(c)
		if button(c, "编辑配置", off) {
			a.edit(m, false)
		}
	})
	scrollBody(c, fmt.Sprintf("machine-%s-%d", m.ID, a.detailTab), func() {
		if a.detailTab == 1 {
			a.snapshotView(c, m, off)
			return
		}
		a.powerCard(c, m, state)
		ui.Row(c).FillWidth().Gap(14).Children(func() {
			metric(c, "处理器", fmt.Sprintf("%d 核", m.CPUs), "虚拟 CPU")
			metric(c, "内存", fmt.Sprintf("%g GiB", float64(m.MemoryMiB)/1024), fmt.Sprintf("%d MiB 已分配", m.MemoryMiB))
			metric(c, "系统磁盘", fmt.Sprintf("%d GiB", m.DiskGiB), "qcow2 / 按需占用")
		})
		card(c, "设备与连接", "启动前可在编辑配置中调整设备。", func() {
			infoRow(c, "加速方式", strings.ToUpper(m.Accelerator))
			network := "NAT / 共享主机网络"
			if m.Network == "bridge" {
				network = "桥接 / " + m.TAP
			}
			infoRow(c, "网络", network)
			iso := m.ISO
			if iso == "" {
				iso = "未挂载 / 从硬盘启动"
			}
			infoRow(c, "安装光盘", iso)
			display := "标准 VGA / 兼容显示"
			if m.Display == "virtio" {
				display = "virtio / 高清显示（需客户机驱动与分辨率服务）"
			}
			infoRow(c, "显示设备", display)
			drivers := m.DriverISO
			if drivers == "" {
				drivers = "未挂载"
			}
			infoRow(c, "驱动光盘", drivers)
			infoRow(c, "磁盘位置", vm.DiskPath(a.root, m))
		})
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			if components.Button(c, "更多操作", components.ButtonGhost).Clicked() {
				a.showAdvanced = !a.showAdvanced
			}
			if a.showAdvanced {
				card(c, "谨慎操作", "强制断电可能丢失未保存的数据；从列表移除会保留磁盘和快照。", func() {
					ui.Row(c).Gap(12).Children(func() {
						ui.Row(c).TextColor(c.Theme().Danger).Children(func() {
							if button(c, "强制断电", !off) && a.confirm("强制断电", "关闭「"+m.Name+"」？", "未保存的数据可能丢失，建议优先使用正常关机。") {
								a.run("强制断电", func() error { return a.manager.Control(m.ID, "kill") }, nil)
							}
						})
						if button(c, "从列表移除", off) {
							a.remove(m)
						}
					})
				})
			}
		})
	})
}

func (a *application) powerCard(c *ui.Context, m vm.Machine, state string) {
	off, paused := state == "已关机", state == "已暂停"
	title, description := "准备就绪", "启动虚拟机后，客户机画面将在独立窗口中打开。"
	if !off {
		title, description = "虚拟机正在运行", "打开客户机画面，继续你的工作。"
	}
	if paused {
		title, description = "虚拟机已暂停", "继续运行后，客户机才能响应操作与正常关机请求。"
	}
	ui.Column(c).FillWidth().Gap(20).Padding(24).Radius(12).
		Background(c.Theme().Surface.Mix(c.Theme().Accent, .06)).Border(1, c.Theme().Accent.Alpha(.18)).Children(func() {
		ui.Column(c).FillWidth().Gap(7).Children(func() {
			components.Text(c, title).FontSize(22).FontWeight(600)
			muted(c, description)
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label := "启动虚拟机"
			if paused {
				label = "继续运行"
			} else if !off {
				label = "打开画面"
			}
			if action(c, label, true, components.ButtonPrimary) {
				switch {
				case off:
					dir := a.config.QEMUDir
					a.resetSnapshots()
					a.run("启动虚拟机", func() error { return a.manager.Start(dir, m) }, nil)
				case paused:
					a.run("继续运行", func() error { return a.manager.Control(m.ID, "cont") }, nil)
				default:
					a.run("打开客户机画面", func() error { return a.manager.ShowDisplay(m.ID) }, nil)
				}
			}
			if !off {
				if paused {
					if button(c, "打开画面", true) {
						a.run("打开客户机画面", func() error { return a.manager.ShowDisplay(m.ID) }, nil)
					}
				} else if button(c, "暂停", true) {
					a.run("暂停虚拟机", func() error { return a.manager.Control(m.ID, "stop") }, nil)
				}
				if button(c, "正常关机", !paused) {
					a.run("发送关机请求", func() error { return a.manager.Control(m.ID, "system_powerdown") }, func() {
						a.message = "已发送关机请求，等待客户机退出；无响应时可在“更多操作”中强制断电。"
					})
				}
			}
		})
	})
}

func metric(c *ui.Context, label, value, description string) {
	ui.Column(c).Key(label).Grow(1).Basis(0).MinWidth(0).Gap(10).Padding(20).
		Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(12).Children(func() {
		muted(c, label)
		components.Text(c, value).FontSize(27).FontWeight(600)
		muted(c, description)
	})
}

func infoRow(c *ui.Context, label, value string) {
	ui.Row(c).Key(label).FillWidth().Gap(16).AlignItems(ui.Start).Children(func() {
		components.Text(c, label).Width(86).Shrink(0).TextColor(c.Theme().TextMuted)
		components.Text(c, value).Grow(1).MinWidth(0).Selectable()
	})
}
