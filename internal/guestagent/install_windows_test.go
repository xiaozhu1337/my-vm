//go:build windows

package guestagent

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("MY_VM_AGENT_HELPER") == "1" {
		agentHelper()
		return
	}
	os.Exit(m.Run())
}
func agentHelper() {
	mode := os.Getenv("MY_VM_AGENT_MODE")
	if mode == "tray" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := runTray(ctx, cancel, io.Discard, func(update func(string)) error { update("原生托盘测试"); cancel(); return nil })
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(12)
		}
		return
	}
	if mode == "failed" {
		os.Exit(7)
	}
	if mode == "legacy" {
		for _, arg := range os.Args[1:] {
			if strings.HasPrefix(arg, "--ready-event") {
				os.Exit(2)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "graceful" {
		release, active, err := acquire(ctx, cancel)
		if err != nil || !active {
			os.Exit(8)
		}
		defer release()
	}
	var listener net.Listener
	if mode == "graceful" || mode == "legacy" {
		var err error
		port := os.Getenv("MY_VM_AGENT_PORT")
		if port == "" {
			port = "0"
		}
		listener, err = net.Listen("tcp4", "127.0.0.1:"+port)
		if err != nil {
			os.Exit(9)
		}
		defer listener.Close()
	}
	value := "idle"
	if listener != nil {
		value = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	}
	if file := os.Getenv("MY_VM_AGENT_READY_FILE"); file != "" {
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			os.Exit(10)
		}
	}
	for i, arg := range os.Args {
		if arg == "--ready-event" && i+1 < len(os.Args) {
			if err := signalReady(os.Args[i+1]); err != nil {
				os.Exit(11)
			}
		}
	}
	<-ctx.Done()
}
func executableCopy(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "my-vm-guest.exe")
	if err = stage(exe, target); err != nil {
		t.Fatal(err)
	}
	return target
}
func helper(t *testing.T, target, mode string) (*exec.Cmd, uint16) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(target)
	command.Env = append(os.Environ(), "MY_VM_AGENT_HELPER=1", "MY_VM_AGENT_MODE="+mode, "MY_VM_AGENT_READY_FILE="+ready)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { command.Wait(); close(done) }()
	t.Cleanup(func() { command.Process.Kill(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(ready)
		if err == nil {
			port, _ := strconv.Atoi(string(data))
			return command, uint16(port)
		}
		select {
		case <-done:
			t.Fatal("helper exited")
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("helper did not start")
	return nil, 0
}
func TestStopOnlyVerifiedAgentListener(t *testing.T) {
	for _, mode := range []string{"legacy", "graceful"} {
		t.Run(mode, func(t *testing.T) {
			target := executableCopy(t)
			listener, port := helper(t, target, mode)
			idle, _ := helper(t, target, "idle")
			otherTarget := executableCopy(t)
			other, _ := helper(t, otherTarget, "legacy")
			stopped, err := stop(target, port)
			if err != nil || !stopped {
				t.Fatalf("stop: %v %v", stopped, err)
			}
			if pid, err := listenerOwners(port); err != nil || pid[uint32(listener.Process.Pid)] {
				t.Fatalf("listener remains: %v", err)
			}
			if err := idle.Process.Kill(); err != nil {
				t.Fatalf("prepare helper was terminated: %v", err)
			}
			if err := other.Process.Kill(); err != nil {
				t.Fatalf("unrelated executable was terminated: %v", err)
			}
		})
	}
}
func TestInstallationLockAndRollback(t *testing.T) {
	source := filepath.Join(t.TempDir(), "new.exe")
	target := filepath.Join(t.TempDir(), "agent.exe")
	os.WriteFile(source, []byte("new executable"), 0700)
	os.WriteFile(target, []byte("old executable"), 0700)
	unlock, err := LockInstallation(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = LockInstallation(target); err == nil {
		t.Fatal("concurrent install accepted")
	}
	unlock()
	prepare := func(string) error { return nil }
	failed := errors.New("replacement failed")
	start := func(string) (func() error, error) { return nil, failed }
	if err = Upgrade(source, target, prepare, start, func() error { return nil }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "old executable" {
		t.Fatal("previous executable lost")
	}
	committed := false
	stopped := false
	start = func(path string) (func() error, error) {
		data, _ := os.ReadFile(path)
		if string(data) != "new executable" {
			return nil, fmt.Errorf("wrong published data")
		}
		return func() error { stopped = true; return nil }, nil
	}
	if err = Upgrade(source, target, prepare, start, func() error { return failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(target)
	if string(data) != "old executable" || !stopped {
		t.Fatal("commit failure did not stop replacement and restore original")
	}
	if err = Upgrade(source, target, prepare, start, func() error { committed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("startup not committed")
	}
	if _, err = os.Stat(target + ".previous.exe"); !os.IsNotExist(err) {
		t.Fatal("backup remains")
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".guest-update-") {
			t.Fatal("staged executable leaked")
		}
	}
}
func TestUpgradeRestoresRunningLegacyAgent(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:18389")
	if err != nil {
		t.Skip("agent port already in use")
	}
	probe.Close()
	target := executableCopy(t)
	source := executableCopy(t)
	t.Setenv("MY_VM_AGENT_PORT", "18389")
	old, _ := helper(t, target, "legacy")
	t.Setenv("MY_VM_AGENT_HELPER", "1")
	t.Setenv("MY_VM_AGENT_MODE", "legacy")
	defer Stop(target)
	failure := errors.New("new agent failed")
	err = Upgrade(source, target, func(string) error { return nil }, func(string) (func() error, error) { return nil, failure }, func() error { return nil })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	processes, err := matchingProcesses(target, 18389)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, p := range processes {
			windows.CloseHandle(p)
		}
	}()
	if len(processes) != 1 {
		t.Fatalf("restored legacy listeners: %d", len(processes))
	}
	owners, err := listenerOwners(18389)
	if err != nil || owners[uint32(old.Process.Pid)] {
		t.Fatalf("old process was not replaced: %v", err)
	}
}

func TestNativeGuestTrayLifecycle(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 to create and close a native tray")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executableCopy(t))
	command.Env = append(os.Environ(), "MY_VM_AGENT_HELPER=1", "MY_VM_AGENT_MODE=tray")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native tray: %v %s", err, output)
	}
}

func TestStartAgentWaitsForChildReady(t *testing.T) {
	target := executableCopy(t)
	t.Setenv("MY_VM_AGENT_HELPER", "1")
	t.Setenv("MY_VM_AGENT_MODE", "ready")
	stop, err := StartAgent(target)
	if err != nil {
		t.Fatal(err)
	}
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MY_VM_AGENT_MODE", "failed")
	if _, err = StartAgent(target); err == nil {
		t.Fatal("startup failure was reported as success")
	}
}
