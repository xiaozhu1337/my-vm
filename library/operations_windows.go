//go:build windows

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"my-vm/internal/vm"
)

func waitLock(ctx context.Context, try func() bool) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if try() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) reserve(ctx context.Context, r *Request) (func(), error) {
	switch r.Action {
	case "network-create", "network-delete", "shutdown-all":
		if err := waitLock(ctx, s.gate.TryLock); err != nil {
			return nil, err
		}
		return s.gate.Unlock, nil
	case "create", "update", "remove", "clone", "start", "stop", "kill", "pause", "resume", "snapshot-create", "snapshot-restore", "snapshot-delete", "snapshots", "enhancements", "media-mount":
	default:
		return func() {}, nil
	}
	ids := []string{}
	if r.Action != "create" {
		s.mu.Lock()
		cfg, err := s.config()
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		machine, err := SelectMachine(cfg.Machines, r.VM)
		if err != nil {
			return nil, err
		}
		if r.resolvedID != "" && machine.ID != r.resolvedID {
			return nil, errors.New("虚拟机已从配置中移除")
		}
		// Bind the selector before waiting; another client may rename machines meanwhile.
		r.VM, r.resolvedID = machine.ID, machine.ID
		ids = append(ids, machine.ID)
	}
	if (r.Action == "create" || r.Action == "clone") && r.Machine != nil {
		ids = append(ids, r.Machine.ID)
	}
	if err := waitLock(ctx, s.gate.TryRLock); err != nil {
		return nil, err
	}
	release := s.gate.RUnlock
	sort.Strings(ids)
	for i, id := range ids {
		if i > 0 && ids[i-1] == id {
			continue
		}
		value, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
		lock := value.(*sync.Mutex)
		if err := waitLock(ctx, lock.TryLock); err != nil {
			release()
			return nil, err
		}
		previous := release
		release = func() { lock.Unlock(); previous() }
	}
	return release, nil
}

func (s *Service) track(id, action, target string) (func(int64, int64), func()) {
	report := func(done, total int64) {
		s.progress.Store(id, Progress{Action: action, Target: target, Completed: done, Total: total})
	}
	report(0, 0)
	return report, func() { s.progress.Delete(id) }
}

func (s *Service) change(edit func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.config()
	if err != nil {
		return err
	}
	if err = edit(&cfg); err != nil {
		return err
	}
	return s.save(cfg)
}
func uniqueMachine(cfg Config, target Machine) error {
	if err := target.Validate(); err != nil {
		return err
	}
	for _, m := range cfg.Machines {
		if m.ID == target.ID || strings.EqualFold(m.Name, target.Name) {
			return errors.New("虚拟机 ID 或名称已存在")
		}
	}
	return vm.ValidateDeviceOwnership(append(append([]Machine{}, cfg.Machines...), target))
}
func (s *Service) register(target Machine) error {
	err := s.change(func(cfg *Config) error {
		if err := uniqueMachine(*cfg, target); err != nil {
			return err
		}
		cfg.Machines = append(cfg.Machines, target)
		return nil
	})
	if err != nil {
		return fmt.Errorf("配置保存失败；磁盘保留在 %s: %w", vm.DiskPath(s.runtime.Root, target), err)
	}
	return nil
}
func (s *Service) saveMachine(r Request, creating bool) (any, error) {
	if r.Machine == nil {
		return nil, errors.New("缺少完整虚拟机配置")
	}
	target := *r.Machine
	target.Name = strings.TrimSpace(target.Name)
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if s.runtime.State(target.ID) != "已关机" {
		return nil, errors.New("请先关闭虚拟机再修改配置")
	}
	if creating {
		s.mu.Lock()
		cfg, err := s.config()
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err = uniqueMachine(cfg, target); err != nil {
			return nil, err
		}
		if err = s.runtime.CreateDisk("", target); err != nil {
			return nil, err
		}
		return target, s.register(target)
	}
	err := s.change(func(cfg *Config) error {
		for i, m := range cfg.Machines {
			if m.ID == target.ID {
				if r.Expected != nil {
					current, _ := json.Marshal(m)
					expected, _ := json.Marshal(r.Expected)
					if string(current) != string(expected) {
						return errors.New("配置已被其他操作修改，请重新读取后再保存")
					}
				}
				if m.DiskGiB != target.DiskGiB || m.Backend != target.Backend {
					return errors.New("不能通过配置修改磁盘容量或后端")
				}
				cfg.Machines[i] = target
				return nil
			}
		}
		return errors.New("虚拟机已从配置中移除")
	})
	return target, err
}

// Published images are retained because another client's unsaved draft may still
// reference them. Only unpublished, unmounted images from this service are reclaimed.
// shortcut: retain published images, add cross-client media leases before reclaiming them.
func (s *Service) cleanupMedia(cfg Config) {
	for path, published := range s.preparedMedia {
		if published {
			continue
		}
		inUse := s.runtime.GuestMediaInUse(path)
		for _, m := range cfg.Machines {
			if vm.SameMediaFile(m.ISO, path) || vm.SameMediaFile(m.DriverISO, path) {
				s.preparedMedia[path] = true
				inUse = true
			}
		}
		if inUse {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(s.log, "清理增强光盘:", err)
			continue
		}
		delete(s.preparedMedia, path)
	}
}
