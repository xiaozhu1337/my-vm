package vm

import (
	"my-vm/internal/sharing"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeviceOwnershipAcrossMachines(t *testing.T) {
	first, second := NewMachine(), NewMachine()
	first.Name, second.Name = "First", "Second"
	first.COMPorts = []COMPort{{Number: 1, Pipe: `\\.\pipe\myvm-fixture-owner`}}
	second.COMPorts = []COMPort{{Number: 2, Pipe: `\\.\pipe\MYVM-FIXTURE-OWNER`}}
	path := filepath.Join(t.TempDir(), "library.yaml")
	if err := Save(path, Config{Machines: []Machine{first}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if Save(path, Config{Machines: []Machine{first, second}}) == nil {
		t.Fatal("accepted cross-VM serial pipe collision")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("damaged original config on validation failure")
	}
	first.COMPorts, second.COMPorts = nil, nil
	first.NetworkAdapters = []NetworkAdapter{{Network: "nat", MACAddress: "02:11:22:33:44:55"}}
	second.NetworkAdapters = []NetworkAdapter{{Network: "nat", MACAddress: "02-11-22-33-44-55"}}
	if ValidateDeviceOwnership([]Machine{first, second}) == nil {
		t.Fatal("accepted cross-VM MAC collision")
	}
	second.NetworkAdapters[0].Network = "none"
	if err := ValidateDeviceOwnership([]Machine{first, second}); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.yaml")
	if cfg, err := Load(path); err != nil || len(cfg.Machines) != 0 {
		t.Fatal(cfg, err)
	}
	m := NewMachine()
	m.Name = "Windows 中文测试"
	m.ISO = `C:\ISO images\安装,光盘.iso`
	m.DriverISO = `C:\ISO images\附加.iso`
	m.Shares = []Share{{Name: "Files", Path: `C:\shared`, ReadOnly: true}}
	m.NetworkAdapters = []NetworkAdapter{{Network: "nat", MACAddress: "02:00:00:00:00:01"}, {Network: "none"}}
	m.COMPorts = []COMPort{{Number: 2, Pipe: `\\.\pipe\round-trip-com2`}}
	want := Config{QEMUDir: `C:\legacy\qemu`, Machines: []Machine{m}}
	for range 2 {
		if err := Save(path, want); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v error %v", got, want, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(data), "\xef\xbb\xbf") || !strings.Contains(string(data), "# 虚拟 CPU 数量") {
		t.Fatal("expected commented UTF-8 without BOM")
	}
}
func TestConfigRejectsUnknownFieldsAndDocuments(t *testing.T) {
	for _, input := range []string{"machines: [broken", "machine: []\n", "machines: []\n---\nmachines: []\n", "machines:\n  - id: abc\n    unexpected: true\n"} {
		path := filepath.Join(t.TempDir(), "library.yaml")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %q", input)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != input {
			t.Fatal("damaged configuration changed")
		}
	}
}
func TestValidate(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Machine)
	}{
		{"traversal", func(m *Machine) { m.ID = "../../disk" }},
		{"cpu", func(m *Machine) { m.CPUs = 0 }},
		{"memory", func(m *Machine) { m.MemoryMiB = 1 }},
		{"disk", func(m *Machine) { m.DiskGiB = 0 }},
		{"name", func(m *Machine) { m.Name = " " }},
		{"network", func(m *Machine) { m.Network = "unknown" }},
		{"bridge missing", func(m *Machine) { m.Network = "bridge" }},
		{"backend", func(m *Machine) { m.Backend = "unknown" }},
		{"legacy accel", func(m *Machine) { m.Backend = "qemu"; m.Accelerator = "tcg" }},
		{"duplicate share", func(m *Machine) {
			m.Shares = []Share{{Name: "Files", Path: `C:\share`}, {Name: "files", Path: `C:\other`}}
		}},
		{"relative share", func(m *Machine) { m.Shares = []Share{{Name: "Files", Path: "relative"}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewMachine()
			test.change(&m)
			if m.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
func TestConfiguration(t *testing.T) {
	m := NewMachine()
	m.ISO = `C:\安装,光盘.iso`
	m.DriverISO = `C:\附加.iso`
	m.Shares = []Share{{Name: "Files", Path: `C:\shared`, ReadOnly: true}}
	cfg, err := configurationWithEndpoints(`C:\VM,中文`, m, []string{"endpoint-test"}, "S-1-5-21-test")
	if err != nil {
		t.Fatal(err)
	}
	virtual := cfg["VirtualMachine"].(map[string]any)
	if cfg["SchemaVersion"].(map[string]int)["Minor"] < 5 || virtual["Services"].(map[string]any)["Shutdown"] == nil {
		t.Fatal("integration-service shutdown requires schema 2.5 and an explicit service")
	}
	devices := virtual["Devices"].(map[string]any)
	attachments := devices["Scsi"].(map[string]any)["my-vm-scsi"].(map[string]any)["Attachments"].(map[string]any)
	if attachments["0"].(map[string]any)["Path"] != DiskPath(`C:\VM,中文`, m) || attachments["1"].(map[string]any)["Path"] != m.ISO || attachments["2"].(map[string]any)["Path"] != m.DriverISO {
		t.Fatal("disk or ISO path changed")
	}
	if cfg["ShouldTerminateOnLastHandleClosed"] != true {
		t.Fatal("orphaned VM protection missing")
	}
	if devices["VideoMonitor"] == nil || devices["EnhancedModeVideo"] == nil {
		t.Fatal("console configuration missing")
	}
}

func TestLinuxCredentialChannelWithoutSharedFolders(t *testing.T) {
	machine := NewMachine()
	machine.GuestOS = "linux"
	machine.Shares = nil
	cfg, err := configurationWithEndpoints(t.TempDir(), machine, []string{"endpoint-test"}, "S-1-5-21-test")
	if err != nil {
		t.Fatal(err)
	}
	devices := cfg["VirtualMachine"].(map[string]any)["Devices"].(map[string]any)
	services := devices["HvSocket"].(map[string]any)["HvSocketConfig"].(map[string]any)["ServiceTable"].(map[string]any)
	service := services[sharing.ServiceGUID()].(map[string]any)
	if service["AllowWildcardBinds"] != false {
		t.Fatal("registration must use a VM-specific listener")
	}
}

func TestDeviceValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Machine)
	}{
		{"too many NICs", func(m *Machine) { m.NetworkAdapters = make([]NetworkAdapter, 8) }},
		{"mode", func(m *Machine) { m.NetworkAdapters[0].Network = "invalid" }},
		{"primary GUID", func(m *Machine) { m.SwitchID = "not-a-guid" }},
		{"extra GUID", func(m *Machine) { m.NetworkAdapters[0].SwitchID = "{bad}" }},
		{"unpaired braces", func(m *Machine) { m.SwitchID = "{" + defaultSwitchID }},
		{"bridge", func(m *Machine) { m.NetworkAdapters[0].Network = "bridge" }},
		{"MAC", func(m *Machine) { m.NetworkAdapters[0].MACAddress = "invalid" }},
		{"multicast", func(m *Machine) { m.NetworkAdapters[0].MACAddress = "01:00:00:00:00:01" }},
		{"broadcast", func(m *Machine) { m.NetworkAdapters[0].MACAddress = "ff:ff:ff:ff:ff:ff" }},
		{"zero", func(m *Machine) { m.NetworkAdapters[0].MACAddress = "00:00:00:00:00:00" }},
		{"EUI64", func(m *Machine) { m.NetworkAdapters[0].MACAddress = "02:00:00:00:00:00:00:01" }},
		{"duplicate MAC", func(m *Machine) {
			m.NetworkAdapters = append(m.NetworkAdapters, NetworkAdapter{Network: "none", MACAddress: "02-AB-00-00-00-01"})
		}},
		{"COM number", func(m *Machine) { m.COMPorts[0].Number = 3 }},
		{"duplicate COM", func(m *Machine) { m.COMPorts = append(m.COMPorts, COMPort{Number: 1, Pipe: `\\.\pipe\other`}) }},
		{"relative pipe", func(m *Machine) { m.COMPorts[0].Pipe = "serial" }},
		{"remote pipe", func(m *Machine) { m.COMPorts[0].Pipe = `\\server\pipe\serial` }},
		{"empty pipe", func(m *Machine) { m.COMPorts[0].Pipe = `\\.\pipe\` }},
		{"reserved basic", func(m *Machine) { m.COMPorts[0].Pipe = `\\.\pipe\MY-VM-` + m.ID + ".basic" }},
		{"reserved enhanced", func(m *Machine) { m.COMPorts[0].Pipe = `\\.\pipe\my-vm-` + m.ID + ".enhanced" }},
		{"nested pipe", func(m *Machine) { m.COMPorts[0].Pipe = `\\.\pipe\..\serial` }},
		{"pipe null", func(m *Machine) { m.COMPorts[0].Pipe += "\x00" }},
		{"pipe collision", func(m *Machine) { m.COMPorts = append(m.COMPorts, COMPort{Number: 2, Pipe: `\\.\PIPE\SERIAL`}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := NewMachine()
			m.NetworkAdapters = []NetworkAdapter{{Network: "nat", MACAddress: "02:ab:00:00:00:01"}}
			m.COMPorts = []COMPort{{Number: 1, Pipe: `\\.\pipe\serial`}}
			test.change(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("invalid devices accepted")
			}
		})
	}
	m := NewMachine()
	m.NetworkAdapters = make([]NetworkAdapter, 7)
	for i := range m.NetworkAdapters {
		m.NetworkAdapters[i] = NetworkAdapter{Network: "nat", SwitchID: "{" + strings.ToUpper(defaultSwitchID) + "}"}
	}
	m.COMPorts = []COMPort{{Number: 1, Pipe: `\\.\pipe\serial1`}, {Number: 2, Pipe: `\\.\pipe\serial2`}}
	if err := m.Validate(); err != nil || len(m.enabledNetworkAdapters()) != 8 {
		t.Fatal("valid maximum device configuration rejected", err)
	}
}

func TestDeviceConfiguration(t *testing.T) {
	m := NewMachine()
	m.NetworkAdapters = []NetworkAdapter{{Network: "none"}, {Network: "nat"}, {Network: "bridge", SwitchID: defaultSwitchID}}
	m.COMPorts = []COMPort{{Number: 2, Pipe: `\\.\pipe\serial2`}, {Number: 1, Pipe: `\\.\pipe\serial1`}}
	for _, primary := range []string{"nat", "none"} {
		m.Network = primary
		endpoints := []string{"additional1", "additional2"}
		if primary == "nat" {
			endpoints = append([]string{"primary"}, endpoints...)
		}
		cfg, err := configurationWithEndpoints(`C:\VM`, m, endpoints, "current-user-sid")
		if err != nil {
			t.Fatal(err)
		}
		devices := cfg["VirtualMachine"].(map[string]any)["Devices"].(map[string]any)
		adapters := devices["NetworkAdapters"].(map[string]any)
		if len(adapters) != len(endpoints) {
			t.Fatal("wrong NIC count", adapters)
		}
		for _, endpoint := range endpoints {
			if adapters[endpoint].(map[string]any)["EndpointId"] != endpoint {
				t.Fatal("wrong endpoint", adapters)
			}
		}
		ports := devices["ComPorts"].(map[string]any)
		if ports["0"].(map[string]any)["NamedPipe"] != m.COMPorts[1].Pipe || ports["1"].(map[string]any)["NamedPipe"] != m.COMPorts[0].Pipe {
			t.Fatal("wrong COM schema", ports)
		}
		for _, name := range []string{"VideoMonitor", "EnhancedModeVideo"} {
			connection := devices[name].(map[string]any)["ConnectionOptions"].(map[string]any)
			if connection["AccessSids"].([]string)[0] != "current-user-sid" || !strings.HasPrefix(connection["NamedPipe"].(string), `\\.\pipe\my-vm-`+m.ID) {
				t.Fatal("console security changed", connection)
			}
		}
		for _, invalid := range [][]string{nil, append(endpoints, "extra"), {"", "additional2"}, {"same", "same"}} {
			if _, err := configurationWithEndpoints(`C:\VM`, m, invalid, "sid"); err == nil {
				t.Fatal("invalid endpoints accepted", invalid)
			}
		}
	}
	m.Network, m.NetworkAdapters, m.COMPorts = "none", nil, nil
	cfg, err := configurationWithEndpoints(`C:\VM`, m, nil, "sid")
	if err != nil {
		t.Fatal(err)
	}
	devices := cfg["VirtualMachine"].(map[string]any)["Devices"].(map[string]any)
	if devices["NetworkAdapters"] != nil || devices["ComPorts"] != nil {
		t.Fatal("absent devices emitted", devices)
	}
}
