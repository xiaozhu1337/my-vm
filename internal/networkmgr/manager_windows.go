//go:build windows

package networkmgr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/windows"
)

const defaultSwitch = "c08cb7b8-9b3c-408e-8e30-5e16a3aeb444"

var operations sync.Mutex

type Network struct{ ID, Name, Type, Subnet string }
type Record struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Subnet  string `yaml:"subnet"`
	Pending bool   `yaml:"pending"`
}
type backend struct {
	list         func() ([]Network, error)
	hostPrefixes func() ([]netip.Prefix, error)
	create       func(string, string, string) error
	remove       func(string) error
	hasEndpoints func(string) (bool, error)
}
type Manager struct {
	path string
	api  backend
}

func New(root string) *Manager {
	return &Manager{path: filepath.Join(root, "networks.yaml"), api: backend{list: listNetworks, hostPrefixes: hostPrefixes, create: createNetwork, remove: removeNetwork, hasEndpoints: hasEndpoints}}
}

func (m *Manager) records() ([]Record, error) {
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var records []Record
	if err = decoder.Decode(&records); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("网络登记文件必须只有一份 YAML 文档")
	}
	seen := map[string]bool{}
	for _, r := range records {
		if _, err = windows.GUIDFromString("{" + r.ID + "}"); err != nil || strings.EqualFold(r.ID, defaultSwitch) || seen[strings.ToLower(r.ID)] {
			return nil, errors.New("网络登记文件包含无效或重复 ID")
		}
		if _, err = validateNetwork(r.Name, r.Subnet); err != nil {
			return nil, err
		}
		seen[strings.ToLower(r.ID)] = true
	}
	return records, nil
}
func (m *Manager) save(records []Record) error {
	data, err := yaml.Marshal(records)
	if err != nil {
		return err
	}
	data = append([]byte("# 本程序创建的 NAT 网络；id 为 HCN 身份，name 为名称，subnet 为 IPv4 网段。\n# pending 表示创建尚未确认，可刷新后重试删除；不按名称认领外部网络。\n"), data...)
	if err = os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".networks-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	source, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(m.path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func (m *Manager) List() ([]Network, []Record, error) {
	operations.Lock()
	defer operations.Unlock()
	records, err := m.records()
	if err != nil {
		return nil, nil, err
	}
	networks, err := m.api.list()
	return networks, records, err
}
func validateNetwork(name, cidr string) (netip.Prefix, error) {
	if strings.TrimSpace(name) != name || name == "" || len([]rune(name)) > 64 || strings.ContainsAny(name, "\r\n\x00") {
		return netip.Prefix{}, errors.New("网络名称需要 1 到 64 个字符，不能含控制字符或首尾空白")
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 16 || prefix.Bits() > 28 || !prefix.Addr().IsPrivate() {
		return netip.Prefix{}, errors.New("使用规范的私有 IPv4 网段，前缀长度 16 到 28，例如 192.168.240.0/24")
	}
	return prefix, nil
}
func (m *Manager) Create(name, cidr string) error {
	operations.Lock()
	defer operations.Unlock()
	prefix, err := validateNetwork(name, cidr)
	if err != nil {
		return err
	}
	records, err := m.records()
	if err != nil {
		return err
	}
	networks, err := m.api.list()
	if err != nil {
		return err
	}
	for _, r := range records {
		if strings.EqualFold(r.Name, name) {
			return errors.New("此网络名称已登记，请先处理原有记录")
		}
	}
	prefixes, err := m.api.hostPrefixes()
	if err != nil {
		return err
	}
	for _, n := range networks {
		if strings.EqualFold(n.Name, name) {
			return errors.New("此网络名称已存在")
		}
		for _, cidr := range strings.Split(n.Subnet, ",") {
			if p, e := netip.ParsePrefix(cidr); e == nil {
				prefixes = append(prefixes, p)
			}
		}
	}
	for _, p := range prefixes {
		if prefix.Overlaps(p) {
			return fmt.Errorf("网段与现有网络 %s 重叠", p)
		}
	}
	id, err := windows.GenerateGUID()
	if err != nil {
		return err
	}
	record := Record{ID: strings.ToLower(strings.Trim(id.String(), "{}")), Name: name, Subnet: cidr, Pending: true}
	records = append(records, record)
	if err = m.save(records); err != nil {
		return err
	}
	if err = m.api.create(record.ID, name, cidr); err != nil {
		return fmt.Errorf("创建未完成，登记已保留，可刷新并删除以恢复: %w", err)
	}
	records[len(records)-1].Pending = false
	if err = m.save(records); err != nil {
		return fmt.Errorf("网络已创建，确认登记失败；保留的待确认记录可用于恢复: %w", err)
	}
	return nil
}
func (m *Manager) Delete(id string, references []string) error {
	operations.Lock()
	defer operations.Unlock()
	records, err := m.records()
	if err != nil {
		return err
	}
	index := -1
	for i, r := range records {
		if strings.EqualFold(r.ID, id) {
			index = i
			break
		}
	}
	if index < 0 || strings.EqualFold(id, defaultSwitch) {
		return errors.New("仅允许删除本程序创建的自定义 NAT 网络")
	}
	for _, ref := range references {
		if strings.EqualFold(strings.Trim(ref, "{}"), id) {
			return errors.New("仍有虚拟机配置使用此网络，请先移除对应网卡连接")
		}
	}
	networks, err := m.api.list()
	if err != nil {
		return err
	}
	found := false
	for _, n := range networks {
		if !strings.EqualFold(n.ID, id) {
			continue
		}
		record := records[index]
		if n.Type != "NAT" || n.Name != record.Name || n.Subnet != record.Subnet {
			return errors.New("网络身份或属性已改变，拒绝删除")
		}
		found = true
	}
	if found {
		used, err := m.api.hasEndpoints(id)
		if err != nil {
			return err
		}
		if used {
			return errors.New("此网络仍有 HCN 端点，拒绝删除")
		}
		if err = m.api.remove(id); err != nil {
			return err
		}
	}
	records = append(records[:index], records[index+1:]...)
	if err = m.save(records); err != nil {
		return fmt.Errorf("网络已删除，登记清理失败，可再次删除恢复: %w", err)
	}
	return nil
}
