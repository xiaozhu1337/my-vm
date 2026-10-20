//go:build windows

package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"my-vm/internal/guestops"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

func TestGuestStatusStreamValidationAndFreshness(t *testing.T) {
	for _, test := range []struct {
		body string
		want int
		fail bool
	}{
		{`{"status":null}` + "\n", 1, false},
		{`{"status":{"protocol":1,"os":"windows","desktop":{"mode":"windows","state":"available","reason":"service_trigger_start"}}}` + "\n", 1, false},
		{`{"status":{"protocol":2}}` + "\n", 0, true},
		{strings.Repeat("a", 4097) + "\n", 0, true},
	} {
		count := 0
		err := readGuestStatus(strings.NewReader(test.body), func(*guestops.Status) { count++ })
		if (err != nil) != test.fail || count != test.want {
			t.Fatalf("stream count=%d error=%v", count, err)
		}
	}
	status := guestops.Status{Protocol: 1, OS: "windows", Desktop: guestops.DesktopStatus{Mode: "windows", State: "waiting"}}
	a := enhancedAvailability{pushed: &status, received: time.Now()}
	if !a.fresh(time.Now()) || a.fresh(time.Now().Add(16*time.Second)) {
		t.Fatal("push freshness incorrect")
	}
	bar := &consoleToolbar{availability: a}
	bar.availability.results = make(chan string, 1)
	bar.availability.results <- "ready"
	v := &viewer{toolbar: bar, log: io.Discard}
	v.enhancedAvailabilityResult()
	if bar.availability.last != "waiting" {
		t.Fatal("late poll overrode current pushed state")
	}
}

func TestPushedAvailableShowsNoticeWithoutSwitchingManualBasic(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		status := guestops.Status{Protocol: 1, OS: "windows", Desktop: guestops.DesktopStatus{Mode: "windows", State: "available"}}
		bar := &consoleToolbar{availability: enhancedAvailability{updates: make(chan guestStatusUpdate, 1)}}
		bar.availability.updates <- guestStatusUpdate{status: &status, received: time.Now()}
		v := &viewer{toolbar: bar, automatic: automatic, headerHidden: true, log: io.Discard}
		v.guestStatusResult()
		if bar.availability.last != "available" || !strings.Contains(bar.guestNotice, "可点击") || v.enhanced || bar.availability.notified || v.headerHidden == automatic {
			t.Fatal("pushed available did not preserve manual mode or expose its notice", automatic)
		}
	}
}

func TestGuestStatusWatcherClearsOnDisconnectAndCancels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var calls atomic.Int32
	client := &http.Client{Transport: desktopTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path != "/v1/guest-status" || r.URL.Query().Get("vm") != "vm" || r.URL.Query().Get("runtime") != "runtime" {
			t.Error("wrong status selector")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":{"protocol":1,"os":"windows","desktop":{"mode":"windows","state":"ready"}}}` + "\n"))}, nil
	})}
	states := []string{}
	watchGuestStatus(ctx, client, "vm", "runtime", func(status *guestops.Status) {
		if status == nil {
			states = append(states, "offline")
			cancel()
		} else {
			states = append(states, status.Desktop.State)
		}
	})
	if strings.Join(states, ",") != "ready,offline" || calls.Load() != 1 {
		t.Fatal(states, calls.Load())
	}
}

