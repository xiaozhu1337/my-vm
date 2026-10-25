package vm

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
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

func TestLegacyRestoreKeepsRecoveryStateAndAllowsDiskClones(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.DiskGiB = 1
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	if err := manager.Snapshot("", machine, "-c", "point", nil); err != nil {
		t.Fatal(err)
	}
	storage, err := manager.storage(machine)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := checkpoint.NewEntry("legacy")
	entry.Disk, entry.State = storage.Active, "running"
	child := "disk-11111111111111111111111111111111.vhdx"
	storage.Layers[child] = storage.Entries[0].Disk
	storage.Restoring = &checkpoint.Restore{
		Active: storage.Active, Current: storage.Current, Firmware: true,
		Backup: "state-" + entry.ID + ".vmgs", Rollback: &entry,
	}
	storage.Active = child
	for _, file := range append(entry.StateFiles(), child, "guest.vmgs") {
		if err = os.WriteFile(storage.Path(file), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := yaml.Marshal(Config{Machines: []Machine{machine}})
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err = yaml.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["machines"].([]any)[0].(map[string]any)["dynamic_memory"] = true
	data, err = yaml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshotStatePath(storage, entry, "yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = storage.Save(); err != nil {
		t.Fatal(err)
	}
	if snapshots, listErr := manager.Snapshots("", machine); listErr != nil || len(snapshots) != 1 {
		t.Fatal("legacy RAM blocked disk inspection", snapshots, listErr)
	}
	if err = manager.Start("", machine); !errors.Is(err, errLegacySnapshotMemory) {
		t.Fatal("legacy RAM silently discarded or used for boot", err)
	}
	for _, source := range []string{CloneCurrent, storage.Entries[0].ID} {
		target := NewClone(machine, []Machine{machine})
		if err = manager.CloneFrom(machine, target, source, nil); err != nil {
			t.Fatal("legacy restore blocked disk clone", source, err)
		}
	}
	retained, err := checkpoint.Load(storage.Dir)
	if err != nil || retained.Restoring == nil || !retained.Restoring.Reverting || retained.Active != entry.Disk {
		t.Fatal("legacy recovery journal or original disk lost", retained, err)
	}
	for _, file := range append(entry.StateFiles(), child) {
		if _, err = os.Stat(storage.Path(file)); err != nil {
			t.Fatal("legacy recovery file removed", file, err)
		}
	}
	data, err = os.ReadFile(storage.Path("guest.vmgs"))
	if err != nil || string(data) != "state-"+entry.ID+".vmgs" {
		t.Fatal("original firmware not restored", string(data), err)
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
