package sharing

import (
	"context"
	"go.yaml.in/yaml/v3"
	"golang.org/x/net/webdav"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenameDoesNotReplace(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"source", "target"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := renameNoReplace(root, "source", "target"); err == nil {
		t.Fatal("existing destination was replaced")
	}
	if data, _ := root.ReadFile("target"); string(data) != "target" {
		t.Fatalf("lost destination: %q", data)
	}
	if err := renameNoReplace(root, "source", "renamed"); err != nil {
		t.Fatal(err)
	}
	if data, _ := root.ReadFile("renamed"); string(data) != "source" {
		t.Fatalf("lost source: %q", data)
	}
	if _, err := root.Stat("source"); !os.IsNotExist(err) {
		t.Fatal("source remains", err)
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestInterruptedUploadPreservesOriginal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file"), []byte("keep"), 0600)
	handler, close, err := Open([]Share{{Name: "RW", Path: dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	request := httptest.NewRequest("PUT", "/dav/RW/file", io.MultiReader(strings.NewReader("partial"), brokenBody{}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code < 400 {
		t.Fatal("partial upload accepted", response.Code)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "file")); string(data) != "keep" {
		t.Fatalf("existing file was damaged: %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary upload leaked", entries)
	}
}

type racingFS struct {
	rootedFS
	created bool
}

func (f *racingFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	info, err := f.rootedFS.Stat(ctx, name)
	if strings.TrimPrefix(name, "/") == "target" && os.IsNotExist(err) && !f.created {
		f.created = true
		file, createErr := f.root.OpenFile("target", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			return nil, createErr
		}
		_, createErr = file.WriteString("concurrent host data")
		file.Close()
		if createErr != nil {
			return nil, createErr
		}
	}
	return info, err
}
func TestCopyDoesNotTruncateConcurrentDestination(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "source"), []byte("guest data"), 0600)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fs := &racingFS{rootedFS: rootedFS{root: root}}
	handler := atomicUploads(&webdav.Handler{Prefix: "/dav/RW", FileSystem: fs, LockSystem: webdav.NewMemLS()}, root, "/dav/RW")
	request := httptest.NewRequest("COPY", "http://localhost/dav/RW/source", nil)
	request.Header.Set("Destination", "http://localhost/dav/RW/target")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code < 400 {
		t.Fatal("concurrent target overwritten", response.Code)
	}
	if data, _ := root.ReadFile("target"); string(data) != "concurrent host data" {
		t.Fatalf("concurrent host data lost: %q", data)
	}
}
func TestShareBoundaryAndDefaultReadOnly(t *testing.T) {
	var share Share
	if err := yaml.Unmarshal([]byte("name: Test\npath: C:/test\n"), &share); err != nil || !share.ReadOnly {
		t.Fatal("missing read_only must remain safe", share, err)
	}
	if err := yaml.Unmarshal([]byte("name: Test\npath: C:/test\nread_ony: false\n"), &share); err == nil {
		t.Fatal("unknown field accepted")
	}
	rootDir, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600)
	if err := os.Symlink(outside, filepath.Join(rootDir, "escape")); err != nil {
		t.Skip("symlink privilege unavailable:", err)
	}
	handler, close, err := Open([]Share{{Name: "Test", Path: rootDir}})
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	for _, method := range []string{"GET", "PUT"} {
		r := httptest.NewRequest(method, "/dav/Test/escape/secret", strings.NewReader("change"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatal("escaped root", method, w.Code)
		}
	}
	if data, err := os.ReadFile(filepath.Join(outside, "secret")); err != nil || string(data) != "private" {
		t.Fatal("outside data changed", err)
	}
}
