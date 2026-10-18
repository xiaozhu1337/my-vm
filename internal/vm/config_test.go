package vm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.yaml")
	cfg, err := Load(path)
	if err != nil || len(cfg.Machines) != 0 {
		t.Fatalf("missing config: %v", err)
	}
	machine := NewMachine()
	machine.Name = "Windows 中文测试"
	machine.ISO = `C:\ISO images\安装,光盘.iso`
	want := Config{QEMUDir: `C:\Program Files\qemu`, Machines: []Machine{machine}}
	if err = Save(path, want); err != nil {
		t.Fatal(err)
	}
	// Saving over an existing file must work on Windows as well.
	if err = Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(data), "\xef\xbb\xbf") || !strings.Contains(string(data), "# 虚拟 CPU 数量") {
		t.Fatal("configuration must be commented UTF-8 without BOM")
	}
	if err = os.WriteFile(path, []byte("machines: [broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path); err == nil {
		t.Fatal("must reject damaged YAML")
	}
	data, _ = os.ReadFile(path)
	if string(data) != "machines: [broken" {
		t.Fatal("must preserve damaged file")
	}
}

func TestConfigRejectsUnknownFieldsAndDocuments(t *testing.T) {
	for _, input := range []string{"machine: []\n", "machines: []\n---\nmachines: []\n", "machines:\n  - id: abc\n    unexpected: true\n"} {
		path := filepath.Join(t.TempDir(), "library.yaml")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted invalid config %q", input)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != input {
			t.Fatal("invalid configuration changed")
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
		{"bridge injection", func(m *Machine) { m.Network = "bridge"; m.TAP = "tap,script=evil" }},
		{"accel", func(m *Machine) { m.Accelerator = "kvm" }},
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

func TestArguments(t *testing.T) {
	m := NewMachine()
	m.Name = "Windows 中文,测试"
	m.ISO = `C:\ISO images\安装,光盘.iso`
	root := `C:\VM library,中文`
	args, err := Arguments(root, m, "127.0.0.1:12345")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string][]string{}
	for i := 0; i < len(args); i += 2 {
		values[args[i]] = append(values[args[i]], args[i+1])
	}
	if values["-name"][0] != "guest=Windows 中文,,测试" {
		t.Fatalf("wrong escaped guest name: %v", args)
	}
	if values["-accel"][0] != "whpx" || values["-netdev"][0] != "user,id=net0" {
		t.Fatalf("wrong defaults: %v", args)
	}
	var disk struct {
		File struct {
			Filename string `json:"filename"`
		} `json:"file"`
	}
	if err = json.Unmarshal([]byte(values["-blockdev"][0]), &disk); err != nil {
		t.Fatal(err)
	}
	if disk.File.Filename != filepath.ToSlash(DiskPath(root, m)) {
		t.Fatal("disk path was altered")
	}
	if err = json.Unmarshal([]byte(values["-blockdev"][1]), &disk); err != nil {
		t.Fatal(err)
	}
	if disk.File.Filename != filepath.ToSlash(m.ISO) {
		t.Fatal("ISO path was altered")
	}
	m.Network = "bridge"
	m.TAP = "TAP 测试"
	m.ISO = ""
	args, err = Arguments(root, m, "127.0.0.1:12345")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, "|"), "tap,id=net0,ifname=TAP 测试,script=no,downscript=no") {
		t.Fatalf("bridge missing: %v", args)
	}
}
