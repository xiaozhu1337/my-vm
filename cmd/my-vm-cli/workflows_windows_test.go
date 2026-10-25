//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	vm "my-vm/library"
)

func TestCommandMappings(t *testing.T) {
	c := &commands{offset: 3, limit: 5}
	cases := []struct {
		args             []string
		action, vm, name string
	}{
		{[]string{"list"}, "list", "", ""},
		{[]string{"start", "VM"}, "start", "VM", ""},
		{[]string{"remove", "VM"}, "remove", "VM", ""},
		{[]string{"clone", "VM", "Copy"}, "clone", "VM", "Copy"},
		{[]string{"snapshot", "list", "VM"}, "snapshots", "VM", ""},
		{[]string{"snapshot", "restore", "VM", "before"}, "snapshot-restore", "VM", "before"},
		{[]string{"memory", "VM"}, "memory", "VM", ""},
		{[]string{"enhancements", "VM"}, "enhancements", "VM", ""},
		{[]string{"media", "mount", "VM", "disc.iso"}, "media-mount", "VM", ""},
		{[]string{"network", "list"}, "networks", "", ""},
		{[]string{"network", "create", "Dev", "192.0.2.0/24"}, "network-create", "", "Dev"},
		{[]string{"network", "delete", "ID"}, "network-delete", "", "ID"},
		{[]string{"settings", "set", "side-channel-mitigations", "true"}, "settings-update", "", ""},
		{[]string{"adapters"}, "adapters", "", ""},
		{[]string{"shutdown-all"}, "shutdown-all", "", ""},
	}
	for _, test := range cases {
		request, err := c.action(test.args)
		if err != nil || request.Action != test.action || request.VM != test.vm || request.Name != test.name || request.Offset != 3 || request.Limit != 5 {
			t.Fatal(test, request, err)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"clone", "VM"}, {"network", "delete"}, {"snapshot", "create", "VM"}, {"memory", "set", "VM", "-1"}, {"memory", "set", "VM", "8192"}, {"memory"}, {"settings", "set", "side-channel-mitigations", "maybe"}, {"get"}, {"create"}, {"update", "VM"}} {
		if _, err := c.action(args); err == nil {
			t.Fatal("accepted malformed command", args)
		}
	}
}

func TestCloneSourceCommandMappings(t *testing.T) {
	c := &commands{}
	for _, state := range []string{"@current", "before-update", "ac53b22f6612ef42d12047faf06c4299"} {
		request, err := c.action([]string{"clone", "VM", "Copy", state})
		if err != nil || request.Snapshot != state || request.Action != "clone" || request.Name != "Copy" {
			t.Fatal(request, err)
		}
	}
	for _, args := range [][]string{{"clone", "VM", "Copy", ""}, {"clone", "VM", "Copy", "@current", "extra"}} {
		if _, err := c.action(args); err == nil {
			t.Fatal("accepted malformed clone selection", args)
		}
	}
}

func TestCreateImportsFixedMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	for _, legacy := range []string{"", "dynamic_memory: true\nmax_memory_mib: 32768\n", "dynamic_memory: false\nmax_memory_mib: 0\n"} {
		data := "memory_mib: 16384\n" + legacy
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		request, err := (&commands{}).action([]string{"create", path})
		if err != nil || request.Machine.Validate() != nil || request.Machine.MemoryMiB != 16384 {
			t.Fatal("fixed memory import changed", request.Machine, err)
		}
		encoded, err := yaml.Marshal(request.Machine)
		if err != nil || strings.Contains(string(encoded), "dynamic_memory") || strings.Contains(string(encoded), "max_memory_mib") {
			t.Fatal("retired fields survived import", string(encoded), err)
		}
	}
}

