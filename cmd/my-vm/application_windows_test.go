//go:build windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	vm "my-vm/library"
)

func TestEditingDefaultsPreserveOriginalVersion(t *testing.T) {
	a := testApplication(t, 1)
	original := a.config.Machines[0]
	if original.Display != "" {
		t.Fatal("fixture must exercise legacy empty display")
	}
	a.edit(original, false)
	a.draft.Name = "Updated"
	if a.persistedDraft.Display != "" || a.draft.Display != "std" {
		t.Fatal("editor defaults replaced original version")
	}
	if err := a.action(vm.Request{Action: "update", VM: original.ID, Machine: &a.draft, Expected: &a.persistedDraft}); err != nil {
		t.Fatal("first GUI edit rejected its own defaults", err)
	}
}

func TestConsoleRuntimeChangesAreObservedAcrossClients(t *testing.T) {
	const id = "vm"
	current := &displayWindow{runtime: "old", automatic: true}
	a := &application{displays: map[string]*displayWindow{id: current}}
	a.manager = vm.NewClient(&http.Client{Transport: guiTestTransport(func(request *http.Request) (*http.Response, error) {
		var input vm.Request
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		switch input.Action {
		case "list":
			json.NewEncoder(response).Encode(vm.Page[vm.Item]{Total: 1, Items: []vm.Item{{ID: id, State: "运行中"}}})
		case "display-info":
			json.NewEncoder(response).Encode(vm.DisplayInfo{RuntimeID: "new"})
		default:
			t.Fatal(input)
		}
		return response.Result(), nil
	})})
	if err := a.manager.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	changes := a.displayRestarts(context.Background())
	if len(changes) != 1 || changes[0].id != id || changes[0].info.RuntimeID != "new" || changes[0].previous != current {
		t.Fatal("CLI runtime replacement did not schedule existing console", changes)
	}
	current.runtime = "new"
	if changes = a.displayRestarts(context.Background()); len(changes) != 0 {
		t.Fatal("unchanged console scheduled for replacement", changes)
	}
	delete(a.displays, id)
	if changes = a.displayRestarts(context.Background()); len(changes) != 0 {
		t.Fatal("closed console reopened", changes)
	}
}

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
