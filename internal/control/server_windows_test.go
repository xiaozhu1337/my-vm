//go:build windows

package control

import (
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"my-vm/internal/vm"
)

func TestGuestStatusSubscriptionDoesNotEnterOperationGuard(t *testing.T) {
	server := &Server{manager: vm.NewManager(t.TempDir(), io.Discard), Allow: func(string, string) error { t.Fatal("status used operation guard"); return nil }, Changed: func(string) { t.Fatal("status caused configuration change") }}
	for _, method := range []string{"GET", "POST"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(method, "/v1/guest-status?vm=missing&runtime=old", nil))
		want := 409
		if method == "POST" {
			want = 405
		}
		if response.Code != want || server.Active() {
			t.Fatal(response.Code, server.Active())
		}
	}
}

func TestSelectionPaginationAndReadOnlyState(t *testing.T) {
	root := t.TempDir()
	first, second := vm.NewMachine(), vm.NewMachine()
	first.Name = "Same"
	second.Name = "same"
	if _, err := selectMachine([]vm.Machine{first, second}, "SAME"); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	if got, err := selectMachine([]vm.Machine{first, second}, first.ID); err != nil || got.ID != first.ID {
		t.Fatal(got, err)
	}
	if err := vm.Save(filepath.Join(root, "library.yaml"), vm.Config{Machines: []vm.Machine{first, second}}); err != nil {
		t.Fatal(err)
	}
	called, finished := 0, 0
	server := &Server{manager: vm.NewManager(root, io.Discard), Allow: func(string, string) error { called++; return nil }, Changed: func(id string) {
		if id != first.ID {
			t.Error("finished wrong machine", id)
		}
		finished++
	}}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", "/v1/action?action=list&offset=1&limit=1", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), second.ID) || strings.Contains(response.Body.String(), first.ID) {
		t.Fatal(response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", "/v1/action?action=state&vm="+first.ID, nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "已关机") || called != 0 {
		t.Fatal(response.Code, response.Body.String(), called)
	}
	response = httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("POST", "/v1/action?action=invalid&vm="+first.ID, nil))
	if response.Code != 400 || called != 1 || finished != 1 || server.Active() {
		t.Fatal(response.Code, called, finished, server.Active())
	}
}
