package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// Clone copies only offline persistent state. Runtime state and snapshots belong
// to the original machine; guest OS identities must be prepared before networking.
func (m *Manager) Clone(source, target Machine, progress func(int64, int64)) (err error) {
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
	if err = copyExclusive(DiskPath(m.Root, source), DiskPath(m.Root, target), progress); err != nil {
		return err
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
