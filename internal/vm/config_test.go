package vm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
	cfg, err := Configuration(`C:\VM,中文`, m, "endpoint-test")
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
