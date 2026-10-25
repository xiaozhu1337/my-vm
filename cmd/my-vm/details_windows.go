//go:build windows

package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	vm "my-vm/library"
)

func (a *application) details(c *ui.Context) {
	m, ok := a.machine()
	if !ok {
		ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(20).Children(func() {
			components.Text(c, "从一台虚拟机开始").FontSize(30).FontWeight(600)
			muted(c, "选择安装镜像，分配资源，在独立的环境中工作。")
			ui.Row(c).Gap(14).Children(func() {
				components.Badge(c, "Hyper-V 硬件虚拟化", components.BadgeOptions{Tone: components.BadgeAccent})
				components.Badge(c, "按需分配磁盘")
				components.Badge(c, "磁盘快照")
			})
			if action(c, "创建第一台虚拟机", true, components.ButtonPrimary) {
				a.edit(vm.NewMachine(), true)
			}
			muted(c, "启用 Hyper-V，并以管理员身份运行。")
		})
		return
	}
	state := a.manager.State(m.ID)
	memory := m.MemoryMiB
	if actual := a.manager.Memory(m.ID); actual > 0 {
		memory = actual
	}
	ui.Row(c).FillWidth().Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
			muted(c, "虚拟机库 / 工作空间")
			components.Text(c, m.Name).FontSize(30).FontWeight(600).SingleLine().FillWidth().
				ContextMenu(func(menu *ui.Menu) { a.machineMenu(menu, m, state) })
		})
		stateBadge(c, state)
	})
	tabs := components.Tabs(c, &a.detailTab, components.TabsUnderline, "概览", "磁盘快照")
	if tabs.List.Changed() && a.detailTab == 1 && !a.diskBusy(m.ID) {
		a.refreshSnapshots(m)
	}
	scrollBody(c, fmt.Sprintf("machine-%s-%d", m.ID, a.detailTab), func() {
		if a.detailTab == 1 {
			a.snapshotView(c, m, state == "已关机")
			return
		}
		ui.Row(c).FillWidth().Gap(14).Children(func() {
			metric(c, "处理器", fmt.Sprintf("%d 核", m.CPUs), "虚拟 CPU")
			metric(c, "内存", fmt.Sprintf("%g GiB", float64(memory)/1024), fmt.Sprintf("%d MiB 已分配", memory))
			metric(c, "系统磁盘", fmt.Sprintf("%d GiB", m.DiskGiB), "VHDX / 按需占用")
		})
		card(c, "设备与连接", "在虚拟机右键菜单选择“设备管理器”查看硬件和增删设备。", func() {
			infoRow(c, "客户机系统", m.GuestName())
			infoRow(c, "虚拟化引擎", "Hyper-V HCS")
			network := "NAT / 共享主机网络"
			if m.Network == "none" {
				network = "已断开 / 不创建网络适配器"
			} else if m.Network == "bridge" {
				network = "桥接 / " + m.SwitchID
			}
			infoRow(c, "网络", network)
			if len(m.NetworkAdapters) > 0 {
				infoRow(c, "附加网卡", fmt.Sprintf("%d 张", len(m.NetworkAdapters)))
			}
			if len(m.COMPorts) > 0 {
				infoRow(c, "串口", fmt.Sprintf("%d 个 COM 端口", len(m.COMPorts)))
			}
			iso := m.ISO
			if iso == "" {
				iso = "未挂载 / 从硬盘启动"
			}
			infoRow(c, "安装光盘", iso)
			if m.DriverISO != "" {
				infoRow(c, "增强光盘", m.DriverISO)
			}
			muted(c, "从右键菜单安装增强能力，插入光盘后在客户机桌面运行安装程序。")
			infoRow(c, "显示设备", "Hyper-V 基础控制台 / RDP 增强会话")
			for _, s := range m.Shares {
				mode := "只读"
				if !s.ReadOnly {
					mode = "可写"
				}
				infoRow(c, s.Name, s.Path+" / "+mode)
			}
			if m.Backend != "hcs" {
				muted(c, "旧 QEMU 虚拟机：磁盘已保留，请新建 HCS 虚拟机。")
			}
			infoRow(c, "磁盘位置", vm.DiskPath(a.root, m))
		})
	})
}

