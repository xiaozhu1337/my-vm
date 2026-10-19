//go:build windows

package vm

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"my-vm/internal/hcs"
)

func TestHCSDefaultNAT(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 for real Default Switch attachment")
	}
	manager := NewManager(t.TempDir(), io.Discard)
	machine := NewMachine()
	machine.DiskGiB, machine.MemoryMiB, machine.CPUs = 1, 512, 1
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
			t.Error("network probe VM did not stop")
		}
	}()
	manager.mu.Lock()
	endpoint := manager.sessions[machine.ID].endpoints[0].ID
	manager.mu.Unlock()
	native := windows.NewLazySystemDLL("computenetwork.dll")
	free := windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")
	var handle uintptr
	var detail, properties *uint16
	hr, _, _ := native.NewProc("HcnOpenEndpoint").Call(uintptr(unsafe.Pointer(&endpoint)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&detail)))
	if detail != nil {
		free.Call(uintptr(unsafe.Pointer(detail)))
		detail = nil
	}
	if int32(hr) < 0 {
		t.Fatalf("HcnOpenEndpoint: 0x%x", hr)
	}
	defer native.NewProc("HcnCloseEndpoint").Call(handle)
	query, _ := windows.UTF16PtrFromString(`{"SchemaVersion":{"Major":2,"Minor":0}}`)
	hr, _, _ = native.NewProc("HcnQueryEndpointProperties").Call(handle, uintptr(unsafe.Pointer(query)), uintptr(unsafe.Pointer(&properties)), uintptr(unsafe.Pointer(&detail)))
	if detail != nil {
		defer free.Call(uintptr(unsafe.Pointer(detail)))
	}
	if properties != nil {
		defer free.Call(uintptr(unsafe.Pointer(properties)))
	}
	if int32(hr) < 0 {
		t.Fatalf("HcnQueryEndpointProperties: 0x%x", hr)
	}
	var actual struct{ HostComputeNetwork, MacAddress string }
	if err := json.Unmarshal([]byte(windows.UTF16PtrToString(properties)), &actual); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(strings.Trim(actual.HostComputeNetwork, "{}"), defaultSwitchID) {
		t.Fatal("VM attached to wrong network", actual)
	}
	t.Logf("temporary VM endpoint %s, MAC %s belongs to Default Switch %s", endpoint.String(), actual.MacAddress, actual.HostComputeNetwork)
}

func TestDeviceEndpointLifecycle(t *testing.T) {
	machine := NewMachine()
	machine.NetworkAdapters = []NetworkAdapter{{Network: "none"}, {Network: "nat", MACAddress: "02:00:00:00:00:01"}, {Network: "bridge", SwitchID: defaultSwitchID}}
	networks := []hcs.Network{{ID: defaultSwitchID, Type: "ICS"}}
	failure := errors.New("create failed")
	cleanupFailure := errors.New("cleanup failed")
	for _, failAt := range []int{0, 1, 2, -1} {
		created, closed := 0, 0
		create := func(switchID, owner string, mac ...string) (*hcs.Endpoint, error) {
			if created == failAt {
				return nil, failure
			}
			if switchID != defaultSwitchID || owner != "owner" || len(mac) != 1 {
				t.Fatal("endpoint arguments changed", switchID, owner, mac)
			}
			if created == 1 && mac[0] != machine.NetworkAdapters[1].MACAddress {
				t.Fatal("additional NIC lost its MAC")
			}
			created++
			return &hcs.Endpoint{}, nil
		}
		closeEndpoint := func(*hcs.Endpoint) error { closed++; return cleanupFailure }
		endpoints, err := createNetworkEndpoints(machine, networks, "owner", create, closeEndpoint)
		if failAt >= 0 {
			if !errors.Is(err, failure) || created != closed || len(endpoints) != 0 || created > 0 && !errors.Is(err, cleanupFailure) {
				t.Fatal("partial endpoint failure leaked resources", created, closed, endpoints, err)
			}
		} else {
			if err != nil || created != 3 || closed != 0 {
				t.Fatal("successful endpoints closed early", created, closed, err)
			}
			if err = closeNetworkEndpoints(endpoints, closeEndpoint); !errors.Is(err, cleanupFailure) || closed != 3 {
				t.Fatal("cleanup did not visit every endpoint", closed, err)
			}
		}
	}
	machine.NetworkAdapters[1].SwitchID = "missing"
	created, closed := 0, 0
	_, err := createNetworkEndpoints(machine, networks, "owner", func(string, string, ...string) (*hcs.Endpoint, error) { created++; return &hcs.Endpoint{}, nil }, func(*hcs.Endpoint) error { closed++; return nil })
	if err == nil || created != 1 || closed != 1 {
		t.Fatal("network selection failure leaked primary endpoint", created, closed, err)
	}
	machine.Network, machine.NetworkAdapters = "none", nil
	endpoints, err := createNetworkEndpoints(machine, nil, "owner", nil, nil)
	if err != nil || len(endpoints) != 0 {
		t.Fatal("offline machine created endpoints", endpoints, err)
	}
}

func TestSelectNetworkDoesNotUseWSLForDefaultNAT(t *testing.T) {
	wsl := hcs.Network{ID: "790e58b4-7939-4434-9358-89ae7ddbe87e", Name: "WSL (Hyper-V firewall)", Type: "ICS"}
	expected := hcs.Network{ID: defaultSwitchID, Name: "Localized default switch", Type: "ICS"}
	for _, networks := range [][]hcs.Network{{wsl, expected}, {expected, wsl}} {
		selected, err := selectNetwork(networks, "")
		if err != nil || selected.ID != expected.ID {
			t.Fatal(selected, err)
		}
	}
	if _, err := selectNetwork([]hcs.Network{wsl}, ""); err == nil {
		t.Fatal("WSL was accepted as the automatic NAT network")
	}
	if _, err := selectNetwork(nil, ""); err == nil {
		t.Fatal("missing default switch was silently accepted")
	}
	external := hcs.Network{ID: "ABCDEF12-0000-0000-0000-123456789ABC", Name: "External", Type: "Transparent"}
	if selected, err := selectNetwork([]hcs.Network{wsl, external}, "{abcdef12-0000-0000-0000-123456789abc}"); err != nil || selected.ID != external.ID {
		t.Fatal(selected, err)
	}
	if _, err := selectNetwork([]hcs.Network{wsl, expected}, external.ID); err == nil {
		t.Fatal("missing explicit switch silently fell back")
	}
	expected.ID = "{C08CB7B8-9B3C-408E-8E30-5E16A3AEB444}"
	if _, err := selectNetwork([]hcs.Network{wsl, expected}, ""); err != nil {
		t.Fatal("GUID normalization failed", err)
	}
}
