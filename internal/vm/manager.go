package vm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"my-vm/internal/hcs"
	"my-vm/internal/sharing"
)

type session struct {
	shares           *sharing.Service
	console          *exec.Cmd
	consoleEnhanced  bool
	consoleAutomatic bool
	system           *hcs.System
	endpoint         *hcs.Endpoint
	paused           bool
	runtimeID        string
	machine          Machine
}

type Manager struct {
	Root       string
	Log        io.Writer
	Changed    func(error)
	mu         sync.Mutex
	operations sync.Map
	sessions   map[string]*session
}

func NewManager(root string, log io.Writer) *Manager {
	return &Manager{Root: root, Log: log, sessions: map[string]*session{}}
}
func (m *Manager) operation(id string) *sync.Mutex {
	lock, _ := m.operations.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (m *Manager) State(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		if s.paused {
			return "已暂停"
		}
		return "运行中"
	}
	return "已关机"
}
func (m *Manager) Active() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.sessions) > 0 }
func requireHCS(machine Machine) error {
	if err := machine.Validate(); err != nil {
		return err
	}
	if machine.Backend != "hcs" {
		return errors.New("此虚拟机使用旧 QEMU/qcow2 格式，磁盘已保留；请新建 HCS 虚拟机。旧磁盘不能直接用于 UEFI/VHDX 后端")
	}
	return nil
}
func (m *Manager) Start(_ string, machine Machine) error {
	lock := m.operation(machine.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := requireHCS(machine); err != nil {
		return err
	}
	if m.State(machine.ID) != "已关机" {
		return errors.New("虚拟机已经启动")
	}
	disk := DiskPath(m.Root, machine)
	files := []string{disk, machine.ISO, machine.DriverISO}
	for _, path := range files {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return errors.New("磁盘和 ISO 必须使用绝对路径")
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return errors.New("磁盘或光盘路径不能是目录")
		}
	}
	id := "my-vm-" + machine.ID
	for _, path := range files {
		if path != "" {
			if err := hcs.PrepareFile(id, path, ""); err != nil {
				return err
			}
		}
	}
	folder := filepath.Dir(disk)
	for _, file := range []struct{ name, create string }{{"guest.vmgs", "HcsCreateEmptyGuestStateFile"}, {"runtime.vmrs", "HcsCreateEmptyRuntimeStateFile"}} {
		path := filepath.Join(folder, file.name)
		create := ""
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			create = file.create
		} else if err != nil {
			return err
		}
		if err := hcs.PrepareFile(id, path, create); err != nil {
			return err
		}
	}
	var endpoint *hcs.Endpoint
	if machine.Network != "none" {
		networks, err := hcs.Networks()
		if err != nil {
			return err
		}
		selected, err := selectNetwork(networks, machine.SwitchID)
		if err != nil {
			return err
		}
		endpoint, err = hcs.CreateEndpoint(selected.ID, id)
		if err != nil {
			return err
		}
		if m.Log != nil {
			fmt.Fprintf(m.Log, "VM %s network: %s (%s), endpoint: %s\n", machine.ID, selected.Name, selected.ID, endpoint.String())
		}
	}
	endpointID := ""
	if endpoint != nil {
		endpointID = endpoint.String()
	}
	configuration, err := Configuration(m.Root, machine, endpointID)
	if err != nil {
		_ = endpoint.Close()
		return err
	}
	system, err := hcs.Create(id, configuration)
	if err != nil {
		_ = endpoint.Close()
		return err
	}
	failed := true
	defer func() {
		if failed {
			system.Close()
			_ = endpoint.Close()
		}
	}()
	if err = system.Action("HcsStartComputeSystem"); err != nil {
		return err
	}
	if machine.GPUShared {
		if err = system.Modify("VirtualMachine/ComputeTopology/Gpu", map[string]any{"AssignmentMode": "Default", "AllowVendorExtension": true}); err != nil {
			return fmt.Errorf("启用 GPU 共享失败（未回退为纯 CPU）: %w", err)
		}
	}
	properties, err := system.Properties()
	if err != nil {
		return err
	}
	runtimeID, _ := properties["RuntimeId"].(string)
	if runtimeID == "" {
		return errors.New("HCS 未返回虚拟机 RuntimeId")
	}
	if m.Log != nil {
		fmt.Fprintln(m.Log, "HCS RuntimeId:", runtimeID)
	}
	s := &session{system: system, endpoint: endpoint, runtimeID: strings.Trim(runtimeID, "{}"), machine: machine}
	if len(machine.Shares) > 0 {
		s.shares, err = sharing.Start(s.runtimeID, machine.Shares, m.Log)
		if err != nil {
			return fmt.Errorf("启动共享文件夹失败: %w", err)
		}
	}
	m.mu.Lock()
	m.sessions[machine.ID] = s
	m.mu.Unlock()
	failed = false
	go m.watch(machine.ID, s)
	return nil
}
func (m *Manager) watch(id string, s *session) {
	<-s.system.Done()
	lock := m.operation(id)
	lock.Lock()
	m.mu.Lock()
	if m.sessions[id] != s {
		m.mu.Unlock()
		lock.Unlock()
		return
	}
	delete(m.sessions, id)
	console := s.console
	m.mu.Unlock()
	if console != nil {
		_ = console.Process.Kill()
	}
	if s.shares != nil {
		_ = s.shares.Close()
	}
	s.system.Close()
	if cleanup := s.endpoint.Close(); cleanup != nil && m.Log != nil {
		fmt.Fprintln(m.Log, cleanup)
	}
	lock.Unlock()
	if m.Changed != nil {
		m.Changed(nil)
	}
}
func (m *Manager) Control(id, command string) error {
	lock := m.operation(id)
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return errors.New("虚拟机未运行")
	}
	action := map[string]string{"kill": "HcsTerminateComputeSystem", "system_powerdown": "HcsShutDownComputeSystem", "stop": "HcsPauseComputeSystem", "cont": "HcsResumeComputeSystem"}[command]
	if action == "" {
		return errors.New("不支持的控制命令")
	}
	if err := s.system.Action(action); err != nil {
		return err
	}
	m.mu.Lock()
	if command == "stop" {
		s.paused = true
	}
	if command == "cont" {
		s.paused = false
	}
	m.mu.Unlock()
	return nil
}
func (m *Manager) CreateDisk(_ string, machine Machine) error {
	lock := m.operation(machine.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := requireHCS(machine); err != nil {
		return err
	}
	path := DiskPath(m.Root, machine)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return hcs.CreateDisk(path, machine.DiskGiB)
}
