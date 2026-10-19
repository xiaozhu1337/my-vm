//go:build windows

package console

import (
	"context"
	"errors"
	"io"
	"my-vm/internal/console/renderhost"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWindowsEnhancedDoesNotDependOnGuestAgent(t *testing.T) {
	for _, runtimeID := range []string{"", "no-agent", "not-a-runtime-guid"} {
		got, err := queryDisplay(runtimeID, "windows")
		if err != nil || got.Mode != "windows" {
			t.Fatalf("Windows was rejected before a display connection: %q %v", got.Mode, err)
		}
		if got := probeDisplay(runtimeID, "windows", io.Discard); got.Mode != "windows" {
			t.Fatal("Windows probe depended on the guest agent")
		}
	}
}

func TestRendererVisibilityAndZOrderDuringMove(t *testing.T) {
	// Visible background consoles must keep their image when another application receives focus.
	if !rendererVisible(true, false) || rendererVisible(true, true) || rendererVisible(false, false) {
		t.Fatal("foreground changes must not be treated as minimization")
	}
	for _, test := range []struct {
		name                       string
		foregroundIsHostOrRenderer bool
	}{
		{"host activated or dragged", true},
		{"renderer has focus", true},
		{"authentication dialog", false},
		{"unrelated application", false},
	} {
		flags := rendererPositionFlags(true, test.foregroundIsHostOrRenderer)
		if flags&0x10 == 0 || flags&0x40 == 0 || flags&0x80 != 0 || flags&(0x1|0x2) != 0 {
			t.Fatal(test.name, "move must show and reposition without stealing focus", flags)
		}
		if (flags&0x4 == 0) != test.foregroundIsHostOrRenderer {
			t.Fatal(test.name, "incorrect content/authentication window stacking", flags)
		}
	}
	if flags := rendererPositionFlags(false, false); flags&(0x80|0x1|0x2|0x4|0x10) != 0x97 || flags&0x40 != 0 {
		t.Fatal("minimize must hide without moving or restacking", flags)
	}
}

func TestConsoleToolbarUsesStableMachineID(t *testing.T) {
	id := "f0c0edc13261438a8b7f755fe72fa4bc"
	for _, suffix := range []string{"basic", "enhanced", "enhanced.linux-1234"} {
		if got := machineIDFromPipe(`\\.\pipe\my-vm-` + id + "." + suffix); got != id {
			t.Fatalf("wrong stable identity: %q", got)
		}
	}
	for _, pipe := range []string{`\\remote\pipe\my-vm-` + id + ".basic", `\\.\pipe\my-vm-runtime.basic`, `\\.\pipe\my-vm-` + id + ".unknown"} {
		if got := machineIDFromPipe(pipe); got != "" {
			t.Fatalf("invalid identity accepted: %q", got)
		}
	}
}

func TestToolbarRemainsAfterHeaderDismissal(t *testing.T) {
	for _, dpi := range []uintptr{96, 144, 192} {
		v := &viewer{toolbar: &consoleToolbar{}}
		top, settings := v.contentLayout(1280, 900, dpi)
		if top != int32(72*dpi/96) || settings.height != 900-top {
			t.Fatal("toolbar/header overlap", top, settings)
		}
		v.dismissHeader()
		top, settings = v.contentLayout(1280, 900, dpi)
		if top != int32(40*dpi/96) || settings.height != 900-top || v.closed {
			t.Fatal("dismissal also hid the toolbar")
		}
	}
}

func TestToolbarPowerQueriesStateAndUsesExistingActions(t *testing.T) {
	id := "f0c0edc13261438a8b7f755fe72fa4bc"
	for _, test := range []struct{ state, input, action string }{
		{"运行中", "toggle-pause", "pause"},
		{"已暂停", "toggle-pause", "resume"},
		{"", "stop", "stop"},
		{"已关机", "toggle-pause", ""},
	} {
		var calls []string
		client := &http.Client{Transport: desktopTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Get("vm") != id || r.URL.Path != "/v1/action" {
				t.Fatal("wrong VM or endpoint", r.URL)
			}
			action := r.URL.Query().Get("action")
			calls = append(calls, r.Method+" "+action)
			body, status := "", 204
			if action == "state" {
				body, status = `"`+test.state+`"`, 200
			} else if r.Method != "POST" || action != test.action {
				t.Fatalf("unexpected action %s %s", r.Method, action)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		status, err := toolbarPower(context.Background(), client, id, test.input)
		if (err != nil) != (test.action == "") {
			t.Fatalf("state=%s status=%s error=%v", test.state, status, err)
		}
		want := []string{"GET state"}
		if test.input == "stop" {
			want = []string{"POST stop"}
		} else if test.action != "" {
			want = append(want, "POST "+test.action)
		}
		if strings.Join(calls, ",") != strings.Join(want, ",") {
			t.Fatal("wrong action sequence", calls)
		}
	}
}

func TestToolbarPowerDoesNotForceStopAfterFailure(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: desktopTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader("busy"))}, nil
	})}
	if _, err := toolbarPower(context.Background(), client, "id", "stop"); err == nil || calls != 1 {
		t.Fatal("failed shutdown was retried or escalated", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.Transport = desktopTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	if _, err := toolbarPower(ctx, client, "id", "stop"); !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "尚未确认") {
		t.Fatal("interrupted operation claimed a known result", err)
	}
}

