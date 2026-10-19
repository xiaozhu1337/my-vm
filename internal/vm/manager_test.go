package vm

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestVHDXSnapshots(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.DiskGiB = 1
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	disk := DiskPath(manager.Root, machine)
	before, err := os.ReadFile(disk)
	if err != nil {
		t.Fatal(err)
	}
	if string(before[:8]) != "vhdxfile" {
		t.Fatal("not VHDX")
	}
	if err = manager.CreateDisk("", machine); err == nil {
		t.Fatal("existing disk overwritten")
	}
	if err = manager.Snapshot("", machine, "-c", "baseline", nil); err != nil {
		t.Fatal(err)
	}
	if err = manager.Snapshot("", machine, "-c", "baseline", nil); err == nil {
		t.Fatal("duplicate snapshot accepted")
	}
	list, err := manager.Snapshots("", machine)
	if err != nil || len(list) != 1 || list[0].Name != "baseline" {
		t.Fatal(list, err)
	}
	for _, name := range []string{"1", "../outside", "-unsafe"} {
		if err = manager.Snapshot("", machine, "-c", name, nil); err == nil {
			t.Fatal("unsafe name accepted")
		}
	}
	if err = os.WriteFile(disk, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = manager.Snapshot("", machine, "-a", "baseline", nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(disk)
	if err != nil || string(before) != string(after) {
		t.Fatal("restore changed original disk contents", err)
	}
	if err = manager.Snapshot("", machine, "-d", "baseline", nil); err != nil {
		t.Fatal(err)
	}
	if list, err = manager.Snapshots("", machine); err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	if err = manager.Snapshot("", machine, "-a", "missing", nil); err == nil {
		t.Fatal("missing snapshot accepted")
	}
}
func TestSnapshotRejectsExternalWriter(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "disk.vhdx"), filepath.Join(root, "copy.vhdx")
	if err := os.WriteFile(source, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(source)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := copyExclusive(source, target, nil); err == nil {
		t.Fatal("disk with an external writer was copied")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("failed copy left an output", err)
	}
}

func TestHCSLifecycle(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 for real HCS lifecycle")
	}
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.DiskGiB = 1
	machine.MemoryMiB = 512
	machine.Shares = []Share{{Name: "Probe", Path: t.TempDir(), ReadOnly: true}}
	machine.CPUs = 1
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start("", machine); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if manager.Active() {
			_ = manager.Control(machine.ID, "kill")
		}
	}()
	if err := manager.Start("", machine); err == nil {
		t.Fatal("double start accepted")
	}
	for _, command := range []string{"stop", "cont"} {
		if err := manager.Control(machine.ID, command); err != nil {
			t.Fatal(err)
		}
		want := "运行中"
		if command == "stop" {
			want = "已暂停"
		}
		if manager.State(machine.ID) != want {
			t.Fatal("state not updated")
		}
	}
	if _, err := manager.Snapshots("", machine); err == nil {
		t.Fatal("running snapshot access accepted")
	}
	if err := manager.Snapshot("", machine, "-c", "busy", nil); err == nil {
		t.Fatal("running snapshot accepted")
	}
	if err := manager.Control(machine.ID, "kill"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for manager.Active() && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if manager.Active() {
		t.Fatal("stale running state")
	}
	if _, err := os.Stat(filepath.Join(manager.Root, "machines", machine.ID, "disk.vhdx")); err != nil {
		t.Fatal("disk lost", err)
	}
}
