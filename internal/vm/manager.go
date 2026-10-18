package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type session struct {
	cmd    *exec.Cmd
	qmp    *monitor
	paused bool
	done   chan struct{}
	output *tailWriter
}

type Manager struct {
	Root     string
	Log      io.Writer
	Changed  func(error)
	mu       sync.Mutex
	opMu     sync.Mutex
	sessions map[string]*session
}

func NewManager(root string, log io.Writer) *Manager {
	return &Manager{Root: root, Log: log, sessions: map[string]*session{}}
}

func executable(dir, name string) (string, error) {
	if dir != "" {
		name = filepath.Join(dir, name+".exe")
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("未找到 %s；请设置 QEMU 安装目录: %w", name, err)
	}
	return path, nil
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

func (m *Manager) Start(dir string, machine Machine) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := machine.Validate(); err != nil {
		return err
	}
	if m.State(machine.ID) != "已关机" {
		return errors.New("虚拟机已经启动")
	}
	if _, err := os.Stat(DiskPath(m.Root, machine)); err != nil {
		return fmt.Errorf("磁盘不可用: %w", err)
	}
	if machine.ISO != "" {
		if !filepath.IsAbs(machine.ISO) {
			return errors.New("ISO 必须使用绝对路径")
		}
		info, err := os.Stat(machine.ISO)
		if err != nil {
			return fmt.Errorf("ISO 不可用: %w", err)
		}
		if info.IsDir() {
			return errors.New("ISO 路径不能是目录")
		}
	}
	exe, err := executable(dir, "qemu-system-x86_64")
	if err != nil {
		return err
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return err
	}
	defer listener.Close()
	args, err := Arguments(m.Root, machine, listener.Addr().String())
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	hideConsole(cmd)
	s := &session{cmd: cmd, done: make(chan struct{}), output: &tailWriter{log: m.Log}}
	cmd.Stdout = s.output
	cmd.Stderr = s.output
	job, err := startProcess(cmd)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sessions[machine.ID] = s
	m.mu.Unlock()
	go func() {
		err := cmd.Wait()
		_ = job.Close()
		_ = listener.Close()
		m.mu.Lock()
		delete(m.sessions, machine.ID)
		if s.qmp != nil {
			s.qmp.close()
		}
		close(s.done)
		m.mu.Unlock()
		if err != nil {
			err = fmt.Errorf("%s 已退出: %w\n%s", machine.Name, err, s.output.String())
		}
		if m.Changed != nil {
			m.Changed(err)
		}
	}()
	_ = listener.SetDeadline(time.Now().Add(10 * time.Second))
	conn, err := listener.Accept()
	if err == nil {
		var qmp *monitor
		qmp, err = newMonitor(conn)
		if err == nil {
			m.mu.Lock()
			if m.sessions[machine.ID] == s {
				s.qmp = qmp
			} else {
				err = errors.New("QEMU 已退出")
			}
			m.mu.Unlock()
		}
		if err != nil {
			_ = conn.Close()
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("QEMU 启动失败（WHPX 需启用 Windows Hypervisor Platform）: %w\n%s", err, s.output.String())
	}
	return nil
}

func (m *Manager) Control(id, command string) error {
	m.mu.Lock()
	s := m.sessions[id]
	var qmp *monitor
	if s != nil {
		qmp = s.qmp
	}
	m.mu.Unlock()
	if s == nil {
		return errors.New("虚拟机未运行")
	}
	if command == "kill" {
		return s.cmd.Process.Kill()
	}
	switch command {
	case "system_powerdown", "stop", "cont":
	default:
		return errors.New("不支持的控制命令")
	}
	if qmp == nil {
		return errors.New("QMP 尚未就绪")
	}
	if err := qmp.execute(command); err != nil {
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

func (m *Manager) image(dir string, args ...string) ([]byte, error) {
	exe, err := executable(dir, "qemu-img")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	hideConsole(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("qemu-img 失败: %w\n%s", err, output)
	}
	return output, nil
}

func (m *Manager) CreateDisk(dir string, machine Machine) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := machine.Validate(); err != nil {
		return err
	}
	disk := DiskPath(m.Root, machine)
	if err := os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
		return err
	}
	// Reserve the exact path so creating a VM can never truncate an existing disk.
	file, err := os.OpenFile(disk, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	_, err = m.image(dir, "create", "-f", "qcow2", disk, fmt.Sprintf("%dG", machine.DiskGiB))
	if err != nil {
		_ = os.Remove(disk)
	}
	return err
}

type Snapshot struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DateSeconds int64  `json:"date-sec"`
}

func (m *Manager) Snapshots(dir string, machine Machine) ([]Snapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.snapshots(dir, machine)
}

func (m *Manager) snapshots(dir string, machine Machine) ([]Snapshot, error) {
	if err := machine.Validate(); err != nil {
		return nil, err
	}
	if m.State(machine.ID) != "已关机" {
		return nil, errors.New("请先关闭虚拟机，再管理磁盘快照")
	}
	output, err := m.image(dir, "info", "--output=json", DiskPath(m.Root, machine))
	if err != nil {
		return nil, err
	}
	var info struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	err = json.Unmarshal(output, &info)
	return info.Snapshots, err
}

func (m *Manager) Snapshot(dir string, machine Machine, action, name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := machine.Validate(); err != nil {
		return err
	}
	if m.State(machine.ID) != "已关机" {
		return errors.New("请先关闭虚拟机，再管理磁盘快照")
	}
	if !validSnapshot.MatchString(name) {
		return errors.New("快照名称需为 1 到 64 个英文字母、数字、下划线或连字符，并以英文字母开头")
	}
	switch action {
	case "-c", "-a", "-d":
	default:
		return errors.New("快照操作无效")
	}
	snapshots, err := m.snapshots(dir, machine)
	if err != nil {
		return err
	}
	exists := false
	for _, s := range snapshots {
		if s.Name == name {
			if exists {
				return errors.New("存在重名快照，无法安全执行操作")
			}
			exists = true
		}
	}
	if action == "-c" && exists {
		return errors.New("同名快照已存在")
	}
	if action != "-c" && !exists {
		return errors.New("快照不存在")
	}
	_, err = m.image(dir, "snapshot", action, name, DiskPath(m.Root, machine))
	return err
}

type tailWriter struct {
	mu   sync.Mutex
	data []byte
	log  io.Writer
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data = append(w.data, p...)
	if len(w.data) > 8192 {
		w.data = bytes.Clone(w.data[len(w.data)-8192:])
	}
	if w.log != nil {
		_, _ = w.log.Write(p)
	}
	return len(p), nil
}
func (w *tailWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return string(w.data) }
