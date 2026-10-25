//go:build windows

package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"my-vm/internal/vm"
)

// Service owns runtime resources and configuration. Different machines can run
// operations concurrently; configuration commits are short and always merge latest data.
type Service struct {
	runtime        *vm.Manager
	mu             sync.Mutex
	gate           sync.RWMutex
	locks          sync.Map
	progress       sync.Map
	log            io.Writer
	mediaDir       string
	preparedMedia  map[string]bool
	cached         *Config
	configFile     os.FileInfo
	revision       uint64
	guestTransport *http.Transport
}

func NewService(root string, log io.Writer) *Service {
	if log == nil {
		log = io.Discard
	}
	return &Service{runtime: vm.NewManager(root, log), log: log, preparedMedia: map[string]bool{}, guestTransport: newGuestTransport(), revision: uint64(time.Now().UnixNano())}
}
func (s *Service) save(cfg Config) error {
	ids, names := map[string]bool{}, map[string]bool{}
	for _, m := range cfg.Machines {
		if err := m.Validate(); err != nil {
			return err
		}
		if ids[m.ID] || names[strings.ToLower(m.Name)] {
			return errors.New("配置包含重复的虚拟机 ID 或名称")
		}
		ids[m.ID], names[strings.ToLower(m.Name)] = true, true
	}
	if err := vm.ValidateDeviceOwnership(cfg.Machines); err != nil {
		return err
	}
	path := filepath.Join(s.runtime.Root, "library.yaml")
	if err := vm.Save(path, cfg); err != nil {
		return err
	}
	// Force the next read to verify the file, including edits from external tools.
	s.cached = nil
	s.revision++
	s.cleanupMedia(cfg)
	return nil
}

func SelectMachine(machines []Machine, selector string) (Machine, error) {
	for _, m := range machines {
		if m.ID == selector {
			return m, nil
		}
	}
	var found *Machine
	for _, m := range machines {
		if strings.EqualFold(m.Name, selector) {
			if found != nil {
				return Machine{}, errors.New("虚拟机名称重复，请使用 ID")
			}
			value := m
			found = &value
		}
	}
	if found == nil {
		return Machine{}, errors.New("找不到指定虚拟机")
	}
	return *found, nil
}

// Execute is the public business boundary shared by GUI, CLI and embedded callers.
func (s *Service) Execute(ctx context.Context, r Request) (value any, err error) {
	defer func() {
		if err != nil {
			fmt.Fprintf(s.log, "%s: %v\n", r.Action, err)
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	release, err := s.reserve(ctx, &r)
	if err != nil {
		return nil, err
	}
	defer release()
	s.mu.Lock()
	cfg, err := s.config()
	revision := s.revision
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	switch r.Action {
	case "list", "status":
		machines := page(cfg.Machines, r.Offset, r.Limit)
		items := make([]Item, 0, len(machines.Items))
		for _, m := range machines.Items {
			item := Item{ID: m.ID, Name: m.Name, State: s.runtime.State(m.ID), Guest: m.GuestName(), MemoryMiB: s.runtime.Memory(m.ID)}
			if item.State != "已关机" {
				item.RuntimeID, _ = s.runtime.GuestDisplayRuntime(m.ID)
			}
			if job, ok := s.progress.Load(m.ID); ok {
				copy := job.(Progress)
				item.Progress = &copy
			}
			items = append(items, item)
		}
		return Page[Item]{Total: machines.Total, Items: items, Revision: revision}, nil
	case "config":
		if r.Offset == 0 && r.Revision == revision {
			return ConfigurationPage{Revision: revision, Unchanged: true}, nil
		}
		machines := page(cfg.Machines, r.Offset, r.Limit)
		cfg.Machines = nil
		return ConfigurationPage{Settings: cfg, Machines: machines, Revision: revision}, nil
	case "settings":
		return Settings{s.runtime.Root, cfg.SideChannelMitigations}, nil
	case "settings-update":
		if r.Enabled == nil {
			return nil, errors.New("缺少 enabled")
		}
		return nil, s.change(func(cfg *Config) error { cfg.SideChannelMitigations = *r.Enabled; return nil })
	case "networks", "network-create", "network-delete", "adapters":
		return s.host(r, cfg)
	case "create":
		return s.saveMachine(r, true)
	case "shutdown-all":
		return nil, s.runtime.ShutdownAll(ctx)
	}
	machine, err := SelectMachine(cfg.Machines, r.VM)
	if err != nil {
		return nil, err
	}
	if r.resolvedID != "" && machine.ID != r.resolvedID {
		return nil, errors.New("虚拟机已从配置中移除")
	}
	switch r.Action {
	case "get":
		return machine, nil
	case "state":
		return s.runtime.State(machine.ID), nil
	case "update":
		if r.Machine == nil || r.Machine.ID != machine.ID {
			return nil, errors.New("修改配置必须保持原虚拟机 ID")
		}
		return s.saveMachine(r, false)
	case "remove":
		if s.runtime.State(machine.ID) != "已关机" {
			return nil, errors.New("请先关闭虚拟机")
		}
		err := s.change(func(cfg *Config) error {
			for i, m := range cfg.Machines {
				if m.ID == machine.ID {
					cfg.Machines = append(cfg.Machines[:i], cfg.Machines[i+1:]...)
					return nil
				}
			}
			return errors.New("虚拟机已移除")
		})
		return map[string]string{"retained_disk": vm.DiskPath(s.runtime.Root, machine)}, err
	case "clone":
		target := vm.NewClone(machine, cfg.Machines)
		if r.Machine != nil {
			target = *r.Machine
		}
		if r.Name != "" {
			target.Name = strings.TrimSpace(r.Name)
		}
		if err := uniqueMachine(cfg, target); err != nil {
			return nil, err
		}
		report, finish := s.track(machine.ID, "clone", target.ID)
		defer finish()
		if err := s.runtime.Clone(machine, target, report); err != nil {
			return nil, err
		}
		return target, s.register(target)
	case "start":
		return nil, s.runtime.Start("", machine)
	case "stop", "kill", "pause", "resume":
		command := map[string]string{"stop": "system_powerdown", "kill": "kill", "pause": "stop", "resume": "cont"}[r.Action]
		return nil, s.runtime.Control(machine.ID, command)
	case "memory":
		return s.runtime.Memory(machine.ID), nil
	case "memory-set":
		return nil, s.runtime.SetMemory(machine.ID, r.Size)
	case "snapshots":
		snapshots, err := s.runtime.Snapshots("", machine)
		return page(snapshots, r.Offset, r.Limit), err
	case "snapshot-create", "snapshot-restore", "snapshot-delete":
		operation := map[string]string{"snapshot-create": "-c", "snapshot-restore": "-a", "snapshot-delete": "-d"}[r.Action]
		report, finish := s.track(machine.ID, r.Action, "")
		defer finish()
		return nil, s.runtime.SnapshotContext(ctx, machine, operation, r.Name, report)
	case "enhancements":
		return s.enhancements(machine)
	case "media-mount":
		return nil, s.runtime.MountGuestMedia(machine.ID, r.Path)
	case "display-info":
		runtime, err := s.runtime.GuestDisplayRuntime(machine.ID)
		return DisplayInfo{Machine: machine, RuntimeID: runtime, Paused: s.runtime.State(machine.ID) == "已暂停"}, err
	case "runtime":
		return s.runtime.GuestRuntime(machine.ID)
	default:
		return nil, errors.New("未知业务操作: " + r.Action)
	}
}