func TestControlAltDeleteUsesBalancedExtendedScanCodes(t *testing.T) {
	up, keys := controlAltDeleteKeys()
	for i, scan := range []uint32{0x1d, 0x38, 0x53} {
		if up[i] != 0 || up[5-i] != -1 || keys[i]>>16&0xff != scan || keys[5-i]>>16&0xff != scan || keys[i]&0xc0000000 != 0 || keys[5-i]&0xc0000000 != 0xc0000000 {
			t.Fatal("unbalanced scan codes", up, keys)
		}
	}
	if keys[2]&(1<<24) == 0 || keys[3]&(1<<24) == 0 {
		t.Fatal("Delete lost the extended-key bit")
	}
}

func TestToolbarResultSurvivesConnectionStatusRefresh(t *testing.T) {
	v := &viewer{log: io.Discard, toolbar: &consoleToolbar{busy: true, results: make(chan toolbarResult, 1)}}
	v.toolbar.results <- toolbarResult{err: errors.New("manager busy")}
	v.finishToolbarAction()
	v.setStatus("已连接")
	if v.toolbar.busy || !strings.Contains(v.status, "manager busy") {
		t.Fatal("background error was hidden by the connection timer", v.status)
	}
	v.toolbar.results <- toolbarResult{status: "虚拟机已恢复运行"}
	v.finishToolbarAction()
	v.setStatus("已连接")
	if !strings.Contains(v.status, "虚拟机已恢复运行") {
		t.Fatal("power-operation outcome was lost", v.status)
	}
}

func TestAutomaticReadyStartsCandidateWithoutConfirmation(t *testing.T) {
	for _, state := range []string{"ready", "unknown", "available", "waiting", "unsupported", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			host := renderhost.New(func(*renderhost.Worker) *exec.Cmd {
				return exec.Command(os.Args[0], "-test.run=^$")
			})
			bar := &consoleToolbar{availability: enhancedAvailability{checking: true, results: make(chan string, 1)}}
			bar.availability.results <- state
			v := &viewer{host: host, automatic: true, toolbar: bar, headerHidden: true, log: io.Discard}
			v.enhancedAvailabilityResult()
			candidate := host.Candidate
			host.Close()
			if candidate != nil {
				select {
				case <-candidate.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("owned test process survived close")
				}
			}
			if bar.prompt != promptNone || bar.availability.checking || (candidate != nil) != (state == "ready") {
				t.Fatal("readiness did not automatically start exactly one positive candidate")
			}
			if candidate != nil && (!candidate.Enhanced || v.enhanced) {
				t.Fatal("candidate did not preserve basic until connected")
			}
		})
	}
}

func TestManualBasicNeverOffersEnhancement(t *testing.T) {
	for _, state := range []string{"ready", "unknown", "available", "waiting", "unsupported", "unavailable"} {
		bar := &consoleToolbar{availability: enhancedAvailability{checking: true, results: make(chan string, 1)}}
		bar.availability.results <- state
		v := &viewer{toolbar: bar, headerHidden: true, display: displayUpdate{connected: true}, log: io.Discard}
		v.enhancedAvailabilityResult()
		if bar.prompt != promptNone || bar.availability.notified || bar.availability.checking || v.enhanced || !v.headerHidden {
			t.Fatalf("manual basic was interrupted by readiness state %q", state)
		}
		v.checkEnhancedAvailability()
		if bar.availability.checking {
			t.Fatal("manual basic started a readiness probe")
		}
	}
}

func TestEnhancedAvailabilityDoesNotInterruptOrRepeat(t *testing.T) {
	now := time.Now()
	var state enhancedAvailability
	if !state.start(now) || state.start(now.Add(time.Second)) {
		t.Fatal("readiness probes overlap")
	}
	if state.finish(false) || state.notified || state.start(now.Add(time.Second)) {
		t.Fatal("not-ready probe prompted or bypassed the polling interval")
	}
	if !state.start(now.Add(16*time.Second)) || !state.finish(true) || !state.notified {
		t.Fatal("new readiness was not offered")
	}
	if state.finish(true) || state.start(now.Add(time.Hour)) {
		t.Fatal("declined or previously accepted offer repeated in the same window")
	}
	for _, v := range []*viewer{{closed: true}, {enhanced: true}, {toolbar: &consoleToolbar{busy: true}}} {
		v.checkEnhancedAvailability()
		if v.toolbar != nil && v.toolbar.availability.checking {
			t.Fatal("probe started while enhanced, closed, or handling a user operation")
		}
	}
	bar := &consoleToolbar{availability: enhancedAvailability{checking: true, results: make(chan string, 1)}}
	bar.availability.results <- "ready"
	v := &viewer{enhanced: true, toolbar: bar}
	v.enhancedAvailabilityResult()
	if bar.availability.notified || bar.availability.checking {
		t.Fatal("late readiness interrupted a user-initiated switch")
	}
}

