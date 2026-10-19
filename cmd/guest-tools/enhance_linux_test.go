//go:build linux

package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGNOMEManagedCredentialUsesStdin(t *testing.T) {
	directory := t.TempDir()
	arguments, secret := filepath.Join(directory, "args"), filepath.Join(directory, "secret")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MY_VM_TEST_ARGS\"\ncat > \"$MY_VM_TEST_SECRET\"\n"
	if err := os.WriteFile(filepath.Join(directory, "grdctl"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+":"+os.Getenv("PATH"))
	t.Setenv("MY_VM_TEST_ARGS", arguments)
	t.Setenv("MY_VM_TEST_SECRET", secret)
	var previous string
	for range 2 {
		if err := configureGNOMELogin(); err != nil {
			t.Fatal(err)
		}
		args, err := os.ReadFile(arguments)
		if err != nil || string(args) != "rdp\nset-credentials\nmyvm\n" {
			t.Fatal("password was exposed in argv", err)
		}
		data, err := os.ReadFile(secret)
		if err != nil || len(data) != 44 || data[43] != '\n' || strings.TrimSpace(string(data)) == previous {
			t.Fatal("missing or reused generated secret", err)
		}
		previous = strings.TrimSpace(string(data))
	}
	if err := os.WriteFile(filepath.Join(directory, "grdctl"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := configureGNOMELogin(); err == nil || strings.Contains(err.Error(), previous) {
		t.Fatal("failed configuration was hidden or leaked the secret")
	}
}

func TestUbuntuVersion(t *testing.T) {
	for _, item := range []struct{ data, want string }{
		{"ID=ubuntu\nVERSION_ID=\"26.04\"\n", "26.04"},
		{"ID=ubuntu\nVERSION_ID='26.10'\nNAME=Kubuntu\n", "26.10"},
		{"ID=debian\nID_LIKE=ubuntu\nVERSION_ID=26.04\n", ""},
		{"ID=ubuntu\nVERSION_ID=24.04\n", ""},
	} {
		got, err := ubuntuVersion([]byte(item.data))
		if got != item.want || (err != nil) != (item.want == "") {
			t.Fatalf("%q: %q, %v", item.data, got, err)
		}
	}
}

func TestWaylandDesktop(t *testing.T) {
	for _, item := range []struct{ value, want string }{
		{"ubuntu:GNOME", "gnome"}, {"KDE", "kde"}, {"plasma", "kde"}, {"XFCE", ""},
	} {
		got, err := waylandDesktop(item.value)
		if got != item.want || (err != nil) != (item.want == "") {
			t.Fatalf("%q: %q, %v", item.value, got, err)
		}
	}
}

func TestKDEListenerRequiresOwnedProcessSocket(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"42/fd", "net"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"42/exe": "/usr/bin/krdpserver", "42/fd/10": "socket:[12345]"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	table := filepath.Join(root, "net", "tcp")
	line := "0: 0100007F:A97D 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 12345\n"
	if err := os.WriteFile(table, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := kdeListener(root, 42, 43389); err != nil || got != "127.0.0.1:43389" {
		t.Fatalf("%q, %v", got, err)
	}
	if err := os.WriteFile(table, []byte("0: 0100007F:A97D 00000000:0000 0A 0 0 0 1000 0 99999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := kdeListener(root, 42, 43389); err == nil {
		t.Fatal("forwarded a listener belonging to another process")
	}
}

func TestDesktopCertificatePersists(t *testing.T) {
	directory := t.TempDir()
	cert, key, err := desktopCertificate(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tls.LoadX509KeyPair(cert, key); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = desktopCertificate(directory); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(cert)
	if err != nil || string(before) != string(after) {
		t.Fatalf("replaced a valid trusted certificate: %v", err)
	}
	info, err := os.Stat(key)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private key permissions: %v, %v", info, err)
	}
	if err = checkDigest(cert, "incorrect"); err == nil {
		t.Fatal("accepted a corrupted payload digest")
	}
}
