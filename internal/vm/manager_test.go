package vm

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealQcow2Snapshots(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img is not installed")
	}
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.DiskGiB = 1
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	disk := DiskPath(manager.Root, machine)
	before, err := os.Stat(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.CreateDisk("", machine); err == nil {
		t.Fatal("must refuse to replace an existing disk")
	}
	after, err := os.Stat(disk)
	if err != nil || before.Size() != after.Size() {
		t.Fatal("existing disk changed")
	}
	if err = manager.Snapshot("", machine, "-c", "baseline"); err != nil {
		t.Fatal(err)
	}
	if err = manager.Snapshot("", machine, "-c", "baseline"); err == nil {
		t.Fatal("must reject duplicate snapshot")
	}
	snapshots, err := manager.Snapshots("", machine)
	if err != nil || len(snapshots) != 1 || snapshots[0].Name != "baseline" {
		t.Fatalf("snapshots=%v err=%v", snapshots, err)
	}
	// Reject names that can address a numeric ID in some QEMU versions.
	for _, action := range []string{"-c", "-a", "-d"} {
		if err = manager.Snapshot("", machine, action, "1"); err == nil {
			t.Fatal("must reject numeric snapshot names")
		}
	}
	if err = manager.Snapshot("", machine, "-a", "baseline"); err != nil {
		t.Fatal(err)
	}
	if err = manager.Snapshot("", machine, "-d", "baseline"); err != nil {
		t.Fatal(err)
	}
	snapshots, err = manager.Snapshots("", machine)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("snapshots=%v err=%v", snapshots, err)
	}
	if err = manager.Snapshot("", machine, "-a", "missing"); err == nil {
		t.Fatal("must reject missing snapshot")
	}
	if err = manager.Snapshot("", machine, "-c", "-unsafe"); err == nil {
		t.Fatal("must reject unsafe name")
	}
}

func TestQEMULifecycle(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 to open a real QEMU display")
	}
	for _, accel := range []string{"tcg", "whpx"} {
		t.Run(accel, func(t *testing.T) {
			manager := NewManager(t.TempDir(), io.Discard)
			machine := NewMachine()
			machine.Name = "TCG/WHPX 中文,测试"
			machine.DiskGiB = 1
			machine.MemoryMiB = 256
			machine.CPUs = 1
			machine.Accelerator = accel
			if err := manager.CreateDisk("", machine); err != nil {
				t.Fatal(err)
			}
			if err := manager.Start("", machine); err != nil {
				t.Fatal(err)
			}
			manager.mu.Lock()
			s := manager.sessions[machine.ID]
			manager.mu.Unlock()
			if s == nil {
				t.Fatal("QEMU exited after handshake")
			}
			defer func() {
				_ = s.cmd.Process.Kill()
				select {
				case <-s.done:
				case <-time.After(5 * time.Second):
					t.Error("QEMU did not exit")
				}
			}()
			requireVisibleQEMUWindow(t, s.cmd.Process.Pid)
			if err := manager.Start("", machine); err == nil {
				t.Fatal("must reject double start")
			}
			if err := manager.Snapshot("", machine, "-c", "busy"); err == nil {
				t.Fatal("must reject snapshot of running VM")
			}
			if err := manager.Control(machine.ID, "stop"); err != nil {
				t.Fatal(err)
			}
			if manager.State(machine.ID) != "已暂停" {
				t.Fatal("pause state missing")
			}
			if err := manager.Control(machine.ID, "cont"); err != nil {
				t.Fatal(err)
			}
			if manager.State(machine.ID) != "运行中" {
				t.Fatal("resume state missing")
			}
			// Windows QEMU does not reliably enforce image locks; the manager must reject every image operation.
			if _, err := manager.Snapshots("", machine); err == nil {
				t.Fatal("must reject reading snapshots of a running VM")
			}
			if err := manager.Control(machine.ID, "system_powerdown"); err != nil {
				t.Fatal(err)
			}
			if err := manager.Control(machine.ID, "kill"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-s.done:
			case <-time.After(5 * time.Second):
				t.Fatal("QEMU did not exit")
			}
			if manager.Active() {
				t.Fatal("stale session after exit")
			}
			if _, err := os.Stat(filepath.Join(manager.Root, "machines", machine.ID, "disk.qcow2")); err != nil {
				t.Fatal("disk lost after shutdown")
			}
		})
	}
}
