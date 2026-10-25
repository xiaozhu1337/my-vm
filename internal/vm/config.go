package vm

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"my-vm/internal/sharing"

	"go.yaml.in/yaml/v3"
)

type Machine struct {
	GuestOS         string           `yaml:"guest_os" json:"guest_os"`
	LinuxDesktop    string           `yaml:"linux_desktop,omitempty" json:"linux_desktop,omitempty"`
	DynamicMemory   bool             `yaml:"dynamic_memory" json:"dynamic_memory"`
	GPUShared       bool             `yaml:"gpu_shared" json:"gpu_shared"`
	Backend         string           `yaml:"backend" json:"backend"`
	SwitchID        string           `yaml:"switch_id" json:"switch_id"`
	Clipboard       bool             `yaml:"clipboard" json:"clipboard"`
	Shares          []Share          `yaml:"shares,omitempty" json:"shares,omitempty"`
	ID              string           `yaml:"id" json:"id"`
	Name            string           `yaml:"name" json:"name"`
	CPUs            int              `yaml:"cpus" json:"cpus"`
	MemoryMiB       int              `yaml:"memory_mib" json:"memory_mib"`
	DiskGiB         int              `yaml:"disk_gib" json:"disk_gib"`
	ISO             string           `yaml:"iso" json:"iso"`
	DriverISO       string           `yaml:"driver_iso" json:"driver_iso"`
	Display         string           `yaml:"display" json:"display"`
	Network         string           `yaml:"network" json:"network"`
	TAP             string           `yaml:"tap" json:"tap"`
	NetworkAdapters []NetworkAdapter `yaml:"network_adapters,omitempty" json:"network_adapters,omitempty"`
	COMPorts        []COMPort        `yaml:"com_ports,omitempty" json:"com_ports,omitempty"`
	Accelerator     string           `yaml:"accelerator" json:"accelerator"`
}

// NetworkAdapter describes an additional NIC; Network and SwitchID remain the primary NIC.
type NetworkAdapter struct {
	Network    string `yaml:"network" json:"network"`
	SwitchID   string `yaml:"switch_id" json:"switch_id"`
	MACAddress string `yaml:"mac_address,omitempty" json:"mac_address,omitempty"`
}

type COMPort struct {
	Number int    `yaml:"number" json:"number"`
	Pipe   string `yaml:"pipe" json:"pipe"`
}

// Share is the management configuration; the guest-facing sharing type hides Path.
type Share struct {
	Name     string `yaml:"name" json:"name"`
	Path     string `yaml:"path" json:"path"`
	ReadOnly bool   `yaml:"read_only" json:"read_only"`
}

func (s *Share) UnmarshalYAML(node *yaml.Node) error {
	var value sharing.Share
	if err := node.Decode(&value); err != nil {
		return err
	}
	*s = Share(value)
	return nil
}

func sharedFolders(shares []Share) []sharing.Share {
	result := make([]sharing.Share, len(shares))
	for i, share := range shares {
		result[i] = sharing.Share(share)
	}
	return result
}

type Config struct {
	SideChannelMitigations bool      `yaml:"side_channel_mitigations" json:"side_channel_mitigations"`
	QEMUDir                string    `yaml:"qemu_dir" json:"qemu_dir"`
	Machines               []Machine `yaml:"machines" json:"machines"`
}

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Snapshot names are safe directory names independent of the storage backend.
var validSnapshot = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func NewMachine() Machine {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return Machine{ID: hex.EncodeToString(id[:]), Backend: "hcs", GuestOS: "windows10", DynamicMemory: true, Name: "新虚拟机", CPUs: 2, MemoryMiB: 4096, DiskGiB: 64, Network: "nat", Clipboard: true}
}

func (m Machine) Validate() error {
	_, knownGuest := m.guestSystem()
	switch {
	case !validID.MatchString(m.ID):
		return errors.New("虚拟机 ID 无效")
	case strings.TrimSpace(m.Name) == "" || len([]rune(m.Name)) > 80:
		return errors.New("名称需要 1 到 80 个字符")
	case m.CPUs < 1 || m.CPUs > 256:
		return errors.New("CPU 数量需要在 1 到 256 之间")
	case m.MemoryMiB < 256 || m.MemoryMiB > 1048576:
		return errors.New("内存需要在 256 到 1048576 MiB 之间")
	case m.DiskGiB < 1 || m.DiskGiB > 65536:
		return errors.New("磁盘需要在 1 到 65536 GiB 之间")
	case m.Network != "nat" && m.Network != "bridge" && (m.Network != "none" || m.Backend != "hcs"):
		return errors.New("网络模式必须为 nat、bridge 或 HCS 的 none（断开网络）")
	case !knownGuest:
		return errors.New("客户机系统必须为 windows10、windows11、ubuntu26、windows 或 linux")
	case m.LinuxDesktop != "" && (m.LinuxDesktop != "wayland" && m.LinuxDesktop != "x11" || !m.IsLinux()):
		return errors.New("Linux 桌面模式必须为 wayland 或 x11，且仅适用于 Linux 客户机")
	case m.Backend != "" && m.Backend != "qemu" && m.Backend != "hcs":
		return errors.New("虚拟机后端无效")
	case m.Backend == "hcs" && m.Network == "bridge" && m.SwitchID == "":
		return errors.New("请选择外部 Hyper-V 交换机")
	case m.Backend != "hcs" && m.Network == "bridge" && (strings.TrimSpace(m.TAP) == "" || strings.ContainsAny(m.TAP, ",\r\n")):
		return errors.New("桥接模式需要有效的 TAP 网卡名称，名称不能含逗号或换行")
	case m.Display != "" && m.Display != "std" && m.Display != "virtio":
		return errors.New("显示设备必须为 std 或 virtio")
	case m.Backend != "hcs" && m.Accelerator != "whpx":
		return errors.New("仅支持硬件加速；旧配置请使用 whpx")
	}
	if err := m.validateDevices(); err != nil {
		return err
	}
	return ValidateShares(m.Shares)
}

