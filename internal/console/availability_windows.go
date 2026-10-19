//go:build windows

package console

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"my-vm/internal/hcs"
)

type enhancedAvailability struct {
	results            chan bool
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
		a.results = make(chan bool, 1)
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

func enhancedAvailable(ctx context.Context, runtimeID, kind string) bool {
	state := probeAvailability(ctx, runtimeID, kind)
	if state == "unknown" && kind == "windows" && ctx.Err() == nil {
		state = probeAvailability(ctx, runtimeID, "windows-agent")
	}
	return state == "ready"
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
		if state == "ready" || state == "waiting" || state == "unknown" {
			return state
		}
	}
	return "unknown"
}

func queryEnhancedAvailability(runtimeID, kind string) string {
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
		client, err := guestClient(runtimeID)
		if err != nil {
			return "waiting"
		}
		defer client.CloseIdleConnections()
		capability, err := displayFromGuest(client, "windows-ready")
		clear(capability.Password)
		if err == nil && capability.Mode == "windows" {
			return "ready"
		}
		return "waiting"
	}
	capability, err := queryDisplay(runtimeID, kind)
	clear(capability.Password)
	if err == nil && capability.Mode != "" {
		return "ready"
	}
	return "waiting"
}

func (v *viewer) checkEnhancedAvailability() {
	if v.closed || v.enhanced || v.toolbar == nil || v.toolbar.busy {
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
		ctx, cancel := context.WithTimeout(bar.ctx, 8*time.Second)
		defer cancel()
		ready := enhancedAvailable(ctx, runtimeID, kind)
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
	case ready := <-availability.results:
		if v.enhanced || v.toolbar.busy || v.toolbar.prompt != promptNone {
			availability.checking = false
			return
		}
		if !availability.finish(ready) {
			return
		}
	default:
		return
	}
	v.showPrompt(promptEnhanced)
}
