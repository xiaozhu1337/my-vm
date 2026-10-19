//go:build windows

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func gpuFixture(t *testing.T) (media, destination, manifest string) {
	t.Helper()
	media = t.TempDir()
	destination = t.TempDir()
	const driver = "nvidia.inf_amd64_test"
	const name = "FileRepository/" + driver + "/nvcuda.dll"
	root := filepath.Join(media, "windows-gpu")
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("driver"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest = fmt.Sprintf("architecture: amd64\nadapters:\n  - device_name: Test GPU\n    version: 1.0\n    hardware_id: PCI\\VEN_10DE\n    driver: %s\n    copies:\n      - source: %s\n        destination: System32/nvcuda.dll\n        overwrite: true\nfiles:\n  - name: %s\n    sha256: %x\n", driver, name, name, sha256.Sum256([]byte("driver")))
	if err := os.WriteFile(filepath.Join(root, "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	return
}

func TestWindowsGPUInstallIntegrityAndRepeat(t *testing.T) {
	media, destination, _ := gpuFixture(t)
	changed, err := installWindowsGPU(media, destination, "test gpu")
	if err != nil || !changed {
		t.Fatalf("install: %v %v", changed, err)
	}
	target := filepath.Join(destination, "System32", "nvcuda.dll")
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "driver" {
		t.Fatalf("supplement: %q %v", data, err)
	}
	if changed, err = installWindowsGPU(media, destination, "Test GPU"); err != nil || changed {
		t.Fatalf("repeat: %v %v", changed, err)
	}
	source := filepath.Join(media, "windows-gpu", "FileRepository", "nvidia.inf_amd64_test", "nvcuda.dll")
	if err = os.WriteFile(source, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = installWindowsGPU(media, destination, "Test GPU"); err == nil {
		t.Fatal("accepted corrupt media")
	}
	if data, _ = os.ReadFile(target); string(data) != "driver" {
		t.Fatal("corruption changed target")
	}
}

func TestWindowsGPURejectUnsafeAndConflictingPackages(t *testing.T) {
	media, destination, manifest := gpuFixture(t)
	if _, err := installWindowsGPU(media, destination, "Other GPU"); err == nil {
		t.Fatal("accepted different adapter")
	}
	path := filepath.Join(media, "windows-gpu", "manifest.yaml")
	bad := strings.Replace(manifest, "destination: System32/nvcuda.dll", "destination: System32/../evil.dll", 1)
	if err := os.WriteFile(path, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWindowsGPU(media); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, "System32", "HostDriverStore", "FileRepository", "nvidia.inf_amd64_test", "nvcuda.dll")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := installWindowsGPU(media, destination, "Test GPU"); err == nil {
		t.Fatal("overwrote conflicting package")
	}
	if data, _ := os.ReadFile(target); string(data) != "existing" {
		t.Fatal("existing package changed")
	}
}

func TestWindowsGPUSupplementWhenNewer(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("target"), 0644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(source, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("source")))
	if changed, err := copyGPUSupplement(source, target, digest, false); err != nil || changed {
		t.Fatalf("replaced newer target: %v %v", changed, err)
	}
	if changed, err := copyGPUSupplement(source, target, digest, true); err != nil || !changed {
		t.Fatalf("overwrite: %v %v", changed, err)
	}
}

func TestWindowsGPUCopyPathsIgnoreCase(t *testing.T) {
	media, destination, manifest := gpuFixture(t)
	manifest = strings.Replace(manifest, "source: FileRepository/nvidia.inf_amd64_test/nvcuda.dll", "source: FileRepository/nvidia.inf_amd64_test/NVCUDA.DLL", 1)
	if err := os.WriteFile(filepath.Join(media, "windows-gpu", "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := installWindowsGPU(media, destination, "Test GPU"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "System32", "nvcuda.dll"))
	if err != nil || string(data) != "driver" {
		t.Fatalf("case-insensitive copy: %q %v", data, err)
	}
}

func TestWindowsGPULockedSupplementLeavesAllTargetsUnchanged(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	var records []windowsGPUCopy
	expected := make(map[string]string)
	for _, name := range []string{"first", "locked"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("new"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, name), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
		records = append(records, windowsGPUCopy{Source: name, Destination: name, Overwrite: true})
		expected[name] = fmt.Sprintf("%x", sha256.Sum256([]byte("new")))
	}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(filepath.Join(target, "locked")), windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if changed, err := installGPUSupplements(source, target, records, expected); err == nil || changed {
		t.Fatalf("locked target accepted: %v %v", changed, err)
	}
	for _, record := range records {
		data, err := os.ReadFile(filepath.Join(target, record.Destination))
		if err != nil || string(data) != "old" {
			t.Fatalf("target changed: %s %q %v", record.Destination, data, err)
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 2 {
		t.Fatalf("pending files leaked: %v %v", entries, err)
	}
}

func TestInstalledWindowsGPUPayload(t *testing.T) {
	media := os.Getenv("MY_VM_GPU_MEDIA")
	if media == "" {
		t.Skip("set MY_VM_GPU_MEDIA to validate the host driver payload")
	}
	manifest, _, err := readWindowsGPU(media)
	if err != nil {
		t.Fatal(err)
	}
	directory := os.Getenv("MY_VM_GPU_WINDOWS")
	selected := os.Getenv("MY_VM_GPU_DEVICE")
	if directory != "" && selected == "" {
		t.Fatal("MY_VM_GPU_DEVICE must explicitly select the guest adapter")
	}
	if directory == "" {
		directory = t.TempDir()
	}
	matched := false
	for _, adapter := range manifest.Adapters {
		if selected != "" && adapter.Name != selected {
			continue
		}
		matched = true
		changed, err := installWindowsGPU(media, directory, adapter.Name)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("GPU %s, version %s, installed=%v", adapter.Name, adapter.Version, changed)
	}
	if !matched {
		t.Fatal("selected adapter is absent from the payload")
	}
}
