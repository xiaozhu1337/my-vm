//go:build windows

package console

import (
	"bytes"
	"encoding/json"
	"io"
	"my-vm/internal/guestops"
	"net/http"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"
)

func TestProtocolAvailabilityRespectsModeAndState(t *testing.T) {
	for _, test := range []struct{ os, mode, state, kind, want string }{{"linux", "wayland", "unknown", "linux", "unknown"}, {"linux", "wayland", "ready", "windows", "unsupported"}, {"windows", "windows", "ready", "windows", "ready"}, {"windows", "windows", "available", "windows", "available"}, {"linux", "wayland", "waiting", "linux", "waiting"}, {"linux", "wayland", "ready", "x11", "unsupported"}, {"windows", "windows", "ready", "linux", "unsupported"}, {"linux", "x11", "unsupported", "linux", "unsupported"}} {
		status := guestops.Status{Protocol: 1, OS: test.os, Desktop: guestops.DesktopStatus{Mode: test.mode, State: test.state}}
		if test.want == "unsupported" || test.want == "unknown" {
			bar := &consoleToolbar{availability: enhancedAvailability{results: make(chan string, 1)}}
			bar.availability.results <- protocolAvailability(status, test.kind)
			v := &viewer{linux: test.os == "linux", toolbar: bar, display: displayUpdate{connected: true}, log: io.Discard}
			v.enhancedAvailabilityResult()
			if bar.prompt != promptNone {
				t.Fatal("mismatched guest triggered an invitation")
			}
		}
		if got := protocolAvailability(status, test.kind); got != test.want {
			t.Fatalf("status mode mapping got %s want %s", got, test.want)
		}
	}
}

func TestLinuxDisplayUsesProtocolBeforeLegacyCommands(t *testing.T) {
	for _, test := range []struct {
		os, mode, state, kind string
		fail                  bool
	}{
		{"linux", "x11", "ready", "linux", false}, {"linux", "wayland", "ready", "linux", false},
		{"linux", "wayland", "waiting", "linux", true}, {"linux", "wayland", "unknown", "linux", true},
		{"linux", "wayland", "ready", "x11", true}, {"windows", "windows", "ready", "linux", true},
	} {
		calls := 0
		client := &http.Client{Transport: desktopTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.Method != "GET" || r.URL.Path != "/v1/status" {
				t.Fatal("new agent state used a legacy command")
			}
			data, _ := json.Marshal(guestops.Status{Protocol: 1, OS: test.os, Desktop: guestops.DesktopStatus{Mode: test.mode, State: test.state}})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})}
		got, err := displayFromGuest(client, test.kind)
		if (err != nil) != test.fail || calls != 1 || !test.fail && got.Mode != test.mode || got.Username != "" || len(got.Password) != 0 {
			t.Fatalf("wrong status selection: mode=%s error=%v", got.Mode, err)
		}
	}
	for _, code := range []int{404, 501} {
		calls := 0
		client := &http.Client{Transport: desktopTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("old agent"))}, nil
			}
			if calls != 2 || r.Method != "POST" || r.URL.Path != "/exec" {
				t.Fatal("invalid legacy fallback")
			}
			data, _ := json.Marshal(guestops.Result{Stdout: []byte("wayland\nRDP:\n Status: enabled\n Username: myvm\n Password: old-keyring\n")})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})}
		got, err := displayFromGuest(client, "linux")
		if err != nil || calls != 2 || got.Mode != "wayland" || string(got.Password) != "old-keyring" {
			t.Fatal("legacy GNOME credentials were lost", err)
		}
		login := got.login()
		if login == nil {
			t.Fatal("missing legacy login")
		}
		login.clear()
	}
}

func TestLinuxSwitchNotReadyKeepsExistingViewer(t *testing.T) {
	v := &viewer{linux: true, vmid: "not-ready", pipe: `\\.\pipe\my-vm-example.basic`, log: io.Discard}
	v.switchDisplay(true, true)
	if v.closed || v.enhanced || v.pipe != `\\.\pipe\my-vm-example.basic` || !strings.Contains(v.status, "保留当前画面") {
		t.Fatal("failed readiness query destroyed the current viewer")
	}
}

