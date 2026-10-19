package vm

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"my-vm/internal/sharing"

	"go.yaml.in/yaml/v3"
)

type Machine struct {
	GuestOS       string  `yaml:"guest_os"`
	DynamicMemory bool    `yaml:"dynamic_memory"`
	GPUShared     bool    `yaml:"gpu_shared"`
	Backend       string  `yaml:"backend"`
	SwitchID      string  `yaml:"switch_id"`
	Clipboard     bool    `yaml:"clipboard"`
	Shares        []Share `yaml:"shares,omitempty"`
	ID            string  `yaml:"id"`
	Name          string  `yaml:"name"`
	CPUs          int     `yaml:"cpus"`
	MemoryMiB     int     `yaml:"memory_mib"`
	DiskGiB       int     `yaml:"disk_gib"`
	ISO           string  `yaml:"iso"`
	DriverISO     string  `yaml:"driver_iso"`
	Display       string  `yaml:"display"`
	Network       string  `yaml:"network"`
	TAP           string  `yaml:"tap"`
	Accelerator   string  `yaml:"accelerator"`
}

type Share = sharing.Share

type Config struct {
	QEMUDir  string    `yaml:"qemu_dir"`
	Machines []Machine `yaml:"machines"`
}

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Snapshot names are safe directory names independent of the storage backend.
var validSnapshot = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func NewMachine() Machine {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return Machine{ID: hex.EncodeToString(id[:]), Backend: "hcs", GuestOS: "windows", DynamicMemory: true, Name: "新虚拟机", CPUs: 2, MemoryMiB: 4096, DiskGiB: 64, Network: "nat", Clipboard: true}
}

func (m Machine) Validate() error {
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
	case m.GuestOS != "" && m.GuestOS != "windows" && m.GuestOS != "linux":
		return errors.New("客户机系统必须为 windows 或 linux")
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
	return ValidateShares(m.Shares)
}

func ValidateShares(shares []Share) error { return sharing.Validate(shares) }

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
	return cfg, nil
}

func Save(path string, cfg Config) error {
	var root yaml.Node
	if err := root.Encode(cfg); err != nil {
		return err
	}
	comments := map[string]string{
		"guest_os":       "客户机类型：windows 或 linux；Linux 的增强会话需自行安装并配置 xrdp/vsock。",
		"dynamic_memory": "允许 HCS 按需分配内存及运行中修改内存；实际回收能力取决于客户机集成驱动。",
		"gpu_shared":     "启用 Hyper-V GPU 分区共享；需要兼容主机和客户机驱动，不保证显卡支持。",
		"backend":        "hcs 使用 Hyper-V Host Compute Service；旧 qemu 配置仅保留记录和磁盘，不再启动。",
		"switch_id":      "HCN 交换机 GUID；NAT 留空时选择系统 ICS 默认交换机，桥接须指定现有外部交换机。",
		"clipboard":      "在增强会话中允许剪贴板重定向（文本、图片和文件）。",
		"shares":         "映射到客户机的主机文件夹；默认只读。",
		"path":           "主机文件夹的绝对路径。", "read_only": "true 只允许读取，false 允许修改主机文件。",
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

func DiskPath(root string, m Machine) string {
	filename := "disk.qcow2"
	if m.Backend == "hcs" {
		filename = "disk.vhdx"
	}
	return filepath.Join(root, "machines", m.ID, filename)
}
