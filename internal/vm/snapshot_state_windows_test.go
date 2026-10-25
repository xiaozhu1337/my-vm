package vm

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
	"my-vm/internal/checkpoint"
	"my-vm/internal/media"
)

func TestSnapshotStateValidationAndDeviceIdentity(t *testing.T) {
	machine := NewMachine()
	machine.MemoryMiB = 512
	storage, err := checkpoint.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := checkpoint.NewEntry("saved")
	entry.State = "running"
	entry.Adapters = []string{"d0fbded0-4097-4af8-9b3f-ea5be634533b"}
	entry.MACs = []string{"02:00:00:00:00:01"}
	for _, file := range entry.StateFiles() {
		if err = os.WriteFile(storage.Path(file), []byte("state"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = Save(snapshotStatePath(storage, entry, "yaml"), Config{Machines: []Machine{machine}}); err != nil {
		t.Fatal(err)
	}
	current := machine
	current.MemoryMiB = 1024
	if _, err = loadSnapshotMachine(current, storage, entry); err == nil {
		t.Fatal("different fixed memory accepted")
	}
	current.MemoryMiB = machine.MemoryMiB
	resumed, err := loadSnapshotMachine(current, storage, entry)
	if err != nil || resumed.MemoryMiB != current.MemoryMiB {
		t.Fatal("matching fixed memory changed", resumed, err)
	}
	current.CPUs++
	if _, err = loadSnapshotMachine(current, storage, entry); err == nil {
		t.Fatal("incompatible CPU layout accepted")
	}
	configuration := map[string]any{"VirtualMachine": map[string]any{"Devices": map[string]any{}}}
	if err = applySnapshotState(configuration, storage, entry, []string{"new-endpoint"}); err != nil {
		t.Fatal(err)
	}
	virtual := configuration["VirtualMachine"].(map[string]any)
	adapters := virtual["Devices"].(map[string]any)["NetworkAdapters"].(map[string]any)
	if adapters[entry.Adapters[0]].(map[string]any)["EndpointId"] != "new-endpoint" {
		t.Fatal("saved device identity was changed")
	}
	if virtual["RestoreState"].(map[string]any)["SaveStateFilePath"] != snapshotStatePath(storage, entry, "vmrs") {
		t.Fatal("saved memory not attached")
	}
	if err = applySnapshotState(configuration, storage, entry, nil); err == nil {
		t.Fatal("missing saved NIC accepted")
	}
	if err = os.Remove(snapshotStatePath(storage, entry, "vmrs")); err != nil {
		t.Fatal(err)
	}
	if _, err = loadSnapshotMachine(machine, storage, entry); err == nil {
		t.Fatal("missing RAM silently fell back to booting")
	}
}

func TestLegacySnapshotMemoryBacking(t *testing.T) {
	for _, state := range []string{"off", "running", "paused"} {
		for _, dynamic := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", state, dynamic), func(t *testing.T) {
				machine := NewMachine()
				storage, err := checkpoint.Load(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				entry, _ := checkpoint.NewEntry("legacy")
				entry.State = state
				for _, file := range entry.StateFiles() {
					if err = os.WriteFile(storage.Path(file), []byte("saved"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				data, err := yaml.Marshal(Config{Machines: []Machine{machine}})
				if err != nil {
					t.Fatal(err)
				}
				var config map[string]any
				if err = yaml.Unmarshal(data, &config); err != nil {
					t.Fatal(err)
				}
				saved := config["machines"].([]any)[0].(map[string]any)
				saved["dynamic_memory"], saved["max_memory_mib"] = dynamic, 8192
				data, err = yaml.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(snapshotStatePath(storage, entry, "yaml"), data, 0600); err != nil {
					t.Fatal(err)
				}
				loaded, err := loadSnapshotMachine(machine, storage, entry)
				if (err != nil) != (dynamic && state != "off") || loaded.MemoryMiB != machine.MemoryMiB {
					t.Fatal("legacy snapshot memory compatibility changed", loaded.MemoryMiB, err)
				}
			})
		}
	}
}

func TestSnapshotFirmwareCopyNeverOverwritesRecoveryPoint(t *testing.T) {
	folder := t.TempDir()
	source, target := filepath.Join(folder, "guest.vmgs"), filepath.Join(folder, "saved.vmgs")
	if err := os.WriteFile(source, []byte("old firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyState(source, target, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyState(source, target, false); err == nil {
		t.Fatal("immutable recovery point overwritten")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "old firmware" {
		t.Fatal("saved firmware changed")
	}
	if err := copyState(target, source, true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(source)
	if string(data) != "old firmware" {
		t.Fatal("working firmware not restored")
	}
}

func TestSavedSnapshotState(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 for native saved-state recovery")
	}
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.CPUs, machine.MemoryMiB, machine.DiskGiB = 1, 512, 1
	stop := func() {
		t.Helper()
		if !manager.Active() {
			return
		}
		if err := manager.Control(machine.ID, "kill"); err != nil {
			t.Error(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for manager.Active() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if manager.Active() {
			t.Error("native VM did not stop")
		}
		lock := manager.operation(machine.ID)
		lock.Lock()
		lock.Unlock()
	}
	t.Cleanup(stop)
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start("", machine); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(t.TempDir(), "guest-tools.iso")
	content := t.TempDir()
	if err := os.WriteFile(filepath.Join(content, "readme.txt"), []byte("snapshot media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := media.CreateISO(content, tools); err != nil {
		t.Fatal(err)
	}
	if err := manager.MountGuestMedia(machine.ID, tools); err != nil {
		t.Fatal(err)
	}
	if err := manager.Control(machine.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Snapshot("", machine, "-c", "paused", nil); err != nil {
		t.Fatal(err)
	}
	if manager.State(machine.ID) != "已暂停" {
		t.Fatal("paused capture resumed guest")
	}
	storage, err := manager.storage(machine)
	if err != nil {
		t.Fatal(err)
	}
	entry, found := storage.Find("paused")
	if !found || entry.State != "paused" || len(entry.MACs) != 1 {
		t.Fatal("full state metadata missing", entry)
	}
	digest := func(path string) string {
		t.Helper()
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		hash := sha256.New()
		if _, err = io.Copy(hash, file); err != nil {
			t.Fatal(err)
		}
		return string(hash.Sum(nil))
	}
	ram := snapshotStatePath(storage, entry, "vmrs")
	before := digest(ram)
	for _, fromRunning := range []bool{true, false} {
		if fromRunning {
			if err = manager.Control(machine.ID, "cont"); err != nil {
				t.Fatal(err)
			}
		} else {
			stop()
		}
		if err = manager.Snapshot("", machine, "-a", "paused", nil); err != nil {
			t.Fatal(err)
		}
		if manager.State(machine.ID) != "已暂停" {
			t.Fatal("restored power state changed")
		}
		manager.mu.Lock()
		restored := manager.sessions[machine.ID]
		manager.mu.Unlock()
		if restored.machine.DriverISO != tools {
			t.Fatal("temporary mounted media not restored", restored.machine.DriverISO)
		}
		mac, err := restored.endpoints[0].MACAddress()
		if err != nil || mac != entry.MACs[0] || restored.adapterIDs[0] != entry.Adapters[0] {
			t.Fatal("network identity changed", mac, err)
		}
		if digest(ram) != before {
			t.Fatal("restore mutated immutable RAM state")
		}
	}
}
func TestSnapshotTemporaryMediaIsRestoredAndPreflightChecked(t *testing.T) {
	machine := NewMachine()
	storage, err := checkpoint.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := checkpoint.NewEntry("mounted")
	entry.State = "paused"
	for _, file := range entry.StateFiles() {
		if err = os.WriteFile(storage.Path(file), []byte("saved"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved := machine
	saved.DriverISO = filepath.Join(t.TempDir(), "tools.iso")
	if err = os.WriteFile(saved.DriverISO, []byte("iso"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Save(snapshotStatePath(storage, entry, "yaml"), Config{Machines: []Machine{saved}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSnapshotMachine(machine, storage, entry)
	if err != nil || loaded.DriverISO != saved.DriverISO {
		t.Fatal("temporary mount rejected or lost", loaded.DriverISO, err)
	}
	if err = os.Remove(saved.DriverISO); err != nil {
		t.Fatal(err)
	}
	if _, err = loadSnapshotMachine(machine, storage, entry); err == nil {
		t.Fatal("missing snapshot media passed preflight")
	}
}