func TestStructuredConfigurationWorkflow(t *testing.T) {
	root := t.TempDir()
	machine := vm.NewMachine()
	machine.Name = "Original"
	if err := vm.Save(filepath.Join(root, "library.yaml"), vm.Config{Machines: []vm.Machine{machine}}); err != nil {
		t.Fatal(err)
	}
	service := vm.NewService(root, nil)
	client := vm.NewClient(&http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		service.ServeHTTP(response, r)
		return response.Result(), nil
	})})
	var stdout, stderr bytes.Buffer
	command := &commands{ctx: context.Background(), client: client, stdout: &stdout, stderr: &stderr, format: "yaml", limit: 20}
	patch := filepath.Join(root, "machine.yaml")
	if err := os.WriteFile(patch, []byte("# Updated CPU and network configuration.\nname: Changed\ncpus: 4\nnetwork: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, err := command.execute([]string{"update", machine.ID, patch}); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	var yamlResult struct {
		OK   bool `yaml:"ok"`
		Data struct {
			Name    string `yaml:"name"`
			CPUs    int    `yaml:"cpus"`
			Network string `yaml:"network"`
		} `yaml:"data"`
	}
	if err := yaml.Unmarshal(stdout.Bytes(), &yamlResult); err != nil || !yamlResult.OK || yamlResult.Data.Name != "Changed" || yamlResult.Data.CPUs != 4 || yamlResult.Data.Network != "none" {
		t.Fatal("YAML result is not a structured mapping", stdout.String(), err)
	}
	stdout.Reset()
	command.format = "json"
	if code, err := command.execute([]string{"get", "Changed"}); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	var result struct {
		OK   bool       `json:"ok"`
		Data vm.Machine `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !result.OK || result.Data.ID != machine.ID || result.Data.MemoryMiB != machine.MemoryMiB {
		t.Fatal(stdout.String(), err)
	}
	if err := os.WriteFile(patch, []byte("# Reject misspelled fields.\ncpuss: 9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := command.execute([]string{"update", machine.ID, patch}); err == nil {
		t.Fatal("unknown YAML field accepted")
	}
	if err := os.WriteFile(patch, []byte("name: First\n---\nname: Second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := command.execute([]string{"update", machine.ID, patch}); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
}

func TestExportedMachineCanBeImported(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			command := &commands{format: format, stdout: &out}
			if code, err := command.execute([]string{"template"}); code != 0 || err != nil {
				t.Fatal(code, err)
			}
			machine := vm.NewMachine()
			if err := readMachine(&out, &machine); err != nil || machine.Validate() != nil {
				t.Fatal(machine, err)
			}
			machine.Shares = []vm.Share{{Name: "Work", Path: t.TempDir(), ReadOnly: false}}
			if err := command.show(machine); err != nil {
				t.Fatal(err)
			}
			var imported vm.Machine
			if err := readMachine(&out, &imported); err != nil || !reflect.DeepEqual(machine, imported) {
				t.Fatal("export/import changed machine", imported, err)
			}
		})
	}
	for _, data := range []string{"ok: true\n", "ok: true\ndata: null\n", "ok: false\ndata: {}\n", "ok: true\ndata: {cpuss: 9}\n", "name: First\n---\nname: Second\n", strings.Repeat(" ", (1<<20)+1)} {
		machine := vm.NewMachine()
		if err := readMachine(strings.NewReader(data), &machine); err == nil {
			t.Fatal("malformed import accepted")
		}
	}
}

func TestCLIUsageErrorsAndGuestStructuredExit(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"--limit", "101", "list"}, {"--timeout", "0", "list"}, {"--format", "bad", "list"}, {"snapshot", "create", "VM"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatal(args, code, out.String(), errOut.String())
		}
		var failure struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
			Exit  int    `json:"exit_code"`
		}
		if err := json.Unmarshal(out.Bytes(), &failure); err != nil || failure.OK || failure.Error == "" || failure.Exit != 2 {
			t.Fatal(args, out.String(), err)
		}
	}
	var out, errOut bytes.Buffer
	c := &commands{ctx: context.Background(), stdout: &out, stderr: &errOut, format: "json", client: vm.NewClient(&http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		json.NewEncoder(response).Encode(vm.CommandResult{ExitCode: 7, Stdout: []byte("out"), Stderr: []byte("err")})
		return response.Result(), nil
	})})}
	if code, err := c.execute([]string{"exec", "VM", "program"}); err != nil || code != 7 {
		t.Fatal(code, err)
	}
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			Exit           int `json:"exit_code"`
			Stdout, Stderr string
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.OK || result.Data.Exit != 7 || result.Data.Stdout != "out" || result.Data.Stderr != "err" || errOut.Len() != 0 {
		t.Fatal(out.String(), errOut.String(), err)
	}
}
