//go:build windows

package console

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"my-vm/internal/console/renderhost"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

func TestDismissHeaderReclaimsSpaceAndStaysHidden(t *testing.T) {
	for _, dpi := range []uintptr{96, 144, 192} {
		t.Run(fmt.Sprint(dpi), func(t *testing.T) {
			v := &viewer{enhanced: true, loggedIn: true}
			top, before := v.contentLayout(1280, 900, dpi)
			if top != int32(32*dpi/96) || before.height != 900-top {
				t.Fatal("visible header has the wrong height", top, before)
			}
			v.layout = before
			v.dismissHeader()
			if !v.headerHidden || v.closed || !v.enhanced || !v.loggedIn || v.layout != (displaySettings{}) {
				t.Fatal("dismissal closed the session or retained its old layout")
			}
			for _, status := range []string{"已连接", "连接已断开", "已连接 · 客户机显示尚未就绪"} {
				v.setStatus(status)
				top, after := v.contentLayout(1280, 900, dpi)
				if !v.headerHidden || top != 0 || after != (displaySettings{1280, 900, dpi}) {
					t.Fatal("status refresh restored the dismissed header", status, top, after)
				}
			}
			v.layout = before
			v.dismissHeader()
			if v.layout != before {
				t.Fatal("repeated dismissal invalidated the layout again")
			}
		})
	}
}

func TestConnectedNoticeOptOutPersistsAndPreservesOtherMessages(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	v := &viewer{log: io.Discard, toolbar: &consoleToolbar{confirmButton: 501}}
	v.loadNoticePreference()
	v.setStatus(connectedNotice)
	if v.headerHidden || !v.canHideConnectedNotice() {
		t.Fatal("connected notice must initially offer opt-out")
	}
	if v.toolbarCommand(toolbarConfirm, 0) || v.toolbarCommand(toolbarConfirm, 502) {
		t.Fatal("opt-out accepted a command from an unrelated control")
	}
	if !v.toolbarCommand(toolbarConfirm, 501) || !v.headerHidden || !v.hideConnectedNotice {
		t.Fatal("opt-out did not immediately dismiss and remember the notice")
	}
	next := &viewer{log: io.Discard, automatic: true, toolbar: &consoleToolbar{}}
	next.loadNoticePreference()
	next.setStatus(connectedNotice)
	if !next.hideConnectedNotice || !next.headerHidden {
		t.Fatal("another console window did not honor the saved preference")
	}
	next.applyEnhancedAvailability("available")
	if !next.headerHidden {
		t.Fatal("a guest status refresh restored the suppressed connected notice")
	}
	next.toolbarError(errors.New("连接失败"))
	if next.headerHidden || next.canHideConnectedNotice() {
		t.Fatal("the preference hid an error")
	}
	next.setStatus(connectedNotice)
	next.showPrompt(promptShutdown)
	if next.headerHidden || next.canHideConnectedNotice() || next.toolbar.prompt != promptShutdown {
		t.Fatal("the preference hid a confirmation")
	}
	next.toolbarError(errors.New("关机请求失败"))
	if next.headerHidden || !next.statusError {
		t.Fatal("restoring the connected status while clearing a prompt hid its error")
	}
	next.toolbar.notice = "虚拟机已暂停"
	next.setStatus(connectedNotice)
	if next.headerHidden || next.canHideConnectedNotice() {
		t.Fatal("the preference hid an operation result")
	}
}

