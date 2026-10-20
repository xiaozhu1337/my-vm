//go:build windows

package console

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"my-vm/internal/console/renderhost"
	"my-vm/internal/control"
	"my-vm/internal/guestops"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"my-vm/internal/hcs"
)

type guestStatusUpdate struct {
	status   *guestops.Status
	received time.Time
}

type enhancedAvailability struct {
	updates            chan guestStatusUpdate
	pushed             *guestops.Status
	received           time.Time
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
	v.guestStatusResult()
	if v.toolbar != nil && v.toolbar.availability.fresh(time.Now()) {
		v.applyEnhancedAvailability(protocolAvailability(*v.toolbar.availability.pushed, v.desktopKind()))
		return
	}
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
		availability.checking = false
		if availability.fresh(time.Now()) {
			state = protocolAvailability(*availability.pushed, v.desktopKind())
		}
		v.applyEnhancedAvailability(state)
	default:
		return
	}
}

func (a *enhancedAvailability) fresh(now time.Time) bool {
	return a.pushed != nil && now.Sub(a.received) < 15*time.Second
}

func (v *viewer) desktopKind() string {
	if !v.linux {
		return "windows"
	}
	if v.linuxMode != "" {
		return v.linuxMode
	}
	return "linux"
}

func (v *viewer) applyEnhancedAvailability(state string) {
	if v.closed || v.toolbar == nil {
		return
	}
	a := &v.toolbar.availability
	if a.last != state {
		a.last = state
		if v.log != nil {
			fmt.Fprintln(v.log, "增强状态:", state)
		}
		switch state {
		case "ready":
			v.toolbar.guestNotice = "增强画面已就绪"
		case "available":
			v.toolbar.guestNotice = "增强服务可用，可点击“增强”尝试连接"
		case "waiting":
			v.toolbar.guestNotice = "增强画面尚未就绪，请确认客户机桌面已登录、解锁"
		case "unsupported":
			v.toolbar.guestNotice = "当前客户机状态不支持增强画面"
		default:
			v.toolbar.guestNotice = "客户机状态暂不可用，正在重新检测"
		}
		if v.automatic && (state == "ready" || state == "available") {
			v.revealHeader()
		}
		v.renderStatus()
	}
	if !v.automatic || v.enhanced || v.host == nil || v.host.Candidate != nil || v.toolbar.busy || v.toolbar.prompt != promptNone {
		return
	}
	if a.finish(renderhost.ShouldUpgrade(v.automatic, state)) {
		v.switchRenderer(true, true)
	}
}

func (v *viewer) startGuestStatus() {
	if v.toolbar == nil || v.machineID == "" || v.vmid == "" {
		return
	}
	updates := make(chan guestStatusUpdate, 1)
	v.toolbar.availability.updates = updates
	ctx, id, runtimeID, hwnd := v.toolbar.ctx, v.machineID, v.vmid, v.hwnd
	go func() {
		client, err := control.ExistingClient()
		if err != nil {
			return
		}
		defer client.CloseIdleConnections()
		client.Transport.(*http.Transport).ResponseHeaderTimeout = 5 * time.Second
		watchGuestStatus(ctx, client, id, runtimeID, func(status *guestops.Status) {
			update := guestStatusUpdate{status: status, received: time.Now()}
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- update:
			case <-ctx.Done():
				return
			}
			user32.NewProc("PostMessageW").Call(hwnd, 0x8006, 0, 0)
		})
	}()
}

func watchGuestStatus(ctx context.Context, client *http.Client, id, runtimeID string, update func(*guestops.Status)) {
	address := "http://manager/v1/guest-status?" + url.Values{"vm": {id}, "runtime": {runtimeID}}.Encode()
	for ctx.Err() == nil {
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		response, err := client.Do(r)
		if err == nil {
			if response.StatusCode == http.StatusOK {
				_ = readGuestStatus(response.Body, update)
			}
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return
		}
		update(nil)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func readGuestStatus(reader io.Reader, update func(*guestops.Status)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4096)
	for scanner.Scan() {
		var event guestops.StatusEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Status != nil && event.Status.Validate() != nil {
			return errors.New("invalid guest status stream")
		}
		update(event.Status)
	}
	return scanner.Err()
}

func (v *viewer) guestStatusResult() {
	if v.closed || v.toolbar == nil {
		return
	}
	a := &v.toolbar.availability
	select {
	case update := <-a.updates:
		a.pushed, a.received = update.status, update.received
		if a.fresh(time.Now()) {
			v.applyEnhancedAvailability(protocolAvailability(*a.pushed, v.desktopKind()))
			return
		}
	default:
		if a.pushed == nil || a.fresh(time.Now()) {
			return
		}
		a.pushed = nil
	}
	v.applyEnhancedAvailability("unknown")
}
