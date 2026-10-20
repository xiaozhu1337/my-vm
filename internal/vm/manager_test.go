package vm

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"unsafe"

	"golang.org/x/sys/windows"
	"my-vm/internal/checkpoint"
	"my-vm/internal/hcs"
)

// diskMarker reads and writes one sector of a newly-created, unformatted test VHDX.
func diskMarker(t *testing.T, path string, value *byte) byte {
	t.Helper()
	return diskMarkerAt(t, path, 1<<20, value)
}

func diskMarkerAt(t *testing.T, path string, offset int64, value *byte) byte {
	t.Helper()
	dll := windows.NewLazySystemDLL("virtdisk.dll")
	vendor, _ := windows.GUIDFromString("{EC984AEC-A0F9-47E9-901F-71415A66345B}")
	kind := struct {
		Device uint32
		Vendor windows.GUID
	}{3, vendor}
	opts := struct {
		Version               uint32
		GetInfoOnly, ReadOnly int32
		Resiliency            windows.GUID
	}{Version: 2}
	var disk windows.Handle
	code, _, _ := dll.NewProc("OpenVirtualDisk").Call(uintptr(unsafe.Pointer(&kind)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(path))), 0, 0, uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&disk)))
	if code != 0 {
		t.Fatal("open virtual disk", windows.Errno(code))
	}
	defer windows.CloseHandle(disk)
	attach := struct{ Version uint32 }{1}
	code, _, _ = dll.NewProc("AttachVirtualDisk").Call(uintptr(disk), 0, 2, 0, uintptr(unsafe.Pointer(&attach)), 0)
	if code != 0 {
		t.Fatal("attach test VHDX", windows.Errno(code))
	}
	defer dll.NewProc("DetachVirtualDisk").Call(uintptr(disk), 0, 0)
	var physical [1024]uint16
	size := uint32(unsafe.Sizeof(physical))
	code, _, _ = dll.NewProc("GetVirtualDiskPhysicalPath").Call(uintptr(disk), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&physical[0])))
	if code != 0 {
		t.Fatal("get virtual device", windows.Errno(code))
	}
	handle, err := windows.CreateFile(&physical[0], windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal("open virtual device", err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 512)
	if value != nil {
		for i := range block {
			block[i] = *value
		}
		if _, err = file.Write(block); err != nil {
			t.Fatal(err)
		}
		if err = file.Sync(); err != nil {
			t.Fatal(err)
		}
		return *value
	}
	if _, err = io.ReadFull(file, block); err != nil {
		t.Fatal(err)
	}
	return block[0]
}

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
	first := byte(11)
	diskMarker(t, disk, &first)
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
	storage, err := checkpoint.Load(filepath.Dir(disk))
	if err != nil {
		t.Fatal(err)
	}
	active := storage.Path(storage.Active)
	changed := byte(22)
	diskMarker(t, active, &changed)
	ctx, cancel := context.WithCancel(context.Background())
	err = manager.SnapshotContext(ctx, machine, "-a", "baseline", func(int64, int64) { cancel() })
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatal("restore did not cancel", err)
	}
	if got := diskMarker(t, active, nil); got != changed {
		t.Fatal("canceled restore replaced current disk", got)
	}
	if _, err = os.Stat(disk + ".restore"); !os.IsNotExist(err) {
		t.Fatal("restore temporary file leaked", err)
	}
	if err = manager.Snapshot("", machine, "-a", "baseline", nil); err != nil {
		t.Fatal(err)
	}
	storage, err = checkpoint.Load(filepath.Dir(disk))
	if err != nil {
		t.Fatal(err)
	}
	if got := diskMarker(t, storage.Path(storage.Active), nil); got != first {
		t.Fatal("restore lost the logical contents of the recovery point", got)
	}
	if got := diskMarker(t, disk, nil); got != first {
		t.Fatal("immutable parent was changed", got)
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
func TestSnapshotBranchesCloneCurrentLeafAndRecoverJournal(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.DiskGiB = 1
	machine.Network = "none"
	if err := manager.CreateDisk("", machine); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Dir(DiskPath(manager.Root, machine))
	load := func() *checkpoint.Store {
		t.Helper()
		s, err := checkpoint.Load(folder)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	create := func(name string) {
		t.Helper()
		if err := manager.Snapshot("", machine, "-c", name, nil); err != nil {
			t.Fatal(err)
		}
	}
	marker := byte(11)
	diskMarker(t, DiskPath(manager.Root, machine), &marker)
	create("baseline")
	s := load()
	marker = 22
	diskMarker(t, s.Path(s.Active), &marker)
	create("branch_b")
	if err := manager.Snapshot("", machine, "-a", "baseline", nil); err != nil {
		t.Fatal(err)
	}
	s = load()
	marker = 33
	diskMarker(t, s.Path(s.Active), &marker)
	create("branch_c")
	list, err := manager.Snapshots("", machine)
	if err != nil || len(list) != 3 || list[1].ParentID != list[2].ParentID || list[2].Depth != 1 || !list[2].Current {
		t.Fatal("branch tree incorrect", list, err)
	}
	target := NewClone(machine, []Machine{machine})
	if err = manager.Clone(machine, target, nil); err != nil {
		t.Fatal(err)
	}
	if got := diskMarker(t, DiskPath(manager.Root, target), nil); got != 33 {
		t.Fatal("clone copied an old ancestor", got)
	}
	if parent, err := hcs.DiskParent(DiskPath(manager.Root, target)); err != nil || parent != "" {
		t.Fatal("clone depends on source snapshot chain", parent, err)
	}
	if err = manager.Snapshot("", machine, "-d", "baseline", nil); err != nil {
		t.Fatal(err)
	}
	s = load()
	if got := diskMarker(t, s.Path(s.Active), nil); got != 33 {
		t.Fatal("deleting ancestor damaged active branch", got)
	}
	cfg, err := Configuration(manager.Root, machine, "")
	if err != nil {
		t.Fatal(err)
	}
	attachment := cfg["VirtualMachine"].(map[string]any)["Devices"].(map[string]any)["Scsi"].(map[string]any)["my-vm-scsi"].(map[string]any)["Attachments"].(map[string]any)["0"].(map[string]any)
	if attachment["Path"] != s.Path(s.Active) {
		t.Fatal("boot configuration still uses original base disk", attachment)
	}
	stage := func(name string) string {
		t.Helper()
		e, err := checkpoint.NewEntry(name)
		if err != nil {
			t.Fatal(err)
		}
		e.Disk, e.Parent = s.Active, s.Current
		id, _ := checkpoint.NewID()
		child := "disk-" + id + ".vhdx"
		s.Layers[child] = s.Active
		s.Pending = &checkpoint.Fork{Entry: e, Child: child}
		if err = s.Save(); err != nil {
			t.Fatal(err)
		}
		return child
	}
	child := stage("recovered")
	if err = hcs.CreateDifferencingDisk(s.Path(child), s.Path(s.Active)); err != nil {
		t.Fatal(err)
	}
	manager = NewManager(manager.Root, io.Discard)
	list, err = manager.Snapshots("", machine)
	if err != nil || len(list) != 3 || !list[2].Current || list[2].Name != "recovered" {
		t.Fatal("committed native switch not recovered", list, err)
	}
	s = load()
	active := s.Active
	stage("not_created")
	if _, err = manager.Snapshots("", machine); err != nil {
		t.Fatal(err)
	}
	s = load()
	if s.Active != active || s.Pending != nil {
		t.Fatal("unstarted fork damaged current disk")
	}
	id, _ := checkpoint.NewID()
	candidate := "disk-" + id + ".vhdx"
	s.Retire(candidate)
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	if err = hcs.CreateDifferencingDisk(s.Path(candidate), s.Path(s.Active)); err != nil {
		t.Fatal(err)
	}
	manager = NewManager(manager.Root, io.Discard)
	if _, err = manager.Snapshots("", machine); err != nil {
		t.Fatal(err)
	}
	s = load()
	if s.Active != active || len(s.Garbage) != 0 {
		t.Fatal("interrupted restore damaged the active disk or left cleanup pending")
	}
	if _, err = os.Stat(s.Path(candidate)); !os.IsNotExist(err) {
		t.Fatal("interrupted restore candidate was not collected", err)
	}
	path := s.Path(s.Active)
	if err = os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(path+".saved", path)
	if err = manager.Start("", machine); err == nil {
		_ = manager.Control(machine.ID, "kill")
		t.Fatal("missing active leaf silently booted an older parent")
	}
}

func TestSnapshotCancelWhileWaitingForOperation(t *testing.T) {
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	lock := manager.operation(machine.ID)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- manager.SnapshotContext(ctx, machine, "-c", "baseline", nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("canceled snapshot remained blocked on operation lock")
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
	machine.DiskGiB = 2
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
	manager.mu.Lock()
	original := manager.sessions[machine.ID]
	manager.mu.Unlock()
	for _, name := range []string{"first_live", "second_live"} {
		if err := manager.Snapshot("", machine, "-c", name, func(done, total int64) {
			if manager.State(machine.ID) != "运行中" {
				t.Error("snapshot paused or stopped the VM")
			}
		}); err != nil {
			t.Fatal(err)
		}
		manager.mu.Lock()
		same := manager.sessions[machine.ID] == original
		manager.mu.Unlock()
		if !same {
			t.Fatal("snapshot replaced the running HCS session")
		}
	}
	list, err := manager.Snapshots("", machine)
	if err != nil || len(list) != 2 || list[1].ParentID != list[0].ID {
		t.Fatal("live snapshot tree lost", list, err)
	}
	if err = manager.Control(machine.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Dir(DiskPath(manager.Root, machine))
	for _, failAt := range []int64{25, 65} {
		var handle windows.Handle
		err = manager.Snapshot("", machine, "-a", "first_live", func(done, total int64) {
			if done != failAt || handle != 0 {
				return
			}
			path := filepath.Join(folder, checkpoint.CatalogFile)
			if failAt == 65 {
				s, readErr := checkpoint.Load(folder)
				if readErr != nil {
					t.Fatal(readErr)
				}
				path = s.Path(s.Active)
			}
			var openErr error
			handle, openErr = windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
		})
		if handle != 0 {
			windows.CloseHandle(handle)
		}
		if err == nil || handle == 0 || manager.State(machine.ID) != "已暂停" {
			t.Fatalf("failed restore at %d did not keep the previous VM paused: state=%s err=%v", failAt, manager.State(machine.ID), err)
		}
	}
	if err = manager.Control(machine.ID, "cont"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Snapshot("", machine, "-a", "first_live", nil); err != nil {
		t.Fatal(err)
	}
	if manager.State(machine.ID) != "运行中" {
		t.Fatal("running restore did not restart the VM")
	}
	if err := manager.Snapshot("", machine, "-c", "new_branch", nil); err != nil {
		t.Fatal(err)
	}
	list, err = manager.Snapshots("", machine)
	if err != nil || len(list) != 3 || list[1].ParentID != list[2].ParentID || list[1].ParentID != list[0].ID {
		t.Fatal("restored snapshot did not create a sibling branch", list, err)
	}
	if err := manager.Snapshot("", machine, "-d", "first_live", nil); err != nil {
		t.Fatal(err)
	}
	if manager.State(machine.ID) != "运行中" {
		t.Fatal("delete interrupted the running VM")
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
	if _, err = manager.Snapshots("", machine); err != nil {
		t.Fatal(err)
	}
	storage, err := checkpoint.Load(folder)
	if err != nil {
		t.Fatal(err)
	}
	marker := byte(77)
	diskMarkerAt(t, storage.Path(storage.Active), 1<<30, &marker)
	if got := diskMarkerAt(t, storage.Path(storage.Active), 1<<30, nil); got != marker {
		t.Fatal("online fork did not retain the source disk capacity", got)
	}
	if _, err := os.Stat(filepath.Join(manager.Root, "machines", machine.ID, "disk.vhdx")); err != nil {
		t.Fatal("disk lost", err)
	}
}

func TestShutdownAllRequestsInParallelAndWaitsForEveryGuest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	states := map[string]string{"slow": "运行中", "paused": "已暂停", "off": "已关机"}
	commands := map[string][]string{}
	state := func(id string) string { mu.Lock(); defer mu.Unlock(); return states[id] }
	started, release := make(chan string, 2), make(chan struct{})
	control := func(id, command string) error {
		mu.Lock()
		commands[id] = append(commands[id], command)
		if command == "cont" {
			states[id] = "运行中"
		}
		mu.Unlock()
		if command == "system_powerdown" {
			started <- id
			if id == "slow" {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return nil
	}
	machines := []Machine{{ID: "slow", Name: "Slow"}, {ID: "paused", Name: "Paused"}, {ID: "off", Name: "Off"}}
	done := make(chan error, 1)
	go func() { done <- shutdownAll(ctx, machines, state, control) }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("one slow shutdown prevented requesting the other guest")
		}
	}
	close(release)
	mu.Lock()
	states["paused"] = "已关机"
	mu.Unlock()
	select {
	case err := <-done:
		t.Fatal("accepted requests were mistaken for all guests stopped", err)
	default:
	}
	mu.Lock()
	states["slow"] = "已关机"
	mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("all stopped guests did not finish shutdown")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(commands["paused"], []string{"cont", "system_powerdown"}) || !slices.Equal(commands["slow"], []string{"system_powerdown"}) || len(commands["off"]) != 0 {
		t.Fatal("shutdown used unexpected or forced commands", commands)
	}
}

func TestShutdownAllRetainsErrorsAndStillShutsDownOtherGuests(t *testing.T) {
	resumeErr, shutdownErr := errors.New("resume refused"), errors.New("shutdown refused")
	var mu sync.Mutex
	states := map[string]string{"paused": "已暂停", "refused": "运行中", "other": "运行中"}
	commands := map[string][]string{}
	state := func(id string) string { mu.Lock(); defer mu.Unlock(); return states[id] }
	control := func(id, command string) error {
		mu.Lock()
		defer mu.Unlock()
		commands[id] = append(commands[id], command)
		switch id {
		case "paused":
			return resumeErr
		case "refused":
			return shutdownErr
		default:
			states[id] = "已关机"
			return nil
		}
	}
	err := shutdownAll(context.Background(), []Machine{{ID: "paused", Name: "Paused"}, {ID: "refused", Name: "Refused"}, {ID: "other", Name: "Other"}}, state, control)
	if !errors.Is(err, resumeErr) || !errors.Is(err, shutdownErr) || !slices.Equal(commands["paused"], []string{"cont"}) || !slices.Equal(commands["other"], []string{"system_powerdown"}) {
		t.Fatal("shutdown lost failures or abandoned another guest", err, commands)
	}
}

func TestShutdownCancellationDoesNotForcePowerOff(t *testing.T) {
	for _, alreadyCanceled := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if alreadyCanceled {
			cancel()
		}
		commands := []string{}
		err := shutdownMachine(ctx, "vm", func(string) string { return "运行中" }, func(_ string, command string) error { commands = append(commands, command); return nil })
		cancel()
		if err == nil || len(commands) > 1 || len(commands) == 1 && commands[0] != "system_powerdown" || alreadyCanceled && len(commands) != 0 {
			t.Fatal("canceled shutdown escalated or ignored cancellation", err, commands)
		}
	}
	manager := NewManager(t.TempDir(), io.Discard)
	if err := manager.ShutdownAll(context.Background()); err != nil || manager.Active() {
		t.Fatal("empty manager did not complete", err)
	}
}

func TestShutdownGuestCanStopBeforeItsRequestCompletes(t *testing.T) {
	state := "运行中"
	err := shutdownMachine(context.Background(), "vm", func(string) string { return state }, func(string, string) error { state = "已关机"; return errors.New("already stopped") })
	if err != nil {
		t.Fatal("guest stopping during its request prevented exit", err)
	}
}
