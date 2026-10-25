package vm

import (
	"errors"
	"fmt"
	"os"

	"my-vm/internal/checkpoint"
)

// An interrupted restore rolls back before any cold boot or garbage collection.
func (m *Manager) rollbackSnapshotRestore(storage *checkpoint.Store, machine Machine) error {
	r := storage.Restoring
	if r == nil {
		return nil
	}
	if err := storage.CheckFiles(r.Active); err != nil {
		return fmt.Errorf("回退原磁盘失败，恢复记录及固件备份保留: %w", err)
	}
	if r.Firmware {
		guest := storage.Path("guest.vmgs")
		if r.Backup != "" {
			if err := storage.CheckFiles(r.Backup); err != nil {
				return fmt.Errorf("回退固件失败，恢复记录保留，备份 %s: %w", storage.Path(r.Backup), err)
			}
			if err := copyState(storage.Path(r.Backup), guest, true); err != nil {
				return fmt.Errorf("回退固件失败，恢复记录保留，备份 %s: %w", storage.Path(r.Backup), err)
			}
		} else if err := os.Remove(guest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("回退固件失败，恢复记录保留: %w", err)
		}
	}
	storage.Active, storage.Current = r.Active, r.Current
	r.Reverting = true
	if err := storage.Save(); err != nil {
		return fmt.Errorf("保存回退记录失败，原磁盘及固件备份保留: %w", err)
	}
	if r.Rollback != nil {
		if err := m.startSession(machine, r.Rollback, true); err != nil {
			return fmt.Errorf("回退原运行现场失败，恢复记录及 RAM 保留，可重试启动: %w", err)
		}
	}
	return m.finishSnapshotRestore(storage)
}

func (m *Manager) finishSnapshotRestore(storage *checkpoint.Store) error {
	r := storage.Restoring
	if r == nil {
		return nil
	}
	storage.Restoring = nil
	files := []string{r.Backup}
	if r.Rollback != nil {
		files = append(files, r.Rollback.StateFiles()...)
	}
	storage.Retire(files...)
	if err := storage.Save(); err != nil {
		return err
	}
	m.collect(storage)
	return nil
}