func TestDisplayCapabilityDoesNotMixRDPAndVNCPasswords(t *testing.T) {
	for _, test := range []struct {
		input, mode, name, password string
		fail                        bool
	}{
		{"windows\r\n", "windows", "", "", false},
		{"x11\n", "x11", "", "", false},
		{"wayland\n", "wayland", "", "", false},
		{"wayland\nRDP:\n\tStatus: enabled\n\tUsername: myvm\n\tPassword: secret with spaces\nVNC:\n\tPassword: wrong\n", "wayland", "myvm", "secret with spaces", false},
		{"wayland\nRDP:\n\tStatus: disabled\n\tUsername: myvm\n\tPassword: secret\n", "", "", "", true},
		{"wayland\nRDP:\n\tStatus: enabled\n\tUsername: myvm\n", "", "", "", true},
		{"tty\n", "", "", "", true},
	} {
		got, err := parseDisplay([]byte(test.input))
		if (err != nil) != test.fail || got.Mode != test.mode || got.Username != test.name || string(got.Password) != test.password {
			t.Fatalf("unexpected capability shape, mode=%q error=%v", got.Mode, err)
		}
		if test.password != "" {
			login := got.login()
			if login == nil || string(utf16.Decode(login.password)) != test.password || !bytes.Equal(got.Password, make([]byte, len(got.Password))) {
				t.Fatal("credential handoff did not copy/erase the transport password")
			}
			login.clear()
		}
	}
}

func TestDisplayProbeRejectsFailedAndMismatchedDesktop(t *testing.T) {
	for _, test := range []struct {
		kind, mode      string
		exit            int
		truncated, fail bool
	}{
		{"linux", "x11", 0, false, false},
		{"linux", "wayland", 0, false, false}, {"x11", "wayland", 0, false, true},
		{"wayland", "x11", 0, false, true}, {"windows", "wayland", 0, false, true},
		{"linux", "windows", 0, false, true}, {"linux", "wayland", 1, false, true},
		{"linux", "wayland", 0, true, true},
	} {
		client := &http.Client{Transport: desktopTransport(func(r *http.Request) (*http.Response, error) {
			var command guestops.Command
			if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
				t.Fatal(err)
			}
			if r.URL.Path != "/exec" || command.Timeout != 2 {
				t.Fatal("unbounded or unrelated query")
			}
			if test.kind != "windows" && (len(command.Args) != 3 || command.Args[0] != "/bin/sh" || command.Args[2] != linuxDisplayQuery) {
				t.Fatal("unexpected guest command")
			}
			data, _ := json.Marshal(guestops.Result{ExitCode: test.exit, Stdout: []byte(test.mode + "\n"), Truncated: test.truncated})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
		})}
		_, err := legacyDisplayFromGuest(client, test.kind)
		if (err != nil) != test.fail {
			t.Fatalf("kind=%s mode=%s error=%v", test.kind, test.mode, err)
		}
	}
	if !strings.Contains(linuxDisplayQuery, `loginctl show-session auto -p LockedHint --value`) {
		t.Fatal("user service cannot use loginctl self")
	}
}

func TestAutomaticConnectionFailureQueuesBasicOutsideCallback(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, enhanced := range []bool{false, true} {
			v := &viewer{automatic: automatic, enhanced: enhanced, log: io.Discard}
			for _, event := range []int32{1, 18, 19} {
				v.sessionEvent(event)
			}
			if v.fallbackPending {
				t.Fatal("credential or certificate waiting triggered fallback")
			}
			callback := &connectorCallback{table: &callbackTable, viewer: v}
			var pinned runtime.Pinner
			pinned.Pin(v)
			pinned.Pin(callback)
			_, _, _ = syscall.SyscallN(callbackTable[4], uintptr(unsafe.Pointer(callback)), 0x80004005)
			pinned.Unpin()
			if v.fallbackPending != (automatic && enhanced) || !strings.Contains(v.status, "管道连接失败") {
				t.Fatalf("automatic=%v enhanced=%v pending=%v status=%q", automatic, enhanced, v.fallbackPending, v.status)
			}
			if v.closed {
				t.Fatal("callback tore down COM synchronously")
			}
		}
	}
	for _, pipe := range []string{`\\.\pipe\my-vm-123.basic`, `\\.\pipe\my-vm-123.enhanced`, `\\.\pipe\my-vm-123.enhanced.linux-42`} {
		for range 4 {
			basic := basicDisplayPipe(pipe)
			if basic != `\\.\pipe\my-vm-123.basic` || basicDisplayPipe(basic) != basic {
				t.Fatal("wrong or non-idempotent basic pipe:", basic)
			}
			pipe = strings.TrimSuffix(basic, ".basic") + ".enhanced"
		}
	}
}

func TestAutomaticDisplayProbeIsBounded(t *testing.T) {
	started := time.Now()
	if got := probeDisplay("blocked", "linux", io.Discard); got.Mode != "" {
		t.Fatal("blocked agent selected enhanced")
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("probe was not bounded by child lifetime")
	}
	for _, kind := range []string{"windows", "wayland", "x11"} {
		if got := probeDisplay(kind, kind, io.Discard); got.Mode != kind {
			t.Fatalf("probe returned %q", got.Mode)
		}
	}
}
