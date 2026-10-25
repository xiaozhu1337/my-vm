package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"my-vm/internal/checkpoint"
	"my-vm/internal/hcs"
	"my-vm/internal/rdpauth"
	"my-vm/internal/sharing"
)

type session struct {
	shares     *sharing.Service
	system     *hcs.System
	endpoints  []*hcs.Endpoint
	adapterIDs []string
	paused     bool
	runtimeID  string
	machine    Machine
	disk       string
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

// GuestStatus binds a subscription to the current session, never a later VM restart.
func (m *Manager) GuestStatus(id, runtimeID string) (*sharing.StatusFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil || runtimeID == "" || !strings.EqualFold(s.runtimeID, strings.Trim(runtimeID, "{}")) || s.shares == nil {
		return nil, errors.New("客户机状态通道未运行或会话已更换")
	}
	return s.shares.Status, nil
}
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
	return m.start(machine)
}

// start runs while the caller owns this machine's operation lock.
func (m *Manager) start(machine Machine) error {
	return m.startWithState(machine, nil)
}

func (m *Manager) startWithState(machine Machine, saved *checkpoint.Entry) error {
	return m.startSession(machine, saved, saved != nil)
}

func (m *Manager) startSession(machine Machine, saved *checkpoint.Entry, restoring bool) error {
	if err := requireHCS(machine); err != nil {
		return err
	}
	if m.State(machine.ID) != "已关机" {
		return errors.New("虚拟机已经启动")
	}
	storage, err := m.loadStorage(machine, restoring)
	if err != nil {
		return err
	}
	if !restoring && m.State(machine.ID) != "已关机" {
		return nil // Recovery may have reloaded the original execution state.
	}
	if err = storage.CheckFiles(storage.Chain()...); err != nil {
		return err
	}
	if saved != nil {
		if machine, err = loadSnapshotMachine(machine, storage, *saved); err != nil {
			return err
		}
		if err = copyState(snapshotStatePath(storage, *saved, "vmgs"), storage.Path("guest.vmgs"), true); err != nil {
			return err
		}
	}
	disk := storage.Path(storage.Active)
	files := []string{disk, machine.ISO, machine.DriverISO}
	for _, parent := range storage.Chain()[1:] {
		files = append(files, storage.Path(parent))
	}
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
	folder := filepath.Dir(DiskPath(m.Root, machine))
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
	var networks []hcs.Network
	if len(machine.enabledNetworkAdapters()) > 0 {
		var err error
		networks, err = hcs.Networks()
		if err != nil {
			return err
		}
	}
	createEndpoint := hcs.CreateEndpoint
	if saved != nil {
		if len(saved.MACs) != len(machine.enabledNetworkAdapters()) {
			return errors.New("快照缺少网卡 MAC 地址")
		}
		next := 0
		createEndpoint = func(switchID, owner string, _ ...string) (*hcs.Endpoint, error) {
			mac := saved.MACs[next]
			next++
			return hcs.CreateEndpoint(switchID, owner, mac)
		}
	}
	endpoints, err := createNetworkEndpoints(machine, networks, id, createEndpoint, (*hcs.Endpoint).Close)
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		if failed {
			if cleanup := closeNetworkEndpoints(endpoints, (*hcs.Endpoint).Close); cleanup != nil && m.Log != nil {
				fmt.Fprintln(m.Log, cleanup)
			}
		}
	}()
	endpointIDs := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpointIDs = append(endpointIDs, endpoint.String())
		if m.Log != nil {
			fmt.Fprintf(m.Log, "VM %s network endpoint: %s\n", machine.ID, endpoint.String())
		}
	}
	configuration, err := ConfigurationWithEndpoints(m.Root, machine, endpointIDs)
	if err != nil {
		return err
	}
	if saved != nil {
		statePath := snapshotStatePath(storage, *saved, "vmrs")
		if err = hcs.PrepareFile(id, statePath, ""); err != nil {
			return err
		}
		if err = applySnapshotState(configuration, storage, *saved, endpointIDs); err != nil {
			return err
		}
	}
	system, err := hcs.Create(id, configuration)
	if err != nil {
		return err
	}
	defer func() {
		if failed {
			system.Close()
		}
	}()
	if err = system.Action("HcsStartComputeSystem"); err != nil {
		return err
	}
	if machine.GPUShared && saved == nil {
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
	s := &session{system: system, endpoints: endpoints, adapterIDs: endpointIDs, runtimeID: strings.Trim(runtimeID, "{}"), machine: machine, disk: storage.Active}
	if saved != nil {
		s.adapterIDs = append([]string(nil), saved.Adapters...)
		if saved.State == "paused" {
			if err = system.Action("HcsPauseComputeSystem"); err != nil {
				return err
			}
			s.paused = true
		}
	}
	var saveLogin func(*rdpauth.Login) error
	if machine.IsLinux() {
		saveLogin = func(login *rdpauth.Login) error {
			err := rdpauth.Save(`\\.\pipe\my-vm-`+machine.ID+".enhanced", login)
			if err != nil && m.Log != nil {
				fmt.Fprintln(m.Log, "保存增强登录凭据失败:", err)
			}
			return err
		}
	}
	// Status reporting also needs this VM-bound listener when no folders are shared.
	s.shares, err = sharing.Start(s.runtimeID, sharedFolders(machine.Shares), m.Log, saveLogin)
	if err != nil {
		return fmt.Errorf("启动客户机回传通道失败: %w", err)
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
	m.mu.Unlock()
	if s.shares != nil {
		_ = s.shares.Close()
	}
	s.system.Close()
	if cleanup := closeNetworkEndpoints(s.endpoints, (*hcs.Endpoint).Close); cleanup != nil && m.Log != nil {
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
func (m *Manager) ShutdownAll(ctx context.Context) error {
	m.mu.Lock()
	machines := make([]Machine, 0, len(m.sessions))
	for id, s := range m.sessions {
		machine := s.machine
		machine.ID = id
		machines = append(machines, machine)
	}
	m.mu.Unlock()
	if err := shutdownAll(ctx, machines, m.State, m.Control); err != nil {
		return err
	}
	// watch removes sessions before cleanup; wait until its operation lock is released.
	for _, machine := range machines {
		lock := m.operation(machine.ID)
		lock.Lock()
		lock.Unlock()
	}
	return nil
}

func shutdownAll(ctx context.Context, machines []Machine, state func(string) string, control func(string, string) error) error {
	results := make(chan error, len(machines))
	for _, machine := range machines {
		go func() {
			err := shutdownMachine(ctx, machine.ID, state, control)
			if err != nil {
				err = fmt.Errorf("正常关闭「%s」: %w", machine.Name, err)
			}
			results <- err
		}()
	}
	var failures []error
	for range machines {
		if err := <-results; err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func shutdownMachine(ctx context.Context, id string, state func(string) string, control func(string, string) error) error {
	if state(id) == "已关机" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state(id) == "已暂停" {
		if err := control(id, "cont"); err != nil && state(id) != "已关机" {
			return fmt.Errorf("恢复后才能正常关机: %w", err)
		}
	}
	if state(id) == "已关机" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := control(id, "system_powerdown"); err != nil && state(id) != "已关机" {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for state(id) != "已关机" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待关机超时或已取消，管理器保持打开: %w", ctx.Err())
		case <-ticker.C:
		}
	}
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
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "storage.yaml")); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return errors.New("虚拟机已有磁盘链，不能重新创建基础盘")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return hcs.CreateDisk(path, machine.DiskGiB)
}
