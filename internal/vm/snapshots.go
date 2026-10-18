package vm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

type Snapshot struct {
	ID, Name    string
	DateSeconds int64
}

func (m *Manager) Snapshots(_ string, machine Machine) ([]Snapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.snapshots(machine)
}
func (m *Manager) snapshots(machine Machine) ([]Snapshot, error) {
	if err := requireHCS(machine); err != nil {
		return nil, err
	}
	if m.State(machine.ID) != "已关机" {
		return nil, errors.New("请先关闭虚拟机，再管理磁盘快照")
	}
	root := filepath.Join(filepath.Dir(DiskPath(m.Root, machine)), "snapshots")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := []Snapshot{}
	for _, entry := range entries {
		if !entry.IsDir() || !validSnapshot.MatchString(entry.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(root, entry.Name(), "disk.vhdx"))
		if err != nil {
			return nil, err
		}
		result = append(result, Snapshot{ID: entry.Name(), Name: entry.Name(), DateSeconds: info.ModTime().Unix()})
	}
	return result, nil
}

// A full offline copy keeps the format independent of VM registration and firmware.
func (m *Manager) Snapshot(_ string, machine Machine, action, name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !validSnapshot.MatchString(name) {
		return errors.New("快照名称需为 1 到 64 个英文字母、数字、下划线或连字符，并以英文字母开头")
	}
	list, err := m.snapshots(machine)
	if err != nil {
		return err
	}
	exists := false
	for _, s := range list {
		if s.Name == name {
			exists = true
		}
	}
	disk := DiskPath(m.Root, machine)
	folder := filepath.Join(filepath.Dir(disk), "snapshots", name)
	snapshot := filepath.Join(folder, "disk.vhdx")
	switch action {
	case "-c":
		if exists {
			return errors.New("同名快照已存在")
		}
		if len(list) >= 16 {
			return errors.New("每台虚拟机最多保留 16 个离线快照，请先删除不需要的快照")
		}
		if err = os.MkdirAll(filepath.Dir(folder), 0700); err != nil {
			return err
		}
		if err = os.Mkdir(folder, 0700); err != nil {
			return err
		}
		if err = copyExclusive(disk, snapshot); err != nil {
			_ = os.Remove(folder)
			return err
		}
		now := time.Now()
		return os.Chtimes(snapshot, now, now)
	case "-a":
		if !exists {
			return errors.New("快照不存在")
		}
		pending := disk + ".restore"
		if err = copyExclusive(snapshot, pending); err != nil {
			return err
		}
		if err = os.Rename(pending, disk); err != nil {
			_ = os.Remove(pending)
			return err
		}
		return nil
	case "-d":
		if !exists {
			return errors.New("快照不存在")
		}
		if err = os.Remove(snapshot); err != nil {
			return err
		}
		return os.Remove(folder)
	default:
		return errors.New("快照操作无效")
	}
}
func copyExclusive(source, target string) error {
	path, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	// Deny writers while copying, including disks attached by another manager.
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return err
	}
	input := os.NewFile(uintptr(handle), source)
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("复制磁盘失败（原磁盘保留）: %w", err)
	}
	return nil
}
