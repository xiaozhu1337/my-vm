//go:build windows

package main

import (
	"testing"

	"my-vm/internal/vm"
)

func TestUnsavedChanges(t *testing.T) {
	m := vm.NewMachine()
	a := &application{}
	a.edit(m, true)
	if a.hasUnsavedChanges() {
		t.Fatal("a new untouched form must not need a discard confirmation")
	}
	a.draft.Name = "Changed"
	if !a.hasUnsavedChanges() {
		t.Fatal("name edit was not detected")
	}
	a.edit(m, false)
	a.memory += 256
	if !a.hasUnsavedChanges() {
		t.Fatal("numeric edit was not detected")
	}
	a.memory = float64(m.MemoryMiB)
	if a.hasUnsavedChanges() {
		t.Fatal("reverting an edit must clear the unsaved state")
	}
	a.page = "settings"
	if a.hasUnsavedChanges() {
		t.Fatal("read-only host settings must not need confirmation")
	}
}
