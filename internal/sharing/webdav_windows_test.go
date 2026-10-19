//go:build windows

package sharing

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNativeWindowsWebDAVMapping(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 with WebClient already running")
	}
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseServiceHandle(manager)
	service, err := windows.OpenService(manager, windows.StringToUTF16Ptr("WebClient"), windows.SERVICE_QUERY_STATUS)
	if err != nil {
		t.Skip(err)
	}
	defer windows.CloseServiceHandle(service)
	var state windows.SERVICE_STATUS
	if err = windows.QueryServiceStatus(service, &state); err != nil {
		t.Fatal(err)
	}
	if state.CurrentState != windows.SERVICE_RUNNING {
		t.Skip("WebClient is not running; test does not change services")
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "probe.txt"), []byte("DAV parent regression"), 0600); err != nil {
		t.Fatal(err)
	}
	handler, closeRoots, err := Open([]Share{{Name: "Shared1", Path: root, ReadOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoots()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Log(r.Method, r.URL.Path); handler.ServeHTTP(w, r) })}
	defer server.Close()
	go server.Serve(listener)
	remote := `\\127.0.0.1@` + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + `\DavWWWRoot\dav\Shared1`
	mpr := windows.NewLazySystemDLL("mpr.dll")
	mask, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetLogicalDrives").Call()
	drive := ""
	for letter := byte('Z'); letter >= 'D'; letter-- {
		if mask&(1<<uint(letter-'A')) != 0 {
			continue
		}
		candidate := string(letter) + ":"
		var buffer [1024]uint16
		size := uint32(len(buffer))
		code, _, _ := mpr.NewProc("WNetGetConnectionW").Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(candidate))), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
		if code == 2250 {
			drive = candidate
			break
		}
	}
	if drive == "" {
		t.Fatal("no unused drive for temporary fixture")
	}
	resource := struct {
		Scope, Type, DisplayType, Usage  uint32
		Local, Remote, Comment, Provider *uint16
	}{Type: 1, Local: windows.StringToUTF16Ptr(drive), Remote: windows.StringToUTF16Ptr(remote)}
	code, _, _ := mpr.NewProc("WNetAddConnection2W").Call(uintptr(unsafe.Pointer(&resource)), 0, 0, 4)
	if code != 0 {
		t.Fatalf("WNetAddConnection2 %s: %v", remote, windows.Errno(code))
	}
	defer func() {
		code, _, _ := mpr.NewProc("WNetCancelConnection2W").Call(uintptr(unsafe.Pointer(resource.Local)), 0, 0)
		if code != 0 {
			t.Errorf("disconnect fixture: %v", windows.Errno(code))
		}
	}()
	file, err := os.Open(drive + `\probe.txt`)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "DAV parent regression" {
		t.Fatalf("read: %q %v", data, err)
	}
}