var validSwitchID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (m Machine) validateDevices() error {
	if len(m.NetworkAdapters) > 7 {
		return errors.New("主网卡和附加网卡合计不能超过 8 张")
	}
	adapters := append([]NetworkAdapter{{Network: m.Network, SwitchID: m.SwitchID}}, m.NetworkAdapters...)
	macs := map[string]bool{}
	for i, adapter := range adapters {
		if adapter.Network != "none" && adapter.Network != "nat" && adapter.Network != "bridge" {
			return fmt.Errorf("网卡 %d 的网络模式必须为 none、nat 或 bridge", i+1)
		}
		switchID := adapter.SwitchID
		if strings.HasPrefix(switchID, "{") && strings.HasSuffix(switchID, "}") {
			switchID = switchID[1 : len(switchID)-1]
		}
		if adapter.SwitchID != "" && !validSwitchID.MatchString(switchID) {
			return fmt.Errorf("网卡 %d 的交换机 ID 必须为 GUID", i+1)
		}
		if (m.Backend == "hcs" || i > 0) && adapter.Network == "bridge" && switchID == "" {
			return fmt.Errorf("网卡 %d 的桥接模式需要指定交换机", i+1)
		}
		if adapter.MACAddress != "" {
			mac, err := net.ParseMAC(adapter.MACAddress)
			if err != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" {
				return fmt.Errorf("网卡 %d 的 MAC 必须为有效的 6 字节单播地址", i+1)
			}
			if macs[mac.String()] {
				return errors.New("网卡 MAC 地址不能重复")
			}
			macs[mac.String()] = true
		}
	}
	numbers, pipes := map[int]bool{}, map[string]bool{}
	for _, port := range m.COMPorts {
		if port.Number != 1 && port.Number != 2 || numbers[port.Number] {
			return errors.New("串口只能为 COM1 或 COM2，且编号不能重复")
		}
		const prefix = `\\.\pipe\`
		if !strings.HasPrefix(strings.ToLower(port.Pipe), prefix) {
			return errors.New(`串口必须使用本机绝对命名管道 \\.\pipe\...`)
		}
		name := strings.ToLower(strings.TrimPrefix(strings.ToLower(port.Pipe), prefix))
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\\/\x00\r\n:*?\"<>|") || strings.HasPrefix(name, "my-vm-") {
			return errors.New("串口管道名称无效或占用了 my-vm 控制台保留名称")
		}
		if pipes[name] {
			return errors.New("串口管道不能重复")
		}
		numbers[port.Number], pipes[name] = true, true
	}
	return nil
}

// enabledNetworkAdapters returns the primary NIC first, followed by enabled additional NICs.
func (m Machine) enabledNetworkAdapters() []NetworkAdapter {
	adapters := make([]NetworkAdapter, 0, 1+len(m.NetworkAdapters))
	if m.Network != "none" {
		adapters = append(adapters, NetworkAdapter{Network: m.Network, SwitchID: m.SwitchID})
	}
	for _, adapter := range m.NetworkAdapters {
		if adapter.Network != "none" {
			adapters = append(adapters, adapter)
		}
	}
	return adapters
}

func ValidateShares(shares []Share) error { return sharing.Validate(sharedFolders(shares)) }

func Load(path string) (Config, error) {
	cfg := Config{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err = decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("读取配置失败（不会覆盖原文件）: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return cfg, errors.New("配置必须只包含一份 YAML 文档（不会覆盖原文件）")
	}
	ids := map[string]bool{}
	for _, m := range cfg.Machines {
		if err = m.Validate(); err != nil {
			return cfg, err
		}
		if ids[m.ID] {
			return cfg, errors.New("配置包含重复的虚拟机 ID")
		}
		ids[m.ID] = true
	}
	if err = ValidateDeviceOwnership(cfg.Machines); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := ValidateDeviceOwnership(cfg.Machines); err != nil {
		return err
	}
	var root yaml.Node
	if err := root.Encode(cfg); err != nil {
		return err
	}
	comments := map[string]string{
		"side_channel_mitigations": "全局侧通道缓解开关，默认 false；关闭时向新启动的虚拟机隐藏推测执行控制能力，不修改宿主策略，运行中的虚拟机需关机后启动生效。",
		"network_adapters":         "附加网卡，不包含 network/switch_id 指定的主网卡；合计最多 8 张。",
		"mac_address":              "可选 6 字节单播 MAC 地址；留空由 HCN 随机分配，同一虚拟机不能重复。",
		"com_ports":                "串口列表；仅支持 COM1、COM2，编号和管道均不能重复。",
		"number":                   "串口编号：1 表示 COM1，2 表示 COM2。",
		"pipe":                     `本机绝对命名管道 \\.\pipe\...；my-vm- 前缀保留给控制台。`,
		"guest_os":                 "客户机系统：windows10、windows11、ubuntu26；旧 windows/linux 和空值继续兼容，不改动已安装系统。",
		"linux_desktop":            "Linux 增强会话仅支持 wayland（现有 GNOME/KDE）；旧 x11 配置只为兼容读取保留，增强方式已停用，不安装或替换桌面。",
		"dynamic_memory":           "允许 HCS 按需分配内存及运行中修改内存；实际回收能力取决于客户机集成驱动。",
		"gpu_shared":               "启用 Hyper-V GPU-PV 共享，默认关闭；需要兼容主机和客户机驱动，不保证显卡支持。",
		"backend":                  "hcs 使用 Hyper-V Host Compute Service；旧 qemu 配置仅保留记录和磁盘，不再启动。",
		"switch_id":                "HCN 交换机 GUID；NAT 留空时精确选择系统 Default Switch，桥接须指定现有外部交换机。",
		"clipboard":                "允许增强会话的剪贴板重定向；可用格式由客户机远程桌面服务决定。",
		"shares":                   "映射到客户机的主机文件夹；默认只读。",
		"path":                     "主机文件夹的绝对路径。", "read_only": "true 只允许读取，false 允许修改主机文件。",
		"qemu_dir": "旧版本的 QEMU 目录，仅为兼容读取保留，HCS 不使用。",
		"machines": "虚拟机清单；HCS 磁盘位于同目录 machines/<id>/disk.vhdx。",
		"id":       "内部唯一标识；请勿修改。", "name": "显示名称；共享项名称需为字母开头的英文、数字、下划线或连字符。", "cpus": "虚拟 CPU 数量。",
		"memory_mib": "内存大小，单位 MiB。", "disk_gib": "创建时的磁盘容量，单位 GiB；修改此字段不会调整磁盘。",
		"iso": "安装光盘 ISO 的绝对路径；留空表示不挂载光盘。", "network": "网络模式：nat、bridge 或 none（断开网络，复制后先处理客户机身份冲突）。",
		"driver_iso":  "客户机工具或驱动光盘 ISO 的绝对路径；作为第二张只读光盘挂载，可与安装光盘同时使用。",
		"display":     "旧版本的 std/virtio 显示设备记录；HCS 使用 Hyper-V 显示和本地 RDP。",
		"tap":         "旧版本的 TAP 网卡记录；HCS 使用 HCN 交换机。",
		"accelerator": "旧版本硬件加速记录；HCS 始终使用 Hyper-V 硬件虚拟化。",
	}
	var annotate func(*yaml.Node)
	annotate = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				n.Content[i].HeadComment = comments[n.Content[i].Value]
			}
		}
		for _, child := range n.Content {
			annotate(child)
		}
	}
	annotate(&root)
	data, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp := path + ".tmp"
	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp, path)
	}
	if err != nil {
		_ = os.Remove(temp)
	}
	return err
}

func ValidateDeviceOwnership(machines []Machine) error {
	pipes, macs := map[string]string{}, map[string]string{}
	for _, machine := range machines {
		for _, port := range machine.COMPorts {
			key := strings.ToLower(port.Pipe)
			if owner, exists := pipes[key]; exists {
				return fmt.Errorf("虚拟机「%s」和「%s」使用了相同串口管道", owner, machine.Name)
			}
			pipes[key] = machine.Name
		}
		for _, adapter := range machine.NetworkAdapters {
			if adapter.Network == "none" || adapter.MACAddress == "" {
				continue
			}
			mac, err := net.ParseMAC(adapter.MACAddress)
			if err != nil {
				return err
			}
			key := mac.String()
			if owner, exists := macs[key]; exists {
				return fmt.Errorf("虚拟机「%s」和「%s」使用了相同网卡 MAC 地址", owner, machine.Name)
			}
			macs[key] = machine.Name
		}
	}
	return nil
}

func DiskPath(root string, m Machine) string {
	filename := "disk.qcow2"
	if m.Backend == "hcs" {
		filename = "disk.vhdx"
	}
	return filepath.Join(root, "machines", m.ID, filename)
}
