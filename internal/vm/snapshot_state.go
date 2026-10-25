package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"golang.org/x/sys/windows"
	"my-vm/internal/checkpoint"
	"my-vm/internal/hcs"
)

// copyState copies firmware while the VM is paused or stopped; writers must stay paused until it returns.
func copyState(source, target string, replace bool) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if info, statErr := os.Lstat(target); statErr == nil {
		if !replace {
			return os.ErrExist
		}
		if !info.Mode().IsRegular() {
			return errors.New("状态文件目标必须是普通文件")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".state-copy-")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	_, err = io.Copy(output, input)
	if err == nil {
		err = output.Sync()
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(output.Name())
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(from, to, flags)
}

func snapshotStatePath(storage *checkpoint.Store, entry checkpoint.Entry, extension string) string {
	return storage.Path("state-" + entry.ID + "." + extension)
}

func snapshotSessionEntry(s *session, entry *checkpoint.Entry) error {
	if s == nil {
		return nil
	}
	if s.machine.GPUShared {
		return errors.New("GPU 共享设备暂不支持完整状态快照；请关机后操作，或关闭 GPU 共享后启动")
	}
	entry.State = "running"
	if s.paused {
		entry.State = "paused"
	}
	entry.Adapters = append([]string(nil), s.adapterIDs...)
	for _, endpoint := range s.endpoints {
		mac, err := endpoint.MACAddress()
		if err != nil {
			return err
		}
		entry.MACs = append(entry.MACs, mac)
	}
	return nil
}

// captureSnapshotState leaves running VMs paused until the disk fork and manifest commit finish.
func (m *Manager) captureSnapshotState(ctx context.Context, machine Machine, storage *checkpoint.Store, entry checkpoint.Entry, s *session) (resume func() error, err error) {
	resume = func() error { return nil }
	if s != nil {
		machine = s.machine
		if !s.paused {
			if err = s.system.Action("HcsPauseComputeSystem"); err != nil {
				return resume, err
			}
			resume = func() error { return s.system.Action("HcsResumeComputeSystem") }
		}
		if err = ctx.Err(); err != nil {
			return resume, err
		}
		state := snapshotStatePath(storage, entry, "vmrs")
		if err = hcs.PrepareFile("my-vm-"+machine.ID, state, "HcsCreateEmptyRuntimeStateFile"); err != nil {
			return resume, err
		}
		if err = s.system.Save(state); err != nil {
			return resume, fmt.Errorf("保存内存和设备状态失败；未创建磁盘快照: %w", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return resume, err
	}
	guest := storage.Path("guest.vmgs")
	if _, err = os.Stat(guest); errors.Is(err, os.ErrNotExist) && s == nil {
		err = hcs.PrepareFile("my-vm-"+machine.ID, guest, "HcsCreateEmptyGuestStateFile")
	}
	if err != nil {
		return resume, err
	}
	if err = copyState(guest, snapshotStatePath(storage, entry, "vmgs"), false); err != nil {
		return resume, err
	}
	return resume, Save(snapshotStatePath(storage, entry, "yaml"), Config{Machines: []Machine{machine}})
}

func loadSnapshotMachine(machine Machine, storage *checkpoint.Store, entry checkpoint.Entry) (Machine, error) {
	if err := storage.CheckFiles(entry.StateFiles()...); err != nil {
		return machine, err
	}
	config, err := Load(snapshotStatePath(storage, entry, "yaml"))
	if err != nil {
		return machine, err
	}
	if len(config.Machines) != 1 || config.Machines[0].ID != machine.ID {
		return machine, errors.New("快照硬件配置不属于此虚拟机")
	}
	saved := config.Machines[0]
	if entry.State == "off" {
		return machine, nil
	}
	// Memory hot changes are part of the saved session, not the configured next-boot default.
	candidate := machine
	candidate.Name, candidate.MemoryMiB, candidate.MaxMemoryMiB = saved.Name, saved.MemoryMiB, saved.MaxMemoryMiB
	candidate.Shares, candidate.Clipboard = saved.Shares, saved.Clipboard
	candidate.DriverISO = saved.DriverISO // Guest tools may have been mounted only in the running session.
	if !reflect.DeepEqual(candidate, saved) {
		return machine, errors.New("硬件或挂载光盘配置与快照不一致，请恢复原配置后重试；原虚拟机保持不变")
	}
	if saved.MemoryMiB > machine.MemoryLimitMiB() {
		return machine, errors.New("快照内存超过当前最大内存，请先提高最大内存")
	}
	for _, path := range []string{saved.ISO, saved.DriverISO} {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return machine, fmt.Errorf("快照光盘不可用 %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return machine, fmt.Errorf("快照光盘不是普通文件: %s", path)
		}
	}
	machine.MemoryMiB, machine.DriverISO = saved.MemoryMiB, saved.DriverISO
	return machine, nil
}

func applySnapshotState(configuration map[string]any, storage *checkpoint.Store, entry checkpoint.Entry, endpointIDs []string) error {
	if entry.State != "running" && entry.State != "paused" {
		return errors.New("快照不包含可恢复的运行内存")
	}
	if len(entry.Adapters) != len(endpointIDs) {
		return errors.New("快照网卡数量与当前配置不一致")
	}
	adapters := map[string]any{}
	for i, id := range entry.Adapters {
		if _, err := windows.GUIDFromString("{" + id + "}"); err != nil {
			return errors.New("快照网卡标识无效")
		}
		if _, exists := adapters[id]; exists {
			return errors.New("快照网卡标识重复")
		}
		adapters[id] = map[string]any{"EndpointId": endpointIDs[i]}
	}
	virtual := configuration["VirtualMachine"].(map[string]any)
	devices := virtual["Devices"].(map[string]any)
	if len(adapters) > 0 {
		devices["NetworkAdapters"] = adapters
	}
	virtual["RestoreState"] = map[string]any{"SaveStateFilePath": snapshotStatePath(storage, entry, "vmrs")}
	return nil
}
