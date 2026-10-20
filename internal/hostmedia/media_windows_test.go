//go:build windows

package hostmedia

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
	"my-vm/internal/media"
)

func testFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func testStage(t *testing.T) *staging {
	t.Helper()
	stage, err := newStaging(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stage.cleanup(); err != nil {
			t.Error(err)
		}
	})
	return stage
}

func TestHostPathsAndFailedCopy(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", `C:/escape`, `dir\file`, "file:stream", "CON", "a/../b", "a//b", "a/.", "a/b.", "line\nbreak"} {
		if safeRelative(name) {
			t.Errorf("accepted %q", name)
		}
	}
	stage := testStage(t)
	if _, err := stage.copy(filepath.Join(t.TempDir(), "missing"), "nested/file"); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(filepath.Join(stage.root, "nested/file")); !os.IsNotExist(err) {
		t.Fatal("failed copy published a file")
	}
}

func TestWindowsRuntimeExcludesNonGuestFiles(t *testing.T) {
	stage := testStage(t)
	driver := t.TempDir()
	keep := []string{"driver.dll", "NVAPI64.DLL", "driver.inf", "driver.cat", "nvcubins.bin", "gsp_ga10x.bin", "vulkan.json", "amdvlk_nexus.cab"}
	skip := []string{"nvlddmkm.sys", "AMDInstallManager.msi", "driver.PNF", "debug.log", "libamdenc64.so", "libcuda.so.1", "nvidia-smi", "nvidia-ngx-updater"}
	for _, name := range append(append([]string{}, keep...), skip...) {
		testFile(t, filepath.Join(driver, name), "test file")
	}
	if err := collectWindows(stage, []Adapter{{Name: "GPU", Driver: "test.inf_amd64", directory: driver}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stage.root, "windows-gpu", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest windowsManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != len(keep) {
		t.Fatalf("unexpected runtime payload: %+v", manifest.Files)
	}
	for _, name := range skip {
		if _, err := os.Stat(filepath.Join(stage.root, "windows-gpu", "FileRepository", "test.inf_amd64", name)); !os.IsNotExist(err) {
			t.Fatalf("unnecessary guest file %s retained: %v", name, err)
		}
	}
}

func TestWindowsPayloadRoundTripAndMissingSupplement(t *testing.T) {
	stage := testStage(t)
	driver := t.TempDir()
	source := filepath.Join(driver, "nested", "driver.dll")
	testFile(t, source, "current host driver")
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(source, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	adapters := []Adapter{{Name: "Test GPU", Version: "1.2.3", Driver: "test.inf_amd64", HardwareID: `PCI\VEN_10DE`, directory: driver, Copies: []Copy{{Source: "FileRepository/test.inf_amd64/nested/driver.dll", Destination: "System32/driver.dll"}}}}
	if err := collectWindows(stage, adapters); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stage.root, "windows-gpu", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest windowsManifest
	if err = yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Adapters) != 1 || len(manifest.Files) != 1 {
		t.Fatalf("invalid payload: %+v", manifest)
	}
	copied := filepath.Join(stage.root, "windows-gpu", filepath.FromSlash(manifest.Files[0].Name))
	digest, err := fileDigest(copied)
	if err != nil || digest != manifest.Files[0].SHA256 {
		t.Fatal("copied driver checksum mismatch", err)
	}
	info, err := os.Stat(copied)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatal("driver timestamp changed", err)
	}
	adapters[0].Copies[0].Source = "FileRepository/test.inf_amd64/missing.dll"
	other := testStage(t)
	if err = collectWindows(other, adapters); err == nil {
		t.Fatal("missing declared supplement silently accepted")
	}
}

func TestISOExtractionRebuildPreservesSource(t *testing.T) {
	sourceDir := t.TempDir()
	testFile(t, filepath.Join(sourceDir, "install.exe"), "test installer")
	testFile(t, filepath.Join(sourceDir, "linux", "nested", "测试.txt"), "test payload")
	testFile(t, filepath.Join(sourceDir, "茅.txt"), "ambiguous ANSI bytes")
	source := filepath.Join(t.TempDir(), "generic.iso")
	if err := media.CreateISO(sourceDir, source); err != nil {
		t.Fatal(err)
	}
	before, err := fileDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	stage := testStage(t)
	if err = stage.extract(source); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(stage.root, "linux", "nested", "测试.txt")
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "test payload" {
		t.Fatal("ISO extraction lost nested/unicode file", err)
	}
	if data, err = os.ReadFile(filepath.Join(stage.root, "茅.txt")); err != nil || string(data) != "ambiguous ANSI bytes" {
		t.Fatal("ISO extraction guessed UTF-8 for ambiguous ANSI bytes", err)
	}
	driver := filepath.Join(t.TempDir(), "gpu.dll")
	testFile(t, driver, "host matched")
	if _, err = stage.copy(driver, "windows-gpu/FileRepository/test/gpu.dll"); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "matched.iso")
	if err = media.CreateISO(stage.root, result); err != nil {
		t.Fatal(err)
	}
	names, err := archiveCommand("-tf", result)
	if err != nil || !strings.Contains(string(names), "windows-gpu/FileRepository/test/gpu.dll") {
		t.Fatal("new ISO missing driver", err)
	}
	after, err := fileDigest(source)
	if err != nil || before != after {
		t.Fatal("original image modified", err)
	}
	rejected := testStage(t)
	if err = rejected.extract(result); err == nil {
		t.Fatal("prepackaged host drivers accepted as a generic image")
	}
}

func TestISOExtractionWithOnlyAmbiguousANSIName(t *testing.T) {
	root := t.TempDir()
	testFile(t, filepath.Join(root, "install.exe"), "installer")
	testFile(t, filepath.Join(root, "茅.txt"), "ambiguous name")
	image := filepath.Join(t.TempDir(), "ambiguous.iso")
	if err := media.CreateISO(root, image); err != nil {
		t.Fatal(err)
	}
	stage := testStage(t)
	if err := stage.extract(image); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stage.root, "茅.txt"))
	if err != nil || string(data) != "ambiguous name" {
		t.Fatal("ANSI filename was mistaken for UTF-8", err)
	}
}

func TestNativeInstalledAdapters(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 to inspect installed display drivers")
	}
	adapters, err := InstalledAdapters()
	if err != nil {
		t.Fatal(err)
	}
	for _, adapter := range adapters {
		if adapter.Name == "" || adapter.Version == "" {
			t.Fatalf("incomplete adapter: %+v", adapter)
		}
		if _, err = os.Stat(adapter.inf); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %s: %s (%d CopyToVm mappings)", adapter.Name, adapter.Version, adapter.Driver, len(adapter.Copies))
	}
}

func TestNativeHostMedia(t *testing.T) {
	platform := os.Getenv("MY_VM_HOST_MEDIA")
	if platform == "" {
		t.Skip("set MY_VM_HOST_MEDIA=windows or linux for a real driver/media round trip")
	}
	source, err := filepath.Abs(filepath.Join("..", "..", "dist", "my-vm-guest-tools-"+platform+".iso"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := Prepare(source, platform, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	names, err := archiveCommand("-tf", target)
	if err != nil {
		t.Fatal(err)
	}
	expected := "windows-gpu/manifest.yaml"
	if platform == "linux" {
		expected = "gpu/manifest.yaml"
	}
	if !strings.Contains(string(names), expected) {
		t.Fatal("host driver manifest missing")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s media prepared: %d bytes", platform, info.Size())
}
