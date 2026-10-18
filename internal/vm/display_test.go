package vm

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDisplay(t *testing.T) {
	machine := NewMachine()
	if machine.Display != "std" {
		t.Fatal("new machines should use VGA until their guest drivers are installed")
	}
	machine.Display = ""
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	legacy := "machines:\n  - id: " + machine.ID + "\n    name: legacy\n    cpus: 2\n    memory_mib: 1024\n    disk_gib: 16\n    network: nat\n    accelerator: whpx\n"
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Machines[0].Display != "" || cfg.Machines[0].DriverISO != "" {
		t.Fatalf("legacy configuration: %#v %v", cfg, err)
	}
	for _, mode := range []string{"", "std"} {
		machine.Display = mode
		args, err := Arguments(t.TempDir(), machine, "127.0.0.1:12345")
		if err != nil || !strings.Contains(strings.Join(args, "|"), "-vga|std") || strings.Contains(strings.Join(args, "|"), "virtio") {
			t.Fatalf("legacy display changed: %v %v", args, err)
		}
	}
}

func TestStartRejectsInvalidDriverISO(t *testing.T) {
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
		if err := manager.Start("", machine); err == nil || !strings.Contains(err.Error(), "驱动光盘") {
			t.Fatalf("invalid driver ISO %q: %v", path, err)
		}
	}
}
