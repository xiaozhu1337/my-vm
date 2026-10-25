//go:build windows

package library

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"my-vm/internal/hostmedia"
	"my-vm/internal/networkmgr"
	"my-vm/internal/vm"
)

func ReadHostAdapters() ([]HostAdapter, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	adapters := make([]HostAdapter, 0, len(interfaces))
	for _, adapter := range interfaces {
		addresses, err := adapter.Addrs()
		if err != nil {
			return nil, err
		}
		values := make([]string, 0, len(addresses))
		for _, address := range addresses {
			values = append(values, address.String())
		}
		state := "未连接"
		if adapter.Flags&net.FlagUp != 0 {
			state = "已启用"
		}
		adapters = append(adapters, HostAdapter{adapter.Name, adapter.HardwareAddr.String(), strings.Join(values, ", "), state, adapter.MTU})
	}
	return adapters, nil
}

func (s *Service) host(r Request, cfg Config) (any, error) {
	manager := networkmgr.New(s.runtime.Root)
	switch r.Action {
	case "networks":
		networks, records, err := manager.List()
		if err != nil {
			return nil, err
		}
		items := make([]NetworkItem, 0, len(networks))
		seen := map[string]bool{}
		for _, n := range networks {
			item := NetworkItem{Network: n}
			for _, record := range records {
				if strings.EqualFold(record.ID, n.ID) {
					item.Managed, item.Pending = true, record.Pending
				}
			}
			seen[strings.ToLower(n.ID)] = true
			items = append(items, item)
		}
		for _, record := range records {
			if !seen[strings.ToLower(record.ID)] {
				items = append(items, NetworkItem{Network: Network{ID: record.ID, Name: record.Name, Type: "NAT", Subnet: record.Subnet}, Managed: true, Pending: true})
			}
		}
		return page(items, r.Offset, r.Limit), nil
	case "adapters":
		adapters, err := ReadHostAdapters()
		return page(adapters, r.Offset, r.Limit), err
	case "network-create":
		return nil, manager.Create(strings.TrimSpace(r.Name), strings.TrimSpace(r.CIDR))
	case "network-delete":
		var refs []string
		for _, m := range cfg.Machines {
			if m.Network != "none" && m.SwitchID != "" {
				refs = append(refs, m.SwitchID)
			}
			for _, adapter := range m.NetworkAdapters {
				if adapter.Network != "none" && adapter.SwitchID != "" {
					refs = append(refs, adapter.SwitchID)
				}
			}
		}
		return nil, manager.Delete(r.Name, refs)
	}
	return nil, errors.New("未知宿主操作")
}

func EnhancementDisc(machine Machine) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return enhancementDisc(filepath.Dir(executable), machine)
}

func enhancementDisc(directory string, machine Machine) (string, error) {
	platform := "windows"
	if machine.IsLinux() {
		platform = "linux"
	}
	target := filepath.Join(directory, "my-vm-guest-tools-"+platform+".iso")
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("增强光盘不可用，请先运行构建脚本: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("增强光盘不是普通文件")
	}
	return target, nil
}

func (s *Service) enhancements(machine Machine) (any, error) {
	directory := s.mediaDir
	if directory == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		directory = filepath.Dir(executable)
	}
	target, err := enhancementDisc(directory, machine)
	if err != nil {
		return nil, err
	}
	if machine.GPUShared {
		platform := "windows"
		if machine.IsLinux() {
			platform = "linux"
		}
		target, err = hostmedia.Prepare(target, platform, filepath.Dir(vm.DiskPath(s.runtime.Root, machine)))
		if err != nil {
			return nil, err
		}
		defer func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.preparedMedia[target] = false
			if current, loadError := s.config(); loadError == nil {
				s.cleanupMedia(current)
			}
		}()
	}
	if s.runtime.State(machine.ID) != "已关机" {
		if err = s.runtime.MountGuestMedia(machine.ID, target); err != nil {
			return nil, err
		}
	}
	if err = s.change(func(current *Config) error {
		for i, m := range current.Machines {
			if m.ID == machine.ID {
				current.Machines[i].DriverISO = target
				return nil
			}
		}
		return errors.New("虚拟机已从配置中移除")
	}); err != nil {
		return nil, fmt.Errorf("增强光盘已准备在 %s，但配置未保存: %w", target, err)
	}
	return map[string]string{"path": target}, nil
}
