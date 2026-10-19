//go:build windows

package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/oneo-me/onevra/source/components"
	"my-vm/internal/hcs"
	"my-vm/internal/networkmgr"
)

type hostAdapter struct {
	Name, MAC, Addresses, State string
	MTU                         int
}

func readHostAdapters() ([]hostAdapter, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var adapters []hostAdapter
	for _, adapter := range interfaces {
		addresses, err := adapter.Addrs()
		if err != nil {
			return nil, err
		}
		var values []string
		for _, address := range addresses {
			values = append(values, address.String())
		}
		state := "未连接"
		if adapter.Flags&net.FlagUp != 0 {
			state = "已启用"
		}
		adapters = append(adapters, hostAdapter{Name: adapter.Name, MAC: adapter.HardwareAddr.String(), Addresses: strings.Join(values, ", "), State: state, MTU: adapter.MTU})
	}
	return adapters, nil
}
func (a *application) refreshHostNetworks() {
	var networks []networkmgr.Network
	var records []networkmgr.Record
	var adapters []hostAdapter
	a.run("host-networks", "读取宿主网络", func() error {
		var err error
		networks, records, err = networkmgr.New(a.root).List()
		if err != nil {
			return err
		}
		adapters, err = readHostAdapters()
		return err
	}, func() {
		a.networkDetails, a.managedNetworks, a.hostAdapters = networks, records, adapters
		a.networks = nil
		for _, n := range networks {
			a.networks = append(a.networks, hcs.Network{ID: n.ID, Name: n.Name, Type: n.Type})
		}
	})
}
func (a *application) networkMutationAllowed() bool {
	return !a.busy && !a.closing && a.loadError == nil && len(a.operations) == 0 && len(a.diskJobs) == 0 && len(a.externalJobs) == 0 && len(a.configurationSaves) == 0
}
func (a *application) networkReferences() []string {
	var ids []string
	for _, m := range a.config.Machines {
		if m.Network != "none" && m.SwitchID != "" {
			ids = append(ids, m.SwitchID)
		}
		for _, adapter := range m.NetworkAdapters {
			if adapter.Network != "none" && adapter.SwitchID != "" {
				ids = append(ids, adapter.SwitchID)
			}
		}
	}
	return ids
}
func (a *application) deleteHostNetwork(record networkmgr.Record) {
	if !a.networkMutationAllowed() {
		return
	}
	for _, ref := range a.networkReferences() {
		if strings.EqualFold(strings.Trim(ref, "{}"), record.ID) {
			a.report(fmt.Errorf("有虚拟机仍使用网络「%s」，请先移除对应连接", record.Name))
			return
		}
	}
	if !a.confirm("删除自定义网络", "删除「"+record.Name+"」？", "仅删除此程序登记且没有端点的 NAT 网络；系统默认交换机、WSL 和物理网卡不会被修改。") {
		return
	}
	references := a.networkReferences()
	a.run("host-network-change", "删除自定义网络", func() error { return networkmgr.New(a.root).Delete(record.ID, references) }, a.refreshHostNetworks)
}
func (a *application) settings(c *ui.Context) {
	pageHeading(c, "全局设置", "管理宿主网络、虚拟交换机和存储位置。")
	components.Tabs(c, &a.settingsTab, components.TabsUnderline, "运行环境", "虚拟网络", "主机网卡", "数据存储")
	scrollBody(c, fmt.Sprintf("settings-%d", a.settingsTab), func() {
		switch a.settingsTab {
		case 0:
			card(c, "Windows 运行环境", "需要以管理员身份运行，并启用 Hyper-V 及硬件虚拟化。", func() {
				infoRow(c, "引擎", "Hyper-V Host Compute Service / HCS")
				infoRow(c, "客户机", "第二代 UEFI，x64；Windows 10/11 或受支持的 Linux 发行版。")
				infoRow(c, "控制台", "普通入口先进入基础画面，增强就绪后自动切换；维护入口保持基础画面。")
			})
		case 1:
			a.globalNetworks(c)
		case 2:
			card(c, "主机网络适配器", "宿主网卡只读；VM 桥接引用已有外部交换机，不更改主机地址或断开网络。", func() {
				if button(c, "刷新主机网卡", !a.diskBusy("host-networks")) {
					a.refreshHostNetworks()
				}
				if len(a.hostAdapters) == 0 {
					muted(c, "刷新后查看网卡名称、MAC 地址、状态和 IP 地址。")
				}
				for _, adapter := range a.hostAdapters {
					ui.Column(c).Key(adapter.Name).FillWidth().Gap(7).Padding(10).Children(func() {
						text(c, adapter.Name)
						infoRow(c, "状态", adapter.State)
						infoRow(c, "MAC", adapter.MAC)
						infoRow(c, "IP 地址", adapter.Addresses)
						infoRow(c, "MTU", fmt.Sprint(adapter.MTU))
					})
				}
			})
		case 3:
			card(c, "数据存储", "VHDX 磁盘按实际使用占用空间。", func() {
				infoRow(c, "数据目录", a.root)
				muted(c, "旧 qcow2 文件保留原位，不自动转换或覆盖。离线快照复制整个磁盘文件，请预留足够空间。")
			})
		}
	})
	ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
		if action(c, "返回", true, components.ButtonGhost) && a.leavePage() {
			a.page = ""
		}
	})
}
func (a *application) globalNetworks(c *ui.Context) {
	card(c, "虚拟网络", "Default Switch 自动分配地址；自定义 NAT 没有 DHCP，需在客户机设置静态 IP、网关和 DNS。", func() {
		if button(c, "刷新网络", !a.diskBusy("host-networks")) {
			a.refreshHostNetworks()
		}
		if len(a.networkDetails) == 0 {
			muted(c, "刷新后列出系统和本程序的网络。")
		}
		for _, n := range a.networkDetails {
			ui.Column(c).Key(n.ID).FillWidth().Gap(7).Padding(10).Children(func() {
				text(c, n.Name)
				infoRow(c, "类型 / ID", n.Type+" / "+n.ID)
				if n.Subnet != "" {
					infoRow(c, "网段", n.Subnet)
				}
				owned := false
				for _, record := range a.managedNetworks {
					if strings.EqualFold(record.ID, n.ID) {
						owned = true
						if action(c, "删除网络「"+n.Name+"」", a.networkMutationAllowed(), components.ButtonGhost) {
							a.deleteHostNetwork(record)
						}
						break
					}
				}
				if !owned {
					muted(c, "系统或外部网络，只读；可在虚拟机网卡中选择。")
				}
			})
		}
		for _, record := range a.managedNetworks {
			found := false
			for _, n := range a.networkDetails {
				if strings.EqualFold(n.ID, record.ID) {
					found = true
				}
			}
			if !found {
				ui.Column(c).Key("pending-" + record.ID).FillWidth().Gap(7).Children(func() {
					text(c, "待恢复登记 / "+record.Name)
					if action(c, "清理登记「"+record.Name+"」", a.networkMutationAllowed(), components.ButtonGhost) {
						a.deleteHostNetwork(record)
					}
				})
			}
		}
	})
	card(c, "新建 NAT 网络", "网段不能与宿主网卡或现有虚拟网络重叠。网关为网段首个可用地址，例如 192.168.240.1。", func() {
		field(c, "网络名称", &a.networkName)
		field(c, "IPv4 私有网段", &a.networkCIDR)
		if a.networkCIDR == "" {
			muted(c, "例如 192.168.240.0/24")
		}
		if action(c, "创建 NAT 网络", a.networkMutationAllowed(), components.ButtonPrimary) {
			name, cidr := strings.TrimSpace(a.networkName), strings.TrimSpace(a.networkCIDR)
			a.run("host-network-change", "创建自定义 NAT 网络", func() error { return networkmgr.New(a.root).Create(name, cidr) }, func() { a.networkName = ""; a.refreshHostNetworks() })
		}
	})
}
