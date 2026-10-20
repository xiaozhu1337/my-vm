package vm

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNewCloneSeparatesHostResourcesAndNames(t *testing.T) {
	source := NewMachine()
	source.Name = "Installed Windows"
	source.Shares = []Share{{Name: "Work", Path: t.TempDir()}}
	source.NetworkAdapters = []NetworkAdapter{{Network: "nat", MACAddress: "02:00:00:00:00:01"}}
	source.COMPorts = []COMPort{{Number: 1, Pipe: `\\.\pipe\source-com1`}}
	source.ISO, source.DriverISO, source.SwitchID = "setup.iso", "drivers.iso", "old-switch"
	first := NewClone(source, []Machine{source})
	second := NewClone(source, []Machine{source, first})
	for _, clone := range []Machine{first, second} {
		if clone.ID == source.ID || clone.Name == source.Name || clone.Network != "none" || clone.SwitchID != "" || len(clone.NetworkAdapters) != 0 || len(clone.COMPorts) != 0 || len(clone.Shares) != 0 || clone.ISO != "" || clone.DriverISO != "" {
			t.Fatalf("clone inherited conflicting resources: %+v", clone)
		}
		if clone.CPUs != source.CPUs || clone.MemoryMiB != source.MemoryMiB || clone.GuestOS != source.GuestOS {
			t.Fatal("hardware and guest configuration lost")
		}
	}
	if first.Name == second.Name || first.ID == second.ID || len(source.Shares) != 1 || len(source.NetworkAdapters) != 1 || len(source.COMPorts) != 1 {
		t.Fatal("clone reused identity or mutated source")
	}
}

func diskIdentity(t *testing.T, path string) windows.GUID {
	t.Helper()
	vendor, _ := windows.GUIDFromString("{EC984AEC-A0F9-47E9-901F-71415A66345B}")
	kind := struct {
		Device uint32
		Vendor windows.GUID
	}{3, vendor}
	options := struct{ Version, Depth uint32 }{1, 1}
	var handle windows.Handle
	dll := windows.NewLazySystemDLL("virtdisk.dll")
	code, _, _ := dll.NewProc("OpenVirtualDisk").Call(uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(path))), 0x80000, 0, uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&handle)))
	if code != 0 {
		t.Fatal(windows.Errno(code))
	}
	defer windows.CloseHandle(handle)
	info := struct {
		Version, Padding uint32
		Identifier       windows.GUID
		Extra            [256]byte
	}{Version: 2}
	size := uint32(unsafe.Sizeof(info))
	code, _, _ = dll.NewProc("GetVirtualDiskInformation").Call(uintptr(handle), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&info)), 0)
	if code != 0 {
		t.Fatal(windows.Errno(code))
	}
	return info.Identifier
}

func TestClonePreservesSourceAndCopiesPersistentState(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	source := NewMachine()
	source.DiskGiB = 1
	if err := manager.CreateDisk("", source); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Dir(DiskPath(manager.Root, source))
	for _, name := range []string{"guest.vmgs", "runtime.vmrs"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Snapshot("", source, "-c", "original", nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(DiskPath(manager.Root, source))
	if err != nil {
		t.Fatal(err)
	}
	originalID := diskIdentity(t, DiskPath(manager.Root, source))
	target := NewClone(source, []Machine{source})
	var copied, total int64
	if err := manager.Clone(source, target, func(done, size int64) { copied, total = done, size }); err != nil {
		t.Fatal(err)
	}
	if total == 0 || copied != total {
		t.Fatal("missing copy progress", copied, total)
	}
	if diskIdentity(t, DiskPath(manager.Root, target)) == originalID {
		t.Fatal("clone retained original VHDX identity")
	}
	after, err := os.ReadFile(DiskPath(manager.Root, source))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source disk changed", err)
	}
	guest, err := os.ReadFile(filepath.Join(filepath.Dir(DiskPath(manager.Root, target)), "guest.vmgs"))
	if err != nil || string(guest) != "guest.vmgs" {
		t.Fatal("firmware state not copied", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(DiskPath(manager.Root, target)), "runtime.vmrs")); !os.IsNotExist(err) {
		t.Fatal("runtime state inherited", err)
	}
	if snapshots, err := manager.Snapshots("", target); err != nil || len(snapshots) != 0 {
		t.Fatal("snapshots inherited", snapshots, err)
	}
	if err := manager.Clone(source, target, nil); err == nil {
		t.Fatal("existing clone overwritten")
	}
	invalid := NewClone(source, []Machine{source})
	manager.sessions[source.ID] = &session{}
	if err := manager.Clone(source, invalid, nil); err == nil {
		t.Fatal("running source copied")
	}
	delete(manager.sessions, source.ID)
	if err := os.WriteFile(DiskPath(manager.Root, source), []byte("invalid disk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Clone(source, invalid, nil); err == nil {
		t.Fatal("invalid VHDX accepted")
	}
	if _, err := os.Stat(filepath.Dir(DiskPath(manager.Root, invalid))); !os.IsNotExist(err) {
		t.Fatal("failed clone left partial output", err)
	}
}

func TestSnapshotWorkDoesNotBlockOtherMachines(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	machine, other := NewMachine(), NewMachine()
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	go func() {
		finished <- manager.Snapshot("", machine, "-c", "busy", func(int64, int64) { once.Do(func() { close(started); <-release }) })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("copy did not start")
	}
	control := make(chan error, 1)
	go func() { control <- manager.Control(other.ID, "kill") }()
	select {
	case err := <-control:
		if err == nil {
			t.Fatal("unexpected running machine")
		}
	case <-time.After(time.Second):
		t.Fatal("other machine blocked by copy")
	}
	list := make(chan error, 1)
	go func() { _, err := manager.Snapshots("", machine); list <- err }()
	select {
	case <-list:
		t.Fatal("same machine bypassed disk lock")
	case <-time.After(30 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-list; err != nil {
		t.Fatal(err)
	}
}
