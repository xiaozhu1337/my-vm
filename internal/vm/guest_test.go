package vm

import (
	"path/filepath"
	"testing"
)

func TestGuestProfilesAndLegacyConfiguration(t *testing.T) {
	for _, test := range []struct {
		id, name, desktop string
		linux             bool
	}{
		{"windows10", "Windows 10", "x11", false},
		{"windows11", "Windows 11", "x11", false},
		{"ubuntu26", "Ubuntu 26", "wayland", true},
		{"windows", "Windows（其他版本）", "x11", false},
		{"linux", "Linux（其他发行版）", "x11", true},
		{"", "Windows（其他版本）", "x11", false},
	} {
		t.Run(test.id, func(t *testing.T) {
			m := NewMachine()
			m.GuestOS = test.id
			if err := m.Validate(); err != nil {
				t.Fatal(err)
			}
			if m.GuestName() != test.name || m.IsLinux() != test.linux || m.DesktopMode() != test.desktop {
				t.Fatalf("wrong profile for %q: %#v", test.id, m)
			}
			path := filepath.Join(t.TempDir(), "library.yaml")
			if err := Save(path, Config{Machines: []Machine{m}}); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if err != nil || len(got.Machines) != 1 || got.Machines[0].GuestOS != test.id {
				t.Fatalf("profile changed on disk: %#v, %v", got, err)
			}
		})
	}
}

func TestLinuxDesktopValidation(t *testing.T) {
	m := NewMachine()
	m.GuestOS = "linux"
	m.LinuxDesktop = "wayland"
	if err := m.Validate(); err != nil || m.DesktopMode() != "wayland" {
		t.Fatal("explicit Wayland selection rejected", err)
	}
	m.GuestOS = "ubuntu26"
	m.LinuxDesktop = "x11"
	if err := m.Validate(); err != nil || m.DesktopMode() != "x11" {
		t.Fatal("explicit X11 selection rejected", err)
	}
	for _, change := range []func(){
		func() { m.LinuxDesktop = "unknown" },
		func() { m.LinuxDesktop = "wayland"; m.GuestOS = "windows11" },
		func() { m.LinuxDesktop = ""; m.GuestOS = "unknown" },
	} {
		change()
		if m.Validate() == nil {
			t.Fatal("invalid guest or desktop accepted", m)
		}
	}
}
