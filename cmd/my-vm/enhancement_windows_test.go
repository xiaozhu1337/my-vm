//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"my-vm/internal/vm"
)

func TestEnhancementDiscMatchesGuestSystem(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(executable)
	for _, platform := range []string{"windows", "linux"} {
		path := filepath.Join(directory, "my-vm-guest-tools-"+platform+".iso")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		t.Cleanup(func() { os.Remove(path) })
	}
	for _, guest := range []struct{ system, platform string }{
		{"", "windows"}, {"windows", "windows"}, {"windows10", "windows"},
		{"windows11", "windows"}, {"linux", "linux"}, {"ubuntu26", "linux"},
	} {
		t.Run(guest.system, func(t *testing.T) {
			path, err := enhancementDisc(vm.Machine{GuestOS: guest.system})
			want := filepath.Join(directory, "my-vm-guest-tools-"+guest.platform+".iso")
			if err != nil || path != want {
				t.Fatalf("selected %q, want %q: %v", path, want, err)
			}
		})
	}
	linux := filepath.Join(directory, "my-vm-guest-tools-linux.iso")
	if err := os.Remove(linux); err != nil {
		t.Fatal(err)
	}
	if _, err := enhancementDisc(vm.Machine{GuestOS: "ubuntu26"}); err == nil {
		t.Fatal("missing Linux disc silently accepted another platform")
	}
}
