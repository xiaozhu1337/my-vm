//go:build windows

package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/vm"
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
	if a.cloneSource != nil {
		title, description, saveLabel = "复制虚拟机", "从「"+a.cloneSource.Name+"」复制已安装的系统，无需重新安装。", "创建独立副本"
	}
	pageHeading(c, title, description)
	scrollBody(c, "editor-"+a.draft.ID, func() {
		card(c, "基本信息", "使用容易辨认的名称来管理不同的工作环境。", func() {
			field(c, "虚拟机名称", &a.draft.Name)
			options, labels := make([]string, 0, len(vm.GuestSystems)), map[string]string{}
			for _, system := range vm.GuestSystems {
				options = append(options, system.ID)
				labels[system.ID] = system.Name
			}
			previous := a.draft.GuestOS
			choice(c, "客户机系统", &a.draft.GuestOS, options, labels)
			if a.draft.GuestOS != previous && !a.draft.IsLinux() {
				a.draft.LinuxDesktop = ""
			}
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
				ui.Column(c).Key("disk").Grow(1).Basis(0).MinWidth(0).Gap(8).Disabled(!a.creating || a.cloneSource != nil).Children(func() {
					text(c, "磁盘 / GiB")
					components.NumberInput(c, &a.disk, 1, 65536, 1).Label("磁盘 GiB").FillWidth()
				})
			})
			infoRow(c, "虚拟化引擎", "Hyper-V HCS / 硬件虚拟化")
			memoryLabel := "内存按需分配：关闭"
			if a.draft.DynamicMemory {
				memoryLabel = "内存按需分配：开启"
			}
			if button(c, memoryLabel, true) {
				a.draft.DynamicMemory = !a.draft.DynamicMemory
			}
			muted(c, "开启后允许 HCS 按需分配，并可在运行中调整内存。客户机需支持 Hyper-V 内存集成。")
			gpuLabel := "GPU 共享：关闭"
			if a.draft.GPUShared {
				gpuLabel = "GPU 共享：开启"
			}
			if button(c, gpuLabel, true) {
				a.draft.GPUShared = !a.draft.GPUShared
			}
			muted(c, "GPU 分区共享需要兼容显卡和客户机驱动；原版 Ubuntu 不含所需的 dxgkrnl 驱动。启用失败将报告错误。")
			muted(c, "VHDX 磁盘按实际使用占用空间；已有磁盘暂不支持调整容量。")
		})
		card(c, "安装镜像", "安装系统时挂载 ISO；安装完成后关机，再卸载光盘。", func() {
			field(c, "ISO 文件路径（可留空）", &a.draft.ISO)
			ui.Row(c).Gap(10).Children(func() {
				if button(c, "浏览 ISO 文件", true) {
					a.pickISO("选择安装光盘", &a.draft.ISO)
				}
				if action(c, "卸载光盘", a.draft.ISO != "", components.ButtonGhost) {
					a.draft.ISO = ""
				}
			})
		})
		card(c, "显示与共享", "基础控制台用于固件和系统安装；增强会话使用本地 RDP 通道提供高清桌面与剪贴板。", func() {
			if a.draft.IsLinux() {
				desktop := a.draft.DesktopMode()
				choice(c, "Linux 桌面会话", &desktop, []string{"wayland", "x11"}, map[string]string{"wayland": "Wayland / GNOME · KDE", "x11": "X11 / xrdp"})
				if desktop != a.draft.DesktopMode() {
					a.draft.LinuxDesktop = desktop
				}
				muted(c, "Wayland 使用 GNOME Remote Desktop 或 KDE KRdp，共享当前桌面、剪贴板并缩放画面；客户机挂载增强盘后运行安装器。")
			} else {
				muted(c, "Windows 10/11 已包含 Hyper-V 显示、存储与网络驱动。增强会话需要客户机启用远程桌面服务。")
			}
			label := "剪贴板共享：关闭"
			if a.draft.Clipboard {
				label = "剪贴板共享：开启"
			}
			if button(c, label, true) {
				a.draft.Clipboard = !a.draft.Clipboard
			}
			muted(c, "增强会话的文字、图片与文件粘贴能力取决于客户机服务和应用；共享文件夹也可独立使用。")
		})
		if a.cloneSource != nil {
			muted(c, "副本保留客户机的账户、主机名和系统身份。首次启动建议保持断网，准备好客户机身份后再联网。")
		}
		a.shareEditor(c)
		card(c, "网络连接", "NAT 使用 Hyper-V 默认交换机；桥接使用预先创建的外部交换机。", func() {
			choice(c, "网络方式", &a.draft.Network, []string{"none", "nat", "bridge"}, map[string]string{"none": "断开网络 / 隔离副本", "nat": "NAT / Hyper-V 默认交换机", "bridge": "桥接 / Hyper-V 外部交换机"})
			field(c, "Hyper-V 交换机 ID（NAT 可留空）", &a.draft.SwitchID)
			muted(c, "可在左下角 Hyper-V 设置中查看交换机名称和 ID。桥接需要已连接物理网卡的外部交换机。")
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
