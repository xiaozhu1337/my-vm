//go:build windows

package console

import (
	"context"
	"fmt"
	"my-vm/internal/console/renderhost"
	"my-vm/internal/guestops"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"my-vm/internal/hcs"
)

type enhancedAvailability struct {
	results            chan string
	last               string
	next               time.Time
	checking, notified bool
}

func (a *enhancedAvailability) start(now time.Time) bool {
	if a.notified || a.checking || now.Before(a.next) {
		return false
	}
	a.checking = true
	a.next = now.Add(15 * time.Second)
	if a.results == nil {
		a.results = make(chan string, 1)
	}
	return true
}

func (a *enhancedAvailability) finish(ready bool) bool {
	a.checking = false
	if !ready || a.notified {
		return false
	}
	a.notified = true
	return true
}

func enhancedState(ctx context.Context, runtimeID, kind string) string {
	if kind == "x11" {
		return "unsupported"
	}
	// A valid unknown state must not be overridden by an older readiness query.
	state := probeAvailability(ctx, runtimeID, kind+"-status")
	if state != "unavailable" {
		return state
	}
	if ctx.Err() != nil {
		return "unknown"
	}
	return probeAvailability(ctx, runtimeID, kind)
}

func probeAvailability(parent context.Context, runtimeID, kind string) string {
	executable, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--console", "--detect-ready", runtimeID, kind)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := command.Output()
	defer clear(output)
	if err == nil && len(output) <= 16 {
		state := strings.TrimSpace(string(output))
		if state == "ready" || state == "waiting" || state == "unknown" || state == "available" || state == "unsupported" || state == "unavailable" {
			return state
		}
	}
	if strings.HasSuffix(kind, "-status") {
		return "unavailable"
	}
	return "unknown"
}

func protocolAvailability(clientStatus guestops.Status, kind string) string {
	if kind == "x11" || clientStatus.OS == "linux" && clientStatus.Desktop.Mode == "x11" {
		return "unsupported"
	}
	if kind == "windows" && clientStatus.OS != "windows" || kind != "windows" && clientStatus.OS != "linux" || (kind == "x11" || kind == "wayland") && clientStatus.Desktop.Mode != kind {
		return "unsupported"
	}
	return clientStatus.Desktop.State
}

func queryEnhancedAvailability(runtimeID, kind string) string {
	if strings.HasSuffix(kind, "-status") {
		kind = strings.TrimSuffix(kind, "-status")
		if client, err := guestClient(runtimeID); err == nil {
			status, e := guestops.ReadStatus(context.Background(), client)
			client.CloseIdleConnections()
			if e == nil {
				return protocolAvailability(status, kind)
			}
		}
		return "unavailable"
	}
	if kind == "windows" {
		known, available := hcs.EnhancedSessionState(runtimeID)
		if !known {
			return "unknown"
		}
		if available {
			return "ready"
		}
		return "waiting"
	}
	if kind == "windows-agent" {
		return "unknown"
	}
	client, err := guestClient(runtimeID)
	if err != nil {
		return "unknown"
	}
	defer client.CloseIdleConnections()
	capability, err := legacyDisplayFromGuest(client, kind)
	clear(capability.Password)
	if err == nil && capability.Mode != "" {
		return "ready"
	}
	return "waiting"
}

func (v *viewer) checkEnhancedAvailability() {
	if v.closed || !v.automatic || v.enhanced || v.host == nil || v.host.Candidate != nil || v.toolbar == nil || v.toolbar.busy || v.toolbar.prompt != promptNone {
		return
	}
	bar := v.toolbar
	if !bar.availability.start(time.Now()) {
		return
	}
	runtimeID, kind, hwnd := v.vmid, "windows", v.hwnd
	if v.linux {
		kind = v.linuxMode
		if kind == "" {
			kind = "linux"
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(bar.ctx, 9*time.Second)
		defer cancel()
		ready := enhancedState(ctx, runtimeID, kind)
		if bar.ctx.Err() != nil {
			return
		}
		bar.availability.results <- ready
		user32.NewProc("PostMessageW").Call(hwnd, 0x8004, 0, 0)
	}()
}

func (v *viewer) enhancedAvailabilityResult() {
	if v.closed || v.toolbar == nil {
		return
	}
	availability := &v.toolbar.availability
	select {
	case state := <-availability.results:
		if state != availability.last {
			availability.last = state
			if v.log != nil {
				fmt.Fprintln(v.log, "增强状态:", state)
			}
		}
		if !v.automatic || v.enhanced || v.host == nil || v.host.Candidate != nil || v.toolbar.busy || v.toolbar.prompt != promptNone {
			availability.checking = false
			return
		}
		if !availability.finish(renderhost.ShouldUpgrade(v.automatic, state)) {
			return
		}
	default:
		return
	}
	v.switchRenderer(true, true)
}
