package vm

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"my-vm/internal/checkpoint"
)

func TestInterruptedRestoreRollsBackDiskAndFirmwareBeforeCleanup(t *testing.T) {
	for _, locked := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "firmware_locked"}[locked], func(t *testing.T) {
			manager := NewManager(t.TempDir(), io.Discard)
			machine := NewMachine()
			folder := filepath.Dir(DiskPath(manager.Root, machine))
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			storage, err := checkpoint.Load(folder)
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := checkpoint.NewEntry("point")
			entry.Disk = storage.Active
			storage.Entries = []checkpoint.Entry{entry}
			old := "disk-11111111111111111111111111111111.vhdx"
			child := "disk-22222222222222222222222222222222.vhdx"
			backup := "state-33333333333333333333333333333333.vmgs"
			storage.Layers[old], storage.Layers[child] = storage.Active, storage.Active
			storage.Active, storage.Current = child, entry.ID
			storage.Restoring = &checkpoint.Restore{Active: old, Firmware: true, Backup: backup}
			for _, file := range []string{entry.Disk, old, child, "guest.vmgs", backup} {
				if err = os.WriteFile(storage.Path(file), []byte(file), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = storage.Save(); err != nil {
				t.Fatal(err)
			}
			if locked {
				handle, openErr := windows.CreateFile(windows.StringToUTF16Ptr(storage.Path("guest.vmgs")), windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, err = manager.storage(machine)
				windows.CloseHandle(handle)
				if err == nil {
					t.Fatal("locked firmware accepted")
				}
				retained, loadErr := checkpoint.Load(folder)
				if loadErr != nil || retained.Restoring == nil || retained.Active != child {
					t.Fatal("failed rollback lost journal", loadErr)
				}
				if _, err = os.Stat(storage.Path(backup)); err != nil {
					t.Fatal("failed rollback removed only firmware backup", err)
				}
			}
			storage, err = manager.storage(machine)
			if err != nil {
				t.Fatal(err)
			}
			if storage.Active != old || storage.Restoring != nil {
				t.Fatal("interrupted restore selected wrong disk", storage)
			}
			data, err := os.ReadFile(storage.Path("guest.vmgs"))
			if err != nil || string(data) != backup {
				t.Fatal("original firmware not restored", string(data), err)
			}
			if _, err = os.Stat(storage.Path(old)); err != nil {
				t.Fatal("original disk lost", err)
			}
			for _, file := range []string{child, backup} {
				if _, err = os.Stat(storage.Path(file)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unreferenced recovery file not collected", file, err)
				}
			}
		})
	}
}

func TestFailedStateCaptureClearsUnstartedFork(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.Network = "none"
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	storage, err := manager.storage(machine)
	if err != nil {
		t.Fatal(err)
	}
	manager.sessions[machine.ID] = &session{machine: machine, disk: storage.Active, paused: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = manager.createSnapshot(ctx, machine, storage, "cancelled", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = manager.storage(machine); err != nil {
		t.Fatal("capture failure blocked subsequent online operations", err)
	}
	if storage.Pending != nil || len(storage.Entries) != 0 || len(storage.Garbage) != 0 {
		t.Fatal("unstarted fork retained", storage)
	}
}
