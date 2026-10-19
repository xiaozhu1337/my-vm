package vm

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstalledVMClones(t *testing.T) {
	if os.Getenv("MY_VM_REUSE") != "1" {
		t.Skip("set MY_VM_REUSE=1 to copy existing offline installations without reinstalling")
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(configDir, "my-vm")
	cfg, err := Load(filepath.Join(root, "library.yaml"))
	if err != nil || len(cfg.Machines) == 0 {
		t.Fatal("existing machine library required", err)
	}
	for _, source := range cfg.Machines {
		if source.Backend != "hcs" {
			continue
		}
		t.Run(source.Name, func(t *testing.T) {
			// Hard links let the manager read existing files from an isolated library.
			manager := NewManager(t.TempDir(), io.Discard)
			folder := filepath.Dir(DiskPath(manager.Root, source))
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"disk.vhdx", "guest.vmgs"} {
				if err := os.Link(filepath.Join(filepath.Dir(DiskPath(root, source)), name), filepath.Join(folder, name)); err != nil {
					t.Fatal("existing offline installation and same-volume temp directory required", err)
				}
			}
			path := DiskPath(root, source)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			originalID := diskIdentity(t, path)
			target := NewClone(source, cfg.Machines)
			var copied, total int64
			if err := manager.Clone(source, target, func(done, size int64) { copied, total = done, size }); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || diskIdentity(t, path) != originalID {
				t.Fatal("original disk metadata changed", err)
			}
			if diskIdentity(t, DiskPath(manager.Root, target)) == originalID || total != before.Size() || copied != total {
				t.Fatal("clone identity or copy progress invalid", copied, total)
			}
			originalState, err := os.ReadFile(filepath.Join(folder, "guest.vmgs"))
			if err != nil {
				t.Fatal(err)
			}
			clonedState, err := os.ReadFile(filepath.Join(filepath.Dir(DiskPath(manager.Root, target)), "guest.vmgs"))
			if err != nil || !bytes.Equal(originalState, clonedState) {
				t.Fatal("installed firmware state was not preserved", err)
			}
			if os.Getenv("MY_VM_INTEGRATION") == "1" {
				verifyInstalledCloneStart(t, manager, target)
			}
			t.Logf("reused %s (%s), copied %d bytes; original disk and library retained", source.Name, source.GuestOS, copied)
		})
	}
}

func verifyInstalledCloneStart(t *testing.T, manager *Manager, machine Machine) {
	t.Helper()
	if err := manager.Start("", machine); err != nil {
		t.Fatal("cloned installation did not start", err)
	}
	defer func() {
		if manager.Active() {
			if err := manager.Control(machine.ID, "kill"); err != nil {
				t.Error("terminate temporary clone", err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for manager.Active() && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if manager.Active() {
			t.Error("temporary clone did not stop")
		}
	}()
	for _, command := range []string{"stop", "cont"} {
		if err := manager.Control(machine.ID, command); err != nil {
			t.Fatal("cloned installation lifecycle", command, err)
		}
	}
	t.Log("temporary clone HCS start/pause/resume passed with network disabled")
}
