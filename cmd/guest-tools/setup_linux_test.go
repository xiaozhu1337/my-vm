//go:build linux

package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"testing"
)

func TestPreviousAgentHelper(t *testing.T) {
	if os.Getenv("MY_VM_AGENT_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	io.WriteString(os.Stdout, "ready\n")
	<-signals
	os.Exit(0)
}

func TestStopPreviousAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	target := filepath.Join(root, "my-vm-guest")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, data, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(target, "-test.run=^TestPreviousAgentHelper$")
	child.Env = append(os.Environ(), "MY_VM_AGENT_HELPER=1")
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper startup: %q, %v", line, err)
	}
	if err = stopPreviousAgent(target); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestGPUPresent(t *testing.T) {
	root := t.TempDir()
	if gpuPresent(root) {
		t.Fatal("empty bus reported a GPU")
	}
	directory := filepath.Join(root, "adapter")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "class_id")
	if err := os.WriteFile(file, []byte("{00000000-0000-0000-0000-000000000000}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if gpuPresent(root) {
		t.Fatal("unrelated device reported a GPU")
	}
	if err := os.WriteFile(file, []byte("{6e382d18-3336-4f4b-acc4-2b7703d4df4a}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !gpuPresent(root) {
		t.Fatal("GPU-P transport was not detected")
	}
}
