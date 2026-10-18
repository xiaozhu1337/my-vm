//go:build windows

package media

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateISOAndPreserveExistingImage(t *testing.T) {
	directory := t.TempDir()
	payload := []byte("MYVM_GUEST_MEDIA_TEST_PAYLOAD")
	if err := os.WriteFile(filepath.Join(directory, "tools.txt"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "tools.iso")
	if err := CreateISO(directory, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 32774 || string(data[32769:32774]) != "CD001" || !bytes.Contains(data, payload) {
		t.Fatal("invalid ISO or missing tool content")
	}
	if err = CreateISO(directory, target); err == nil {
		t.Fatal("existing image replaced")
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, after) {
		t.Fatal("existing image changed")
	}
}
