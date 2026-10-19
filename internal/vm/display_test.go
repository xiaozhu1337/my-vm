package vm

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
func TestDisplayModeSwitchPreservesConsole(t *testing.T) {
	for _, enhanced := range []bool{false, true} {
		t.Run(fmt.Sprint(enhanced), func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			command := exec.CommandContext(ctx, executable, "-test.run=^TestDisplayConsoleProcess$")
			command.Env = append(os.Environ(), "MY_VM_DISPLAY_HELPER=1")
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				input.Close()
				if err := command.Wait(); err != nil {
					t.Error("existing console was terminated:", err)
				}
			})
			manager := NewManager(t.TempDir(), io.Discard)
			machine := NewMachine()
			state := &session{console: command, consoleEnhanced: enhanced, machine: machine}
			manager.sessions[machine.ID] = state
			if err := manager.ShowDisplayMode(machine.ID, !enhanced); err == nil || !strings.Contains(err.Error(), "手动关闭") {
				t.Fatal("mode switch did not ask to preserve the current window:", err)
			}
			if state.console != command || state.consoleEnhanced != enhanced {
				t.Fatal("mode switch replaced the existing console")
			}
		})
	}
}

func TestDisplayConsoleProcess(t *testing.T) {
	if os.Getenv("MY_VM_DISPLAY_HELPER") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
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
