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

	"go.yaml.in/yaml/v3"
)

type Machine struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	CPUs        int    `yaml:"cpus"`
	MemoryMiB   int    `yaml:"memory_mib"`
	DiskGiB     int    `yaml:"disk_gib"`
	ISO         string `yaml:"iso"`
	Network     string `yaml:"network"`
	TAP         string `yaml:"tap"`
	Accelerator string `yaml:"accelerator"`
}

type Config struct {
	QEMUDir  string    `yaml:"qemu_dir"`
	Machines []Machine `yaml:"machines"`
}

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Snapshot names start with a letter to avoid QEMU version differences in numeric ID/name resolution.
var validSnapshot = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func NewMachine() Machine {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return Machine{ID: hex.EncodeToString(id[:]), Name: "新虚拟机", CPUs: 2, MemoryMiB: 4096, DiskGiB: 64, Network: "nat", Accelerator: "whpx"}
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
	case m.Network != "nat" && m.Network != "bridge":
		return errors.New("网络模式必须为 nat 或 bridge")
	case m.Network == "bridge" && (strings.TrimSpace(m.TAP) == "" || strings.ContainsAny(m.TAP, ",\r\n")):
		return errors.New("桥接模式需要有效的 TAP 网卡名称，名称不能含逗号或换行")
	case m.Accelerator != "whpx" && m.Accelerator != "tcg":
		return errors.New("加速器必须为 whpx 或 tcg")
	}
	return nil
}

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
		"qemu_dir": "QEMU 安装目录；留空时从 PATH 查找 qemu-system-x86_64.exe 和 qemu-img.exe。",
		"machines": "虚拟机清单；磁盘位于同目录 machines/<id>/disk.qcow2。",
		"id":       "内部唯一标识；请勿修改。", "name": "虚拟机显示名称。", "cpus": "虚拟 CPU 数量。",
		"memory_mib": "内存大小，单位 MiB。", "disk_gib": "创建时的磁盘容量，单位 GiB；修改此字段不会调整磁盘。",
		"iso": "安装光盘 ISO 的绝对路径；留空表示不挂载光盘。", "network": "网络模式：nat 或 bridge。",
		"tap":         "bridge 模式使用的 Windows TAP 网卡名称；须事先与物理网卡建立网络桥。",
		"accelerator": "whpx 使用 Windows Hypervisor Platform；tcg 使用软件模拟。",
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
	return filepath.Join(root, "machines", m.ID, "disk.qcow2")
}
