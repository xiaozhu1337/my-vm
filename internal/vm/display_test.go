package vm

import (
	"io"
	"my-vm/internal/sharing"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestStatusBindsCurrentSessionWithoutShares(t *testing.T) {
	for _, osName := range []string{"windows10", "ubuntu26"} {
		manager := NewManager(t.TempDir(), io.Discard)
		machine := NewMachine()
		machine.GuestOS = osName
		feed := sharing.NewStatusFeed()
		manager.sessions[machine.ID] = &session{machine: machine, runtimeID: "runtime-a", shares: &sharing.Service{Status: feed}}
		if got, err := manager.GuestStatus(machine.ID, "{RUNTIME-A}"); err != nil || got != feed {
			t.Fatal(got, err)
		}
		if _, err := manager.GuestStatus(machine.ID, "runtime-b"); err == nil {
			t.Fatal("wrong runtime accepted")
		}
		if _, err := manager.GuestStatus("other", "runtime-a"); err == nil {
			t.Fatal("wrong machine accepted")
		}
		feed.Close()
		newFeed := sharing.NewStatusFeed()
		manager.sessions[machine.ID] = &session{machine: machine, runtimeID: "runtime-a", shares: &sharing.Service{Status: newFeed}}
		got, err := manager.GuestStatus(machine.ID, "runtime-a")
		if err != nil || got == feed || got != newFeed {
			t.Fatal("restarted VM inherited old status", err)
		}
		newFeed.Close()
		delete(manager.sessions, machine.ID)
		if _, err = manager.GuestStatus(machine.ID, "runtime-a"); err == nil {
			t.Fatal("stopped VM accepted")
		}
	}
}

func TestLegacyLibraryPreservesDisks(t *testing.T) {
	m := NewMachine()
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	legacy := "machines:\n  - id: " + m.ID + "\n    name: legacy\n    cpus: 2\n    memory_mib: 1024\n    disk_gib: 16\n    network: nat\n    accelerator: whpx\n"
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	old := cfg.Machines[0]
	if old.Backend != "" || !strings.HasSuffix(DiskPath(t.TempDir(), old), "disk.qcow2") {
		t.Fatal("legacy machine silently migrated")
	}
	manager := NewManager(t.TempDir(), io.Discard)
	disk := DiskPath(manager.Root, old)
	if err = os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(disk, []byte("preserve qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = manager.Start("", old); err == nil {
		t.Fatal("legacy QEMU started")
	}
	data, err := os.ReadFile(disk)
	if err != nil || string(data) != "preserve qcow2" {
		t.Fatal("legacy disk changed", err)
	}
}
func TestStartRejectsInvalidAdditionalISO(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	disk := DiskPath(manager.Root, machine)
	if err := os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative.iso", filepath.Join(manager.Root, "missing.iso"), manager.Root} {
		machine.DriverISO = path
		if err := manager.Start("", machine); err == nil {
			t.Fatalf("invalid ISO accepted: %q", path)
		}
	}
}
