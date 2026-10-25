package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"my-vm/internal/checkpoint"
	"my-vm/internal/hcs"
)

type Snapshot struct {
	ID, Name             string
	DateSeconds          int64
	ParentID, ParentName string
	Depth                int
	Current, Legacy      bool
	State                string
}

func (m *Manager) Snapshots(_ string, machine Machine) ([]Snapshot, error) {
	lock := m.operation(machine.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := requireHCS(machine); err != nil {
		return nil, err
	}
	storage, err := m.snapshotSource(machine)
	if err != nil {
		return nil, err
	}
	result := make([]Snapshot, 0, len(storage.Entries))
	names, depths := map[string]string{}, map[string]int{}
	for _, e := range storage.Tree() {
		depth := 0
		if e.Parent != "" {
			depth = depths[e.Parent] + 1
		}
		result = append(result, Snapshot{ID: e.ID, Name: e.Name, DateSeconds: e.Created, ParentID: e.Parent, ParentName: names[e.Parent], Depth: depth, Current: storage.Current == e.ID, Legacy: e.Legacy, State: e.State})
		names[e.ID], depths[e.ID] = e.Name, depth
	}
	return result, nil
}

// snapshotSource permits disk inspection after a legacy RAM rollback cannot be replayed.
func (m *Manager) snapshotSource(machine Machine) (*checkpoint.Store, error) {
	storage, err := m.storage(machine)
	if !errors.Is(err, errLegacySnapshotMemory) {
		return storage, err
	}
	storage, loadErr := checkpoint.Load(filepath.Dir(DiskPath(m.Root, machine)))
	if loadErr != nil {
		return nil, errors.Join(err, loadErr)
	}
	r := storage.Restoring
	if r == nil || !r.Reverting || storage.Active != r.Active || storage.Current != r.Current {
		return nil, err
	}
	return storage, nil
}

// storage reconciles an interrupted native backing-file switch before any disk operation.
func (m *Manager) storage(machine Machine) (*checkpoint.Store, error) {
	return m.loadStorage(machine, false)
}

func (m *Manager) loadStorage(machine Machine, restoring bool) (*checkpoint.Store, error) {
	storage, err := checkpoint.Load(filepath.Dir(DiskPath(m.Root, machine)))
	if err != nil {
		return nil, err
	}
	if storage.Restoring != nil && !restoring {
		m.mu.Lock()
		s := m.sessions[machine.ID]
		m.mu.Unlock()
		if s != nil && s.disk == storage.Active {
			err = m.finishSnapshotRestore(storage)
		} else if s != nil {
			err = errors.New("快照恢复尚未完成，请关机后重试以回退磁盘和固件")
		} else {
			err = m.rollbackSnapshotRestore(storage, machine)
		}
		if err != nil {
			return nil, err
		}
	}
	if storage.Pending == nil {
		m.collect(storage)
		return storage, nil
	}
	pending := storage.Pending
	m.mu.Lock()
	s := m.sessions[machine.ID]
	m.mu.Unlock()
	if s != nil {
		if s.disk != pending.Child {
			return nil, errors.New("上次在线快照的结果尚未确认；虚拟机继续运行，请关机后重新读取快照以核对磁盘链")
		}
		if err = storage.CheckFiles(pending.Entry.StateFiles()...); err != nil {
			return nil, err
		}
		storage.CommitFork()
	} else {
		if err = storage.CheckFiles(pending.Child); errors.Is(err, os.ErrNotExist) {
			storage.Pending = nil
			storage.Retire(append(pending.Entry.StateFiles(), pending.Child)...)
		} else if err != nil {
			return nil, err
		} else {
			parent, parentErr := hcs.DiskParent(storage.Path(pending.Child))
			if parentErr != nil {
				return nil, fmt.Errorf("核对未完成快照失败（磁盘全部保留）: %w", parentErr)
			}
			if parent == "" {
				storage.Pending = nil
				storage.Retire(append(pending.Entry.StateFiles(), pending.Child)...)
			} else if strings.EqualFold(filepath.Clean(parent), filepath.Clean(storage.Path(pending.Entry.Disk))) {
				if err = storage.CheckFiles(pending.Entry.StateFiles()...); err != nil {
					return nil, err
				}
				storage.CommitFork()
			} else {
				return nil, errors.New("未完成快照的父盘不匹配，已保留所有磁盘并阻止启动")
			}
		}
	}
	if err = storage.Save(); err != nil {
		return nil, err
	}
	m.collect(storage)
	return storage, nil
}

func (m *Manager) collect(storage *checkpoint.Store) {
	if len(storage.Garbage) == 0 {
		return
	}
	if err := storage.Collect(); err != nil && m.Log != nil {
		fmt.Fprintln(m.Log, "快照文件清理稍后重试:", err)
	}
}

func (m *Manager) Snapshot(_ string, machine Machine, action, name string, progress func(int64, int64)) error {
	return m.SnapshotContext(context.Background(), machine, action, name, progress)
}

func (m *Manager) SnapshotContext(ctx context.Context, machine Machine, action, name string, progress func(int64, int64)) error {
	lock := m.operation(machine.ID)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for !lock.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireHCS(machine); err != nil {
		return err
	}
	if !validSnapshot.MatchString(name) {
		return errors.New("快照名称需为 1 到 64 个英文字母、数字、下划线或连字符，并以英文字母开头")
	}
	if progress != nil {
		progress(0, 100)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	storage, err := m.storage(machine)
	if err != nil {
		return err
	}
	entry, exists := storage.Find(name)
	switch action {
	case "-c":
		if exists {
			return errors.New("同名快照已存在")
		}
		if len(storage.Entries) >= 16 {
			return errors.New("每台虚拟机最多保留 16 个快照，请先删除不需要的快照")
		}
		return m.createSnapshot(ctx, machine, storage, name, progress)
	case "-a":
		if !exists {
			return errors.New("快照不存在")
		}
		return m.restoreSnapshot(ctx, machine, storage, entry, progress)
	case "-d":
		if !exists {
			return errors.New("快照不存在")
		}
		storage.Remove(entry.ID)
		storage.Retire(entry.StateFiles()...)
		if err = storage.Save(); err != nil {
			return err
		}
		m.collect(storage)
		if progress != nil {
			progress(100, 100)
		}
		return nil
	default:
		return errors.New("快照操作无效")
	}
}

func (m *Manager) createSnapshot(ctx context.Context, machine Machine, storage *checkpoint.Store, name string, progress func(int64, int64)) (err error) {
	if err := storage.CheckFork(storage.Active); err != nil {
		return err
	}
	if err := storage.CheckFiles(storage.Chain()...); err != nil {
		return err
	}
	entry, err := checkpoint.NewEntry(name)
	if err != nil {
		return err
	}
	entry.Disk, entry.Parent, entry.State = storage.Active, storage.Current, "off"
	m.mu.Lock()
	s := m.sessions[machine.ID]
	m.mu.Unlock()
	if err = snapshotSessionEntry(s, &entry); err != nil {
		return err
	}
	id, err := checkpoint.NewID()
	if err != nil {
		return err
	}
	child := "disk-" + id + ".vhdx"
	storage.Layers[child] = storage.Active
	storage.Pending = &checkpoint.Fork{Entry: entry, Child: child}
	if err = storage.Save(); err != nil {
		return err
	}
	resume, captureErr := m.captureSnapshotState(ctx, machine, storage, entry, s)
	defer func() {
		if resumeErr := resume(); resumeErr != nil {
			m.mu.Lock()
			if s != nil {
				s.paused = true
			}
			m.mu.Unlock()
			err = errors.Join(err, fmt.Errorf("快照操作后继续运行失败，请手动继续虚拟机: %w", resumeErr))
		}
	}()
	if captureErr != nil {
		storage.Pending = nil
		storage.Retire(append(entry.StateFiles(), child)...)
		if cleanupErr := storage.Save(); cleanupErr != nil {
			return errors.Join(captureErr, cleanupErr)
		}
		m.collect(storage)
		return captureErr
	}
	if s != nil {
		err = hcs.ForkDisk(ctx, storage.Path(storage.Active), storage.Path(child), "my-vm-"+machine.ID, func(done, total int64) {
			if progress != nil && total > 0 {
				progress(5+done*85/total, 100)
			}
		})
	} else if err = ctx.Err(); err == nil {
		if progress != nil {
			progress(20, 100)
		}
		if err = ctx.Err(); err == nil {
			err = hcs.CreateDifferencingDisk(storage.Path(child), storage.Path(storage.Active))
		}
	}
	if err != nil {
		// A native operation may already have activated the child. Never delete an uncertain target.
		return fmt.Errorf("创建快照未完成，磁盘和恢复记录已保留: %w", err)
	}
	if s != nil {
		m.mu.Lock()
		s.disk = child
		m.mu.Unlock()
	}
	storage.CommitFork()
	if err = storage.Save(); err != nil {
		return fmt.Errorf("磁盘已分叉，保存清单失败；虚拟机及磁盘保留，可重试读取快照: %w", err)
	}
	if progress != nil {
		progress(100, 100)
	}
	return nil
}

func (m *Manager) restoreSnapshot(ctx context.Context, machine Machine, storage *checkpoint.Store, entry checkpoint.Entry, progress func(int64, int64)) error {
	if err := storage.CheckFork(entry.Disk); err != nil {
		return err
	}
	if err := storage.CheckFiles(entry.Disk); err != nil {
		return err
	}
	if entry.State != "" {
		if _, err := loadSnapshotMachine(machine, storage, entry); err != nil {
			return err
		}
	}
	id, err := checkpoint.NewID()
	if err != nil {
		return err
	}
	child := "disk-" + id + ".vhdx"
	path := storage.Path(child)
	if err = ctx.Err(); err != nil {
		return err
	}
	// Journal unused artifacts before creating them, so a crash cannot orphan them.
	backupName := "state-" + id + ".vmgs"
	storage.Retire(child, backupName, "state-"+id+".vmrs", "state-"+id+".yaml")
	if err = storage.Save(); err != nil {
		return err
	}
	if err = hcs.CreateDifferencingDisk(path, storage.Path(entry.Disk)); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if progress != nil {
		progress(25, 100)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	previous := m.sessions[machine.ID]
	m.mu.Unlock()
	guest := storage.Path("guest.vmgs")
	restore := &checkpoint.Restore{Active: storage.Active, Current: storage.Current, Firmware: entry.State != ""}
	resume := func() error { return nil }
	if previous != nil {
		rollback := checkpoint.Entry{ID: id, Name: "rollback", Disk: storage.Active}
		if err = snapshotSessionEntry(previous, &rollback); err != nil {
			return err
		}
		resume, err = m.captureSnapshotState(ctx, previous.machine, storage, rollback, previous)
		if err != nil {
			return errors.Join(err, resume())
		}
		restore.Rollback, restore.Firmware, restore.Backup = &rollback, true, backupName
	} else if restore.Firmware {
		if err = copyState(guest, storage.Path(backupName), false); err == nil {
			restore.Backup = backupName
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	storage.Restoring = restore
	storage.Active, storage.Current = child, entry.ID
	storage.Layers[child] = entry.Disk
	storage.Garbage = slices.DeleteFunc(storage.Garbage, func(name string) bool {
		return name == child || name == backupName || (restore.Rollback != nil && slices.Contains(restore.Rollback.StateFiles(), name))
	})
	if err = storage.Save(); err != nil {
		return errors.Join(err, resume())
	}
	committed = true
	if previous != nil {
		if err = m.stopSnapshotSession(machine.ID, previous); err != nil {
			storage.Active, storage.Current, storage.Restoring = restore.Active, restore.Current, nil
			storage.Retire(restore.Rollback.StateFiles()...)
			err = errors.Join(err, storage.Save(), resume())
			return err
		}
	}
	if progress != nil {
		progress(65, 100)
	}
	var startErr error
	switch entry.State {
	case "running", "paused":
		startErr = m.startWithState(machine, &entry)
	case "off":
		startErr = copyState(snapshotStatePath(storage, entry, "vmgs"), guest, true)
	default:
		if previous != nil {
			startErr = m.restartSnapshotSession(previous)
		}
	}
	if startErr != nil {
		// Preserve the journal and firmware backup until rollback has durably completed.
		retry := m.rollbackSnapshotRestore(storage, machine)
		return errors.Join(fmt.Errorf("快照恢复失败，原磁盘和固件已保留: %w", startErr), retry)
	}
	if err = m.finishSnapshotRestore(storage); err != nil {
		return fmt.Errorf("快照已恢复，保存完成记录失败；原磁盘及固件仍保留: %w", err)
	}
	if progress != nil {
		progress(100, 100)
	}
	return nil
}

func (m *Manager) restartSnapshotSession(previous *session) error {
	if err := m.startSession(previous.machine, nil, true); err != nil {
		return err
	}
	return m.restoreSnapshotSessionState(previous)
}

func (m *Manager) restoreSnapshotSessionState(previous *session) error {
	if !previous.paused {
		return nil
	}
	m.mu.Lock()
	restored := m.sessions[previous.machine.ID]
	m.mu.Unlock()
	if err := restored.system.Action("HcsPauseComputeSystem"); err != nil {
		return fmt.Errorf("恢复虚拟机暂停状态失败: %w", err)
	}
	m.mu.Lock()
	restored.paused = true
	m.mu.Unlock()
	return nil
}

// The caller owns operation(id); the normal watcher will see the replaced session and skip it.
func (m *Manager) stopSnapshotSession(id string, s *session) error {
	if err := s.system.Action("HcsTerminateComputeSystem"); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	if s.shares != nil {
		_ = s.shares.Close()
	}
	s.system.Close()
	if err := closeNetworkEndpoints(s.endpoints, (*hcs.Endpoint).Close); err != nil && m.Log != nil {
		fmt.Fprintln(m.Log, err)
	}
	return nil
}