func TestConnectedNoticePreferenceFailuresStayVisible(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	if err := os.WriteFile(filepath.Join(dir, "my-vm"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	v := &viewer{log: io.Discard, toolbar: &consoleToolbar{confirmButton: 501}}
	v.setStatus(connectedNotice)
	v.toolbarCommand(toolbarConfirm, 501)
	if v.hideConnectedNotice || v.headerHidden || !v.statusError {
		t.Fatal("failed save silently dismissed the notice", v.status)
	}
}

func TestInitialDesktopUsesHostDimensionsAndDPI(t *testing.T) {
	for _, test := range []struct{ input, want displaySettings }{
		{displaySettings{1720, 1200, 144}, displaySettings{1720, 1200, 144}},
		{displaySettings{1, 1, 0}, displaySettings{1280, 720, 96}},
		{displaySettings{20000, 12000, 960}, displaySettings{8192, 8192, 480}},
	} {
		v := &viewer{renderer: &renderhost.Session{InitialDisplay: renderhost.Packet{Width: test.input.width, Height: test.input.height, DPI: uint32(test.input.dpi)}}}
		if got := v.initialDesktop(); got != test.want {
			t.Fatalf("initial desktop = %v, want %v", got, test.want)
		}
	}
}

func TestDisplayUpdatesOnlyAfterStableSizeAndConnection(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	calls := 0
	send := func(got displaySettings) error { calls++; return nil }
	state.update(false, settings, now, send)
	state.update(true, settings, now, send)
	if calls != 0 {
		t.Fatal("display sent before connection or while resizing")
	}
	for i := 1; i <= 30; i++ {
		state.update(true, settings, now.Add(time.Duration(i)*300*time.Millisecond), send)
	}
	if calls != 1 {
		t.Fatalf("stable desktop updated %d times", calls)
	}
	settings.dpi = 144
	state.update(true, settings, now.Add(10*time.Second), send)
	state.update(true, settings, now.Add(11*time.Second), send)
	if calls != 2 {
		t.Fatal("DPI-only change was missed")
	}
	state.update(false, settings, now.Add(12*time.Second), send)
	state.update(true, settings, now.Add(13*time.Second), send)
	state.update(true, settings, now.Add(14*time.Second), send)
	if calls != 3 {
		t.Fatal("unchanged dimensions were not sent on a new connection")
	}
}

func TestDisplayWaitsForAvailableChannelAndKeepsLatestSize(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	calls, ready := 0, false
	send := func(got displaySettings) error {
		calls++
		if got != settings {
			t.Fatal("stale display dimensions sent", got)
		}
		if !ready {
			return fmt.Errorf("display update: %w", ole.NewError(0x8000FFFF))
		}
		return nil
	}
	state.update(true, settings, now, send)
	sent, err := state.update(true, settings, now.Add(300*time.Millisecond), send)
	if sent || err != nil || !state.waiting || state.applied == settings {
		t.Fatal("unavailable channel was reported as failed or applied", err)
	}
	state.update(true, settings, now.Add(time.Second), send)
	if calls != 1 || !state.waiting {
		t.Fatal("pending display update ignored backoff")
	}
	settings.width = 1600
	state.update(true, settings, now.Add(1500*time.Millisecond), send)
	state.update(true, settings, now.Add(1800*time.Millisecond), send)
	if calls != 2 || state.applied == settings {
		t.Fatal("new pending dimensions were lost")
	}
	ready = true
	sent, err = state.update(true, settings, now.Add(4*time.Second), send)
	if !sent || err != nil || state.waiting || state.applied != settings {
		t.Fatal("pending channel did not recover with latest dimensions", err)
	}
	state.update(true, settings, now.Add(5*time.Second), send)
	if calls != 3 {
		t.Fatal("stable applied display was sent again")
	}
	state.update(false, settings, now.Add(6*time.Second), send)
	if state.waiting || state.connected || state.applied != (displaySettings{}) {
		t.Fatal("disconnect did not clear display state")
	}
}

func TestDisplayReturningToAppliedSizeClearsPendingUpdate(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	calls := 0
	send := func(displaySettings) error {
		calls++
		if calls > 1 {
			return ole.NewError(0x8000FFFF)
		}
		return nil
	}
	state.update(true, settings, now, send)
	state.update(true, settings, now.Add(300*time.Millisecond), send)
	larger := settings
	larger.width = 1600
	state.update(true, larger, now.Add(time.Second), send)
	state.update(true, larger, now.Add(1300*time.Millisecond), send)
	if !state.waiting {
		t.Fatal("unavailable update was not retained")
	}
	state.update(true, settings, now.Add(2*time.Second), send)
	state.update(true, settings, now.Add(4*time.Second), send)
	if state.waiting || state.applied != settings || calls != 2 {
		t.Fatal("returning to the applied size kept a stale pending update")
	}
}

func TestDisplayDoesNotHideOtherCOMFailures(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	failure := ole.NewError(0x80070057)
	send := func(displaySettings) error { return failure }
	state.update(true, settings, now, send)
	_, err := state.update(true, settings, now.Add(time.Second), send)
	if err != failure || state.waiting {
		t.Fatal("unexpected COM failure was hidden", err)
	}
}

func TestDisplayPersistentInitializationFailureIsReportedAndStillRetries(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	failure := ole.NewError(0x8000FFFF)
	ready := false
	send := func(displaySettings) error {
		if ready {
			return nil
		}
		return failure
	}
	for i := range 4 {
		settings.width++
		tick := now.Add(time.Duration(i) * 10 * time.Second)
		state.update(true, settings, tick, send)
		_, err := state.update(true, settings, tick.Add(300*time.Millisecond), send)
		if i < 3 && (err != nil || !state.waiting) {
			t.Fatal("initial channel delay was reported as failure", err)
		}
		if i == 3 && (err != failure || state.waiting) {
			t.Fatal("persistent failure was hidden by dimension changes", err)
		}
	}
	ready = true
	sent, err := state.update(true, settings, now.Add(33*time.Second), send)
	if !sent || err != nil || state.waiting || !state.unavailable.IsZero() || state.applied != settings {
		t.Fatal("reported pending update stopped retrying or did not recover", err)
	}
}

func TestNativeDisplayUpdateBeforeConnect(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 to test the native RDP control without connecting a guest")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		t.Fatal(err)
	}
	defer ole.CoUninitialize()
	object, err := ole.CreateInstance(ole.NewGUID("{A0C63C30-F08D-4AB4-907C-34905D770C7D}"), ole.IID_IUnknown)
	if err != nil {
		t.Fatal(err)
	}
	defer object.Release()
	client, err := object.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Release()
	v := &viewer{client: client, log: io.Discard}
	if err := v.initializeEvents(); err != nil {
		t.Fatal(err)
	}
	defer v.closeEvents()
	invoke := func(id uintptr, args *eventArguments) {
		if err := call((*ole.IUnknown)(unsafe.Pointer(v.events)), 6, id, 0, 0, 1, uintptr(unsafe.Pointer(args)), 0, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	invoke(2, nil)
	if v.loggedIn {
		t.Fatal("OnConnected was mistaken for login completion")
	}
	invoke(3, nil)
	if !v.loggedIn {
		t.Fatal("native event sink did not accept OnLoginComplete")
	}
	invoke(4, nil)
	if v.loggedIn {
		t.Fatal("disconnect retained login state")
	}
	allow := int16(0)
	value := ole.VARIANT{VT: ole.VT_BYREF | ole.VT_BOOL, Val: int64(uintptr(unsafe.Pointer(&allow)))}
	invoke(15, &eventArguments{values: &value, count: 1})
	if allow != -1 {
		t.Fatal("event sink did not allow session close")
	}
	var display *ole.IUnknown
	if err := client.PutQueryInterface(ole.NewGUID("{28904001-04B6-436C-A55B-0AF1A0883DC9}"), &display); err != nil {
		t.Fatal(err)
	}
	defer display.Release()
	err = call(display, 69, 1280, 720, 338, 190, 0, 100, 100)
	var native *ole.OleError
	if !errors.As(err, &native) || uint32(native.Code()) != 0x8000FFFF {
		t.Fatalf("native display HRESULT was not retained: %v", err)
	}
	t.Logf("native display update before Connect: %v", err)
}

func TestNativeInputModePolicy(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 to check native RDP mouse settings without a guest")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		t.Fatal(err)
	}
	defer ole.CoUninitialize()
	object, err := ole.CreateInstance(ole.NewGUID("{A0C63C30-F08D-4AB4-907C-34905D770C7D}"), ole.IID_IUnknown)
	if err != nil {
		t.Fatal(err)
	}
	defer object.Release()
	client, err := object.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Release()
	advanced, err := property(client, "AdvancedSettings9")
	if err != nil {
		t.Fatal(err)
	}
	defer advanced.Release()
	var settings *ole.IUnknown
	if err := client.PutQueryInterface(ole.NewGUID("{302D8188-0052-4807-806A-362B628F9AC5}"), &settings); err != nil {
		t.Fatal(err)
	}
	defer settings.Release()
	label := ole.SysAllocString("AllowRelativeMouseMode")
	defer ole.SysFreeString(label)
	extendedMouse := windows.RtlGetVersion().BuildNumber >= 26100
	for _, test := range []struct {
		name              string
		enhanced, wayland bool
	}{
		{"Linux enhanced", true, true},
		{"Windows basic", false, false},
		{"Windows enhanced", true, false},
	} {
		if extendedMouse {
			// Reproduce the old unconditional opt-in before applying the input policy.
			if err := extended(client, "AllowRelativeMouseMode", ole.VARIANT{VT: ole.VT_BOOL, Val: -1}); err != nil {
				t.Fatal(test.name, err)
			}
		}
		v := &viewer{client: client, advanced: advanced, enhanced: test.enhanced, wayland: test.wayland}
		if err := v.configureInput(); err != nil {
			t.Fatal(test.name, err)
		}
		if extendedMouse {
			value := ole.VARIANT{}
			if err := call(settings, 4, uintptr(unsafe.Pointer(label)), uintptr(unsafe.Pointer(&value))); err != nil {
				t.Fatal(test.name, err)
			}
			if value.VT != ole.VT_BOOL || (value.Val != 0) != !test.wayland {
				t.Fatalf("%s: relative mouse opt-in=%+v, want %v", test.name, value, !test.wayland)
			}
			_ = value.Clear()
		}
		mode, err := oleutil.GetProperty(advanced, "RelativeMouseMode")
		if err != nil {
			t.Fatal(test.name, err)
		}
		defer mode.Clear()
		if (mode.Val != 0) != !test.wayland {
			t.Fatalf("%s: relative mouse mode=%v, want %v", test.name, mode.Val, !test.wayland)
		}
	}
}

func TestNativeAutomaticScaling(t *testing.T) {
	if os.Getenv("MY_VM_INTEGRATION") != "1" {
		t.Skip("set MY_VM_INTEGRATION=1 to test native RDP scaling without connecting a guest")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		t.Fatal(err)
	}
	defer ole.CoUninitialize()
	object, err := ole.CreateInstance(ole.NewGUID("{A0C63C30-F08D-4AB4-907C-34905D770C7D}"), ole.IID_IUnknown)
	if err != nil {
		t.Fatal(err)
	}
	defer object.Release()
	client, err := object.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Release()
	advanced, err := property(client, "AdvancedSettings9")
	if err != nil {
		t.Fatal(err)
	}
	defer advanced.Release()
	for _, test := range []struct {
		name                                 string
		enhanced, wayland, dynamicResolution bool
	}{
		{"GNOME dynamic resolution", true, true, true},
		{"Linux fixed resolution", true, true, false},
		{"Windows enhanced", true, false, false},
		{"Basic console", false, false, false},
	} {
		if err := put(advanced, "SmartSizing", false); err != nil {
			t.Fatal(test.name, err)
		}
		if err := extended(client, "EnableZoom", ole.VARIANT{VT: ole.VT_BOOL}); err != nil {
			t.Fatal(test.name, err)
		}
		v := &viewer{client: client, advanced: advanced, enhanced: test.enhanced,
			wayland: test.wayland, dynamicResolution: test.dynamicResolution}
		if err := v.configureScaling(); err != nil {
			t.Fatal(test.name, err)
		}
		sizing, err := oleutil.GetProperty(advanced, "SmartSizing")
		if err != nil {
			t.Fatal(test.name, err)
		}
		defer sizing.Clear()
		if sizing.VT != ole.VT_BOOL || sizing.Val == 0 {
			t.Fatal(test.name, "automatic frame fitting is disabled", sizing)
		}
	}
}

func TestDisplayResizeDebounceAndFailedUpdateRetry(t *testing.T) {
	var state displayUpdate
	now := time.Now()
	settings := displaySettings{1280, 720, 96}
	calls := 0
	failure := errors.New("display not ready")
	send := func(got displaySettings) error { calls++; return failure }
	for i := range 10 {
		settings.width++
		state.update(true, settings, now.Add(time.Duration(i)*30*time.Millisecond), send)
	}
	if calls != 0 {
		t.Fatal("interactive resizing flooded the display channel")
	}
	_, err := state.update(true, settings, now.Add(time.Second), send)
	if err != failure || state.applied == settings {
		t.Fatal("failed update was marked successful")
	}
	state.update(true, settings, now.Add(1500*time.Millisecond), send)
	if calls != 1 {
		t.Fatal("failed update retried without backoff")
	}
	failure = nil
	state.update(true, settings, now.Add(4*time.Second), send)
	state.update(true, settings, now.Add(5*time.Second), send)
	if calls != 2 || state.applied != settings {
		t.Fatal("failed update did not recover or stable size was sent again")
	}
}