func TestProtocolAvailabilityRespectsModeAndState(t *testing.T) {
	for _, test := range []struct{ os, mode, state, kind, want string }{{"linux", "wayland", "unknown", "linux", "unknown"}, {"linux", "wayland", "ready", "windows", "unsupported"}, {"windows", "windows", "ready", "windows", "ready"}, {"windows", "windows", "available", "windows", "available"}, {"linux", "wayland", "waiting", "linux", "waiting"}, {"linux", "wayland", "ready", "x11", "unsupported"}, {"windows", "windows", "ready", "linux", "unsupported"}, {"linux", "x11", "unsupported", "linux", "unsupported"}, {"linux", "x11", "ready", "linux", "unsupported"}, {"linux", "x11", "ready", "x11", "unsupported"}} {
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
		{"linux", "x11", "ready", "linux", true}, {"linux", "wayland", "ready", "linux", false},
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

func TestLegacyX11EnhancementDisabled(t *testing.T) {
	if got := probeDisplay("must-not-connect", "x11", io.Discard); got.Mode != "" {
		t.Fatal("old X11 probe returned an enhanced display")
	}
	v := &viewer{linux: true, linuxMode: "x11", pipe: `\\.\pipe\my-vm-fixture.basic`, log: io.Discard}
	v.switchRenderer(true, true)
	if v.enhanced || !strings.Contains(v.status, "已停用") || v.pipe != `\\.\pipe\my-vm-fixture.basic` {
		t.Fatal("disabled X11 changed the current display")
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
		{"x11\n", "", "", "", true},
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
		{"linux", "x11", 0, false, true},
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
	if got := probeDisplay("x11", "linux", io.Discard); got.Mode != "" {
		t.Fatal("legacy helper bypassed disabled X11 mode")
	}
	for _, kind := range []string{"windows", "wayland"} {
		if got := probeDisplay(kind, kind, io.Discard); got.Mode != kind {
			t.Fatalf("probe returned %q", got.Mode)
		}
	}
}

func TestStaleCallbacksCannotChangeReplacementSession(t *testing.T) {
	v := &viewer{generation: 2, automatic: true, enhanced: true, log: io.Discard}
	connector := &connectorCallback{table: &callbackTable, viewer: v, generation: 1}
	events := &sessionEvents{table: &sessionEventTable, viewer: v, generation: 1}
	var pinned runtime.Pinner
	pinned.Pin(v)
	pinned.Pin(connector)
	pinned.Pin(events)
	defer pinned.Unpin()
	for _, closed := range []bool{false, true} {
		v.closed = closed
		// A stale success must be rejected before dereferencing a stream or client.
		_, _, _ = syscall.SyscallN(callbackTable[3], uintptr(unsafe.Pointer(connector)), 0)
		_, _, _ = syscall.SyscallN(callbackTable[4], uintptr(unsafe.Pointer(connector)), 0x80004005)
		for _, id := range []uintptr{1, 3, 4, 18, 19, 33} {
			_, _, _ = syscall.SyscallN(sessionEventTable[6], uintptr(unsafe.Pointer(events)), id, 0, 0, 0, 0, 0, 0, 0)
		}
		if v.fallbackPending || v.loggedIn || v.status != "" {
			t.Fatal("retired COM callbacks changed the replacement session")
		}
	}
}

func TestQueuedFallbackBelongsToOneSession(t *testing.T) {
	v := &viewer{generation: 2, automatic: true, enhanced: true, fallbackPending: true}
	if v.acceptFallback(1) || !v.acceptFallback(2) {
		t.Fatal("fallback message was not restricted to its originating session")
	}
	v.fallbackPending = false
	if v.acceptFallback(2) {
		t.Fatal("message with no pending failure requested fallback")
	}
	v.fallbackPending, v.closed = true, true
	if v.acceptFallback(2) {
		t.Fatal("closed viewer accepted fallback")
	}
}

func TestWindowsDisconnectReasonSurvivesBasicReconnect(t *testing.T) {
	var log bytes.Buffer
	bar := &consoleToolbar{}
	v := &viewer{generation: 2, automatic: true, enhanced: true, loggedIn: true, toolbar: bar, log: &log, headerHidden: true}
	events := &sessionEvents{table: &sessionEventTable, viewer: v, generation: 2}
	reason := &ole.VARIANT{VT: ole.VT_I4, Val: 3}
	args := &eventArguments{count: 1, values: reason}
	var pinned runtime.Pinner
	for _, value := range []any{v, events, reason, args} {
		pinned.Pin(value)
	}
	defer pinned.Unpin()
	_, _, _ = syscall.SyscallN(sessionEventTable[6], uintptr(unsafe.Pointer(events)), 4, 0, 0, 0, uintptr(unsafe.Pointer(args)), 0, 0, 0)
	if !strings.Contains(log.String(), "原因 3，扩展 0") || strings.Contains(log.String(), "GNOME") || !v.fallbackPending || v.loggedIn || v.closed {
		t.Fatal("Windows disconnect did not log its reason before queuing fallback", log.String())
	}
	v.setStatus("正在重新连接")
	v.retainFallbackReason()
	for range 3 {
		v.setStatus("已连接 · 关闭控制台后虚拟机继续运行")
		if !strings.Contains(v.status, "原因 3") || !strings.Contains(v.status, "已返回基础画面") || !v.statusError || v.headerHidden {
			t.Fatal("basic reconnect or status refresh erased the enhanced failure", v.status)
		}
	}
}

func TestCallbackRootsFollowNativeReferences(t *testing.T) {
	check := func(object any, refs *atomic.Int32, address, add, release uintptr) {
		t.Helper()
		registerCallback(object, refs)
		_, _, _ = syscall.SyscallN(add, address)
		if got := releaseCallback(object, refs); got != 1 {
			t.Fatal("closing creator reference released a native reference", got)
		}
		runtime.GC()
		if _, held := callbackRoots.Load(object); !held {
			t.Fatal("native callback lost its Go root before final Release")
		}
		count, _, _ := syscall.SyscallN(release, address)
		if _, held := callbackRoots.Load(object); count != 0 || held {
			t.Fatal("final native Release leaked its Go root")
		}
	}
	connector := &connectorCallback{table: &callbackTable}
	check(connector, &connector.refs, uintptr(unsafe.Pointer(connector)), callbackTable[1], callbackTable[2])
	events := &sessionEvents{table: &sessionEventTable}
	check(events, &events.refs, uintptr(unsafe.Pointer(events)), sessionEventTable[1], sessionEventTable[2])
}
