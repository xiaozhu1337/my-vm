//go:build windows

package vm

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"my-vm/internal/media"
)

func TestGuestMediaValidation(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	for _, path := range []string{"relative.iso", filepath.Join(t.TempDir(), "missing.iso"), t.TempDir()} {
		if err := manager.MountGuestMedia("missing", path); err == nil {
			t.Fatalf("invalid media accepted: %q", path)
		}
	}
}

func TestHCSGuestMedia(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 for real HCS optical insertion")
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "README.txt"), []byte("media probe"), 0600); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), "guest.iso")
	if err := media.CreateISO(source, image); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.DiskGiB, machine.MemoryMiB, machine.CPUs, machine.Network = 1, 512, 1, "none"
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start("", machine); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Control(machine.ID, "kill"); err != nil {
			t.Error(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for manager.Active() && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if manager.Active() {
			t.Error("media probe VM did not stop")
		}
	}()
	for attempt := range 2 {
		if err := manager.MountGuestMedia(machine.ID, image); err != nil {
			t.Fatalf("insert %d: %v", attempt, err)
		}
	}
	if err := manager.Control(machine.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := manager.MountGuestMedia(machine.ID, image); err == nil {
		t.Fatal("paused VM accepted media")
	}
	if err := manager.Control(machine.ID, "cont"); err != nil {
		t.Fatal(err)
	}
}