func (a *application) machineMenu(menu *ui.Menu, m vm.Machine, state string) {
	blocked := a.busy || a.loadError != nil || a.diskBusy(m.ID) || a.diskBusy("host-network-change")
	editing := a.page == "edit" && a.draft.ID == m.ID
	off, paused, hcs := state == "已关机", state == "已暂停", m.Backend == "hcs"
	menu.Item(m.Name).Disabled(true)
	menu.Separator()
	if menu.Item("启动虚拟机").Disabled(blocked || !off || !hcs).Chosen() {
		if a.selected == m.ID {
			a.resetSnapshots()
		}
		a.run(m.ID, "启动虚拟机「"+m.Name+"」", func() error {
			if err := a.action(vm.Request{Action: "start", VM: m.ID}); err != nil {
				return err
			}
			return a.showDisplay(m.ID, false, true)
		}, nil)
	}
	if menu.Item("打开画面").Disabled(blocked || off || !hcs).Chosen() {
		a.run(m.ID, "打开客户机画面「"+m.Name+"」", func() error { return a.showDisplay(m.ID, false, true) }, nil)
	}
	pauseLabel, pauseCommand := "暂停", "stop"
	if paused {
		pauseLabel, pauseCommand = "继续运行", "cont"
	}
	if menu.Item(pauseLabel).Disabled(blocked || off || !hcs).Chosen() {
		a.run(m.ID, pauseLabel+"「"+m.Name+"」", func() error {
			return a.action(vm.Request{Action: map[string]string{"stop": "pause", "cont": "resume"}[pauseCommand], VM: m.ID})
		}, nil)
	}
	if menu.Item("正常关机").Disabled(blocked || off || paused || !hcs).Chosen() {
		a.run(m.ID, "发送关机请求「"+m.Name+"」", func() error { return a.action(vm.Request{Action: "stop", VM: m.ID}) }, func() {
			a.message = "已发送关机请求，等待客户机退出；无响应时可在虚拟机右键菜单中强制断电。"
		})
	}
	menu.Separator()
	if menu.Item("设备管理器").Disabled(blocked || !hcs).Chosen() && a.leavePage() {
		a.edit(m, false)
		a.deviceOnly = true
	}
	if menu.Item("编辑配置").Disabled(blocked || !off).Chosen() && a.leavePage() {
		a.edit(m, false)
	}
	if menu.Item("复制虚拟机").Disabled(blocked || !off || !hcs).Chosen() {
		a.beginClone(m)
	}
	if menu.Item("安装增强能力").Disabled(blocked || editing || paused || !hcs).Chosen() {
		a.installEnhancements(m)
	}
	if menu.Item("管理快照").Disabled(a.busy || a.loadError != nil || !hcs).Chosen() && a.selectMachine(m, "", 1) {
		if !a.snapshotsLoaded && !a.diskBusy(m.ID) {
			a.refreshSnapshots(m)
		}
	}
	if menu.Item("调整运行内存").Disabled(blocked || state != "运行中" || !m.DynamicMemory || !hcs).Chosen() && a.selectMachine(m, "memory", 0) {
		a.runtimeMemoryID = ""
	}
	menu.Separator()
	if menu.Item("基础控制台（安装与维护）").Disabled(blocked || off || !hcs).Chosen() {
		a.run(m.ID, "打开基础控制台「"+m.Name+"」", func() error { return a.showDisplay(m.ID, false, false) }, nil)
	}
	if menu.Item("强制断电").Disabled(blocked || off || !hcs).Chosen() && a.confirm("强制断电", "关闭「"+m.Name+"」？", "未保存的数据可能丢失，建议优先使用正常关机。") {
		a.run(m.ID, "强制断电「"+m.Name+"」", func() error { return a.action(vm.Request{Action: "kill", VM: m.ID}) }, nil)
	}
	if menu.Item("从列表移除").Disabled(blocked || editing || !off).Chosen() {
		a.remove(m)
	}
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
