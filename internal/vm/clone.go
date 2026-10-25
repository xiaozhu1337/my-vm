package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"my-vm/internal/checkpoint"
	"my-vm/internal/hcs"
)

func NewClone(source Machine, machines []Machine) Machine {
	clone := source
	clone.ID = NewMachine().ID
	clone.Network, clone.SwitchID = "none", ""
	clone.NetworkAdapters, clone.COMPorts = nil, nil
	clone.ISO, clone.DriverISO, clone.Shares = "", "", nil
	for number := 1; ; number++ {
		suffix := " - 副本"
		if number > 1 {
			suffix = fmt.Sprintf(" - 副本 %d", number)
		}
		base := []rune(strings.TrimSpace(source.Name))
		clone.Name = string(base[:min(len(base), 80-len([]rune(suffix)))]) + suffix
		found := false
		for _, existing := range machines {
			if strings.EqualFold(existing.Name, clone.Name) {
				found = true
				break
			}
		}
		if !found {
			return clone
		}
	}
}

const CloneCurrent = "@current"

// Clone retains the default for machines without snapshots; otherwise selection is required.
func (m *Manager) Clone(source, target Machine, progress func(int64, int64)) error {
	return m.CloneFrom(source, target, "", progress)
}

// CloneFrom materializes one selected disk state without changing the source branch.
// A clone boots independently; saved RAM remains bound to the source VM's device identities.
func (m *Manager) CloneFrom(source, target Machine, snapshot string, progress func(int64, int64)) (err error) {
	if err = requireHCS(source); err != nil {
		return err
	}
	if err = requireHCS(target); err != nil {
		return err
	}
	if source.ID == target.ID {
		return errors.New("副本必须使用新的虚拟机 ID")
	}
	first, second := source.ID, target.ID
	if first > second {
		first, second = second, first
	}
	lock1, lock2 := m.operation(first), m.operation(second)
	lock1.Lock()
	defer lock1.Unlock()
	lock2.Lock()
	defer lock2.Unlock()
	if m.State(source.ID) != "已关机" || m.State(target.ID) != "已关机" {
		return errors.New("请先关闭虚拟机，再复制磁盘")
	}
	storage, err := m.storage(source)
	if err != nil {
		return err
	}
	if snapshot == "" && len(storage.Entries) > 0 {
		return errors.New("此虚拟机存在快照，请指定复制来源：@current（当前工作状态）或快照名称/ID")
	}
	disk := storage.Active
	var selected *checkpoint.Entry
	if snapshot != "" && snapshot != CloneCurrent {
		entry, found := storage.Find(snapshot)
		for _, candidate := range storage.Entries {
			if candidate.ID == snapshot {
				entry, found = candidate, true
				break
			}
		}
		if !found {
			return errors.New("指定的复制来源快照不存在")
		}
		disk, selected = entry.Disk, &entry
	}
	var chain []string
	for path := disk; path != ""; path = storage.Layers[path] {
		chain = append(chain, path)
	}
	if err = storage.CheckFiles(chain...); err != nil {
		return err
	}
	folder := filepath.Dir(DiskPath(m.Root, target))
	if err = os.MkdirAll(filepath.Dir(folder), 0700); err != nil {
		return err
	}
	if err = os.Mkdir(folder, 0700); err != nil {
		return fmt.Errorf("副本目录已存在或无法创建（不会覆盖）: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		for _, path := range []string{DiskPath(m.Root, target), filepath.Join(folder, "guest.vmgs"), folder} {
			if cleanup := os.Remove(path); cleanup != nil && !errors.Is(cleanup, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("清理未完成的副本 %s: %w", path, cleanup))
			}
		}
	}()
	if len(chain) > 1 {
		err = hcs.ExportDisk(context.Background(), DiskPath(m.Root, target), storage.Path(disk), progress)
	} else {
		err = copyExclusive(storage.Path(disk), DiskPath(m.Root, target), progress)
	}
	if err != nil {
		return err
	}
	if snapshot != "" && snapshot != CloneCurrent {
		if selected.State != "" {
			file := "state-" + selected.ID + ".vmgs"
			if err = storage.CheckFiles(file); err != nil {
				return err
			}
			if err = copyExclusive(storage.Path(file), filepath.Join(folder, "guest.vmgs"), nil); err != nil {
				return err
			}
		}
		return hcs.ResetDiskIdentity(DiskPath(m.Root, target))
	}
	guestState := filepath.Join(filepath.Dir(DiskPath(m.Root, source)), "guest.vmgs")
	if _, err = os.Stat(guestState); err == nil {
		err = copyExclusive(guestState, filepath.Join(folder, "guest.vmgs"), nil)
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return err
	}
	return hcs.ResetDiskIdentity(DiskPath(m.Root, target))
}