func TestStatusCompatibilityHasItsOwnTimeBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	if got := enhancedState(ctx, "legacy-slow", "linux"); got != "ready" {
		t.Fatalf("protocol probe consumed fallback budget: %s", got)
	}
}

func TestEnhancedAvailabilityProbeIsBoundedAndConservative(t *testing.T) {
	for _, test := range []struct {
		runtime, kind string
		state         string
	}{
		{"ready", "windows", "ready"}, {"waiting", "windows", "waiting"},
		{"available", "windows", "available"}, {"unknown", "windows", "unknown"},
		{"known-unknown", "linux", "unknown"},
		{"garbage", "windows", "unknown"}, {"ready", "wayland", "ready"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		got := enhancedState(ctx, test.runtime, test.kind)
		cancel()
		if got != test.state {
			t.Fatalf("unexpected readiness: runtime=%s kind=%s state=%s", test.runtime, test.kind, got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if enhancedState(ctx, "blocked", "windows") != "unknown" || time.Since(started) > 2*time.Second {
		t.Fatal("hung provider was not bounded by the helper process")
	}
	if err := Run([]string{"--detect-ready", "id", "invalid"}, io.Discard); err == nil || err.Error() != "控制台会话参数无效" {
		t.Fatal("invalid readiness mode was not rejected before native calls")
	}
}

func TestInlineNoticesRequireAUserDecision(t *testing.T) {
	bar := &consoleToolbar{availability: enhancedAvailability{checking: true, results: make(chan string, 1)}}
	v := &viewer{toolbar: bar, headerHidden: true, log: io.Discard}
	bar.availability.results <- "ready"
	v.enhancedAvailabilityResult()
	if v.enhanced || bar.busy || bar.prompt != promptNone || !v.headerHidden || bar.availability.notified {
		t.Fatal("manual basic was changed by a readiness result")
	}
	if v.toolbarCommand(toolbarConfirm, 0) {
		t.Fatal("an untrusted command without a source control was accepted")
	}

	bar.buttons = []toolbarButton{{id: toolbarShutdown, handle: 501}}
	if !v.toolbarCommand(toolbarShutdown, 501) || bar.prompt != promptShutdown || bar.busy || v.headerHidden {
		t.Fatal("pressing shutdown started an operation before confirmation")
	}
	v.setStatus("已连接")
	if !strings.Contains(v.status, "请先保存") {
		t.Fatal("the shutdown warning was overwritten")
	}
	v.finishPrompt(false)
	if bar.busy || bar.prompt != promptNone {
		t.Fatal("cancelling shutdown started an operation")
	}

	v.showPrompt(promptShutdown)
	bar.availability = enhancedAvailability{checking: true, results: make(chan string, 1)}
	bar.availability.results <- "ready"
	v.enhancedAvailabilityResult()
	if bar.prompt != promptShutdown || bar.availability.notified {
		t.Fatal("a readiness result replaced a user confirmation")
	}
	v.fail(errors.New("connection failed"))
	if bar.prompt != promptNone || !v.statusError || !strings.Contains(v.status, "connection failed") {
		t.Fatal("a real failure was hidden behind a notice")
	}
}

func TestClearingPromptRestoresPreviousMessage(t *testing.T) {
	for _, original := range []string{"已连接 · 关闭控制台后虚拟机继续运行", "已连接 · 虚拟机已恢复运行", "connection failed"} {
		v := &viewer{status: original, statusError: original == "connection failed", log: io.Discard, toolbar: &consoleToolbar{}}
		v.showPrompt(promptShutdown)
		v.setStatus("已连接")
		v.clearPrompt()
		if v.status != original || v.statusError != (original == "connection failed") || v.toolbar.prompt != promptNone {
			t.Fatal("cancelling a prompt left stale or duplicate text", v.status)
		}
		v.clearPrompt()
		if v.status != original {
			t.Fatal("repeated cleanup erased the restored message")
		}
	}
}

func TestToolbarErrorsRevealDismissedMessageBar(t *testing.T) {
	v := &viewer{headerHidden: true, log: io.Discard, toolbar: &consoleToolbar{busy: true, results: make(chan toolbarResult, 1)}}
	v.toolbar.results <- toolbarResult{err: errors.New("manager unavailable")}
	v.finishToolbarAction()
	if v.headerHidden || !v.statusError || v.toolbar.busy {
		t.Fatal("a failed operation was hidden")
	}
	v.setStatus("已连接")
	if !v.statusError || !strings.Contains(v.status, "manager unavailable") {
		t.Fatal("connection refresh hid the error state")
	}
}

func TestToolbarClosedWindowIgnoresLateResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	v := &viewer{closed: true, toolbar: &consoleToolbar{ctx: ctx, cancel: cancel, results: make(chan toolbarResult, 1)}}
	cancel()
	v.toolbar.results <- toolbarResult{status: "late"}
	v.finishToolbarAction()
	if len(v.toolbar.results) != 1 || v.status != "" {
		t.Fatal("closed console consumed a late operation result")
	}
	select {
	case <-v.toolbar.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("window cancellation was lost")
	}
}
