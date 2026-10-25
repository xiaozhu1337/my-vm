package sharing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestShareMetadataHidesHostPaths(t *testing.T) {
	data, err := json.Marshal([]Share{{Name: "Work", Path: t.TempDir(), ReadOnly: true}})
	if err != nil || strings.Contains(string(data), "path") || !strings.Contains(string(data), `"name":"Work"`) {
		t.Fatal("guest metadata exposed host configuration", string(data), err)
	}
}

func TestRootedShares(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "original.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, closeRoots, err := Open([]Share{{Name: "ReadOnly", Path: root, ReadOnly: true}, {Name: "Writable", Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoots()
	check := func(method, path, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d (%s), want %d", method, path, w.Code, w.Body.String(), want)
		}
		return w.Body.String()
	}
	if body := check("GET", "/dav/ReadOnly/original.txt", "", http.StatusOK); body != "original" {
		t.Fatal(body)
	}
	check("PUT", "/dav/ReadOnly/original.txt", "blocked", http.StatusForbidden)
	check("PUT", "/dav/Writable/original.txt", "updated", http.StatusCreated)
	if body := check("GET", "/dav/Writable/original.txt", "", http.StatusOK); body != "updated" {
		t.Fatal(body)
	}
	check("PUT", "/dav/Writable/new.txt", "new", http.StatusCreated)
	check("DELETE", "/dav/Writable/new.txt", "", http.StatusNoContent)
}
