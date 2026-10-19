package vm

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

type Snapshot struct {
	ID, Name    string
	DateSeconds int64
}

func (m *Manager) Snapshots(_ string, machine Machine) ([]Snapshot, error) {
	lock := m.operation(machine.ID)
	lock.Lock()
	defer lock.Unlock()
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
func (m *Manager) Snapshot(_ string, machine Machine, action, name string, progress func(int64, int64)) error {
	lock := m.operation(machine.ID)
	lock.Lock()
	defer lock.Unlock()
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
		if err = copyExclusive(disk, snapshot, progress); err != nil {
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
		if err = copyExclusive(snapshot, pending, progress); err != nil {
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
