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
	ctx, cancel := context.WithCancel(context.Background())
	err = manager.SnapshotContext(ctx, machine, "-a", "baseline", func(int64, int64) { cancel() })
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatal("restore did not cancel", err)
	}
	unchanged, err := os.ReadFile(disk)
	if err != nil || string(unchanged) != "changed" {
		t.Fatal("canceled restore replaced current disk", err)
	}
	if _, err = os.Stat(disk + ".restore"); !os.IsNotExist(err) {
		t.Fatal("restore temporary file leaked", err)
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
