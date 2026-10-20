//go:build windows

package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"my-vm/internal/console/renderhost"
	"my-vm/internal/guestops"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type displayCapability struct {
	Mode     string `json:"mode"`
	Username string `json:"username,omitempty"`
	Password []byte `json:"password,omitempty"`
}

// All commands are fixed read-only queries, executed as the already logged-in desktop user.
const linuxDisplayQuery = `set -eu
case "$XDG_SESSION_TYPE" in
 x11) exit 1;;
 wayland)
  locked=$(loginctl show-session auto -p LockedHint --value) || exit 1
  [ "$locked" = no ] || exit 1
  case ":$XDG_CURRENT_DESKTOP:" in
   *:GNOME:*|*:gnome:*|*:ubuntu:*)
    systemctl --user is-active --quiet gnome-remote-desktop.service
    printf 'wayland\n'
    LC_ALL=C grdctl status --show-credentials;;
   *:KDE:*|*:kde:*|*:plasma:*)
    systemctl --user is-active --quiet app-org.kde.krdpserver.service
    printf 'wayland\n';;
   *) exit 1;;
  esac;;
 *) exit 1;;
esac`

func queryDisplay(runtimeID, kind string) (displayCapability, error) {
	// Windows enhanced transport reports its own availability; vmicrdv can be trigger-started.
	if kind == "windows" {
		return displayCapability{Mode: "windows"}, nil
	}
	client, err := guestClient(runtimeID)
	if err != nil {
		return displayCapability{}, err
	}
	defer client.CloseIdleConnections()
	if kind == "wayland-login" {
		return legacyDisplayFromGuest(client, "wayland")
	}
	return displayFromGuest(client, kind)
}

func displayFromGuest(client *http.Client, kind string) (displayCapability, error) {
	if status, err := guestops.ReadStatus(context.Background(), client); err == nil {
		if protocolAvailability(status, kind) != "ready" {
			return displayCapability{}, errors.New("guest desktop is not ready")
		}
		return displayCapability{Mode: status.Desktop.Mode}, nil
	}
	return legacyDisplayFromGuest(client, kind)
}

func legacyDisplayFromGuest(client *http.Client, kind string) (displayCapability, error) {
	args := []string{"/bin/sh", "-c", linuxDisplayQuery}
	data, err := json.Marshal(guestops.Command{Args: args, Timeout: 2})
	if err != nil {
		return displayCapability{}, err
	}
	response, err := client.Post("http://guest/exec", "application/json", bytes.NewReader(data))
	if err != nil {
		return displayCapability{}, errors.New("guest display query unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return displayCapability{}, errors.New("guest display query rejected")
	}
	var result guestops.Result
	if err = json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&result); err != nil {
		return displayCapability{}, errors.New("invalid guest display response")
	}
	defer clear(result.Stdout)
	defer clear(result.Stderr)
	if result.ExitCode != 0 || result.Error != "" || result.Truncated {
		return displayCapability{}, errors.New("guest desktop is not ready")
	}
	capability, err := parseDisplay(result.Stdout)
	if err != nil {
		return displayCapability{}, err
	}
	if (kind == "windows") != (capability.Mode == "windows") || kind == "x11" && capability.Mode != "x11" || kind == "wayland" && capability.Mode != "wayland" {
		clear(capability.Password)
		return displayCapability{}, errors.New("guest desktop mode differs from configured mode")
	}
	return capability, nil
}

func parseDisplay(data []byte) (displayCapability, error) {
	first, body, _ := bytes.Cut(data, []byte{'\n'})
	result := displayCapability{Mode: strings.TrimSpace(string(first))}
	if result.Mode != "windows" && result.Mode != "wayland" {
		return displayCapability{}, errors.New("unknown guest desktop mode")
	}
	if result.Mode != "wayland" {
		return result, nil
	}
	inRDP, sawRDP, enabled := false, false, false
	for line := range bytes.SplitSeq(body, []byte{'\n'}) {
		if len(line) > 0 && line[0] != '\t' && line[0] != ' ' {
			inRDP = bytes.Equal(bytes.TrimSpace(line), []byte("RDP:"))
			sawRDP = sawRDP || inRDP
		}
		if !inRDP {
			continue
		}
		line = bytes.TrimSuffix(bytes.TrimLeft(line, "\t "), []byte{'\r'})
		if bytes.Equal(line, []byte("Status: enabled")) {
			enabled = true
		}
		if value, ok := bytes.CutPrefix(line, []byte("Username: ")); ok {
			result.Username = string(value)
		}
		if value, ok := bytes.CutPrefix(line, []byte("Password: ")); ok {
			result.Password = append(result.Password[:0], value...)
		}
	}
	if !sawRDP {
		return result, nil
	} // KRdp uses its existing saved/manual login.
	if !enabled || result.Username == "" || len(result.Username) > 256 || len(result.Password) == 0 || len(result.Password) > 1024 || bytes.ContainsAny(result.Password, "\x00\r\n") {
		clear(result.Password)
		return displayCapability{}, errors.New("GNOME desktop credentials unavailable")
	}
	return result, nil
}

func probeDisplay(runtimeID, kind string, log io.Writer) displayCapability {
	if kind == "x11" {
		return displayCapability{}
	}
	if kind == "windows" {
		return displayCapability{Mode: "windows"}
	}
	executable, err := os.Executable()
	if err != nil {
		return displayCapability{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "--console", "--detect-display", runtimeID, kind)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := command.Output()
	defer clear(output)
	var result displayCapability
	if err == nil && len(output) <= 8192 {
		err = json.Unmarshal(output, &result)
	}
	if err != nil || (result.Mode != "windows" && result.Mode != "wayland") {
		clear(result.Password)
		if log != nil {
			fmt.Fprintln(log, "增强桌面尚未就绪，使用基础控制台")
		}
		return displayCapability{}
	}
	return result
}

func (d *displayCapability) login() *loginCredential {
	defer clear(d.Password)
	if d.Username == "" || len(d.Password) == 0 {
		return nil
	}
	// The response is bounded and never written to logs or the host credential store.
	password := utf16.Encode([]rune(string(d.Password)))
	if len(password) == 0 || len(password) > 256 {
		clear(password)
		return nil
	}
	return &loginCredential{username: d.Username, password: password}
}

func (v *viewer) connectionFailed(err error) {
	v.fail(err)
	v.requestBasic()
}

func (v *viewer) activeSession(generation uintptr) bool {
	return !v.closed && v.generation == generation
}

func (v *viewer) acceptFallback(generation uintptr) bool {
	return v.activeSession(generation) && v.automatic && v.enhanced && v.fallbackPending
}

func (v *viewer) requestBasic() {
	if v.renderer != nil {
		if !v.reportedFailure {
			v.reportedFailure = true
			v.renderer.Report("failed", v.hwnd)
		}
		return
	}
	if !v.automatic || !v.enhanced || v.closed || v.fallbackPending {
		return
	}
	v.fallbackPending = true
	v.fallbackReason = v.status
	// Do not dismantle COM while processing one of its callbacks, or time out authentication UI.
	if v.hwnd != 0 {
		user32.NewProc("PostMessageW").Call(v.hwnd, 0x8002, v.generation, 0)
	}
}

func basicDisplayPipe(pipe string) string {
	if index := strings.Index(pipe, ".enhanced.linux-"); index >= 0 {
		return pipe[:index] + ".basic"
	}
	return strings.TrimSuffix(strings.TrimSuffix(pipe, ".enhanced"), ".basic") + ".basic"
}

func (v *viewer) fallbackBasic() {
	if !v.automatic || !v.enhanced || v.closed {
		return
	}
	if !v.fallbackPending {
		v.fallbackReason = v.status
	}
	v.retainFallbackReason()
	v.switchDisplay(false, true)
}

func (v *viewer) retainFallbackReason() {
	if v.toolbar != nil {
		v.toolbar.notice = "增强连接失败，已返回基础画面：" + v.fallbackReason
		v.toolbar.noticeError = true
	}
	v.headerHidden = false
}

func (v *viewer) switchDisplay(enhanced, automatic bool) {
	if v.host != nil {
		v.switchRenderer(enhanced, automatic)
		return
	}
	if v.hwnd != 0 {
		v.fail(errors.New("控制台渲染进程尚未初始化"))
		return
	}
	if v.closed {
		return
	}
	var capability displayCapability
	if enhanced && v.linux {
		kind := v.linuxMode
		if kind == "" {
			kind = "linux"
		}
		capability = probeDisplay(v.vmid, kind, v.log)
		defer clear(capability.Password)
		if capability.Mode == "" {
			v.fail(errors.New("客户机增强画面尚未就绪，保留当前画面"))
			return
		}
	}
	hwnd, header, headerClose := v.hwnd, v.header, v.headerClose
	headerHidden, toolbar := v.headerHidden, v.toolbar
	if enhanced && toolbar != nil {
		toolbar.notice, toolbar.noticeError = "", false
	}
	v.clearPrompt()
	pipe, runtimeID, title, log := basicDisplayPipe(v.pipe), v.vmid, v.title, v.log
	linux, clipboard, linuxMode, machineID := v.linux, v.clipboard, v.linuxMode, v.machineID
	v.close()
	if enhanced {
		pipe = strings.TrimSuffix(pipe, ".basic") + ".enhanced"
	}
	*v = viewer{hwnd: hwnd, header: header, headerClose: headerClose, headerHidden: headerHidden, toolbar: toolbar,
		pipe: pipe, vmid: runtimeID, title: title, log: log, generation: v.generation, automatic: automatic, enhanced: enhanced, linux: linux, clipboard: clipboard, linuxMode: linuxMode, machineID: machineID}
	if enhanced && linux {
		v.wayland = capability.Mode == "wayland"
	}
	if v.wayland {
		v.automaticLogin = capability.login()
		port, stop, err := linuxWaylandBridge(runtimeID, log)
		if err != nil {
			v.connectionFailed(err)
			return
		}
		v.stopBridge, v.rdpPort = stop, port
	} else if enhanced && linux {
		v.pipe += fmt.Sprintf(".linux-%d", os.Getpid())
		stop, err := linuxBridge(v.pipe, runtimeID, log)
		if err != nil {
			v.connectionFailed(err)
			return
		}
		v.stopBridge = stop
	}
	menu, _, _ := user32.NewProc("GetSystemMenu").Call(hwnd, 0)
	user32.NewProc("DeleteMenu").Call(menu, forgetLoginCommand, 0)
	if v.wayland {
		label := windows.StringToUTF16Ptr("忘记 Linux 登录凭据")
		user32.NewProc("AppendMenuW").Call(menu, 0, forgetLoginCommand, uintptr(unsafe.Pointer(label)))
	}
	mode := "基础控制台"
	if enhanced {
		mode = "增强会话"
	}
	caption := windows.StringToUTF16Ptr("VM · " + title + " · " + mode)
	user32.NewProc("SetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(caption)))
	v.setStatus("正在连接" + mode)
	if log != nil {
		fmt.Fprintln(log, "切换画面:", mode, "会话:", v.generation)
	}
	if err := v.initialize(); err != nil {
		v.connectionFailed(err)
	}
	user32.NewProc("SetTimer").Call(hwnd, 1, 300, 0)
}

func (v *viewer) owner() uintptr { return v.hwnd }

func (v *viewer) validParent() bool {
	var pid uint32
	user32.NewProc("GetWindowThreadProcessId").Call(v.parent, uintptr(unsafe.Pointer(&pid)))
	return pid != 0 && uintptr(pid) == v.parentPID
}

func (v *viewer) startRenderHost() {
	executable, err := os.Executable()
	if err != nil {
		v.fail(err)
		return
	}
	v.host = renderhost.New(func(w *renderhost.Worker) *exec.Cmd {
		pipe := basicDisplayPipe(v.pipe)
		if w.Enhanced {
			pipe = strings.TrimSuffix(pipe, ".basic") + ".enhanced"
		}
		args := []string{"--console", "--renderer", "--parent", strconv.FormatUint(uint64(v.hwnd), 10), "--parent-pid", strconv.Itoa(os.Getpid()), "--render-token", w.Token, "--render-generation", strconv.FormatUint(w.Generation, 10), "--pipe", pipe, "--vmid", v.vmid, "--machine-id", v.machineID, "--title", v.title}
		if w.Enhanced {
			args = append(args, "--enhanced")
		}
		if v.clipboard {
			args = append(args, "--clipboard")
		}
		if v.linux {
			args = append(args, "--linux")
		}
		if v.linuxMode == "wayland" {
			args = append(args, "--wayland")
		}
		if v.linuxMode == "x11" {
			args = append(args, "--x11")
		}
		command := exec.Command(executable, args...)
		command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		return command
	})
	// GPU-PV guests can reject a second Windows display stack while the old one is still open.
	v.host.Sequential = !v.linux
	v.host.Begin(v.enhanced)
}

func (v *viewer) switchRenderer(enhanced, automatic bool) {
	if enhanced && v.linux && v.linuxMode == "x11" {
		v.fail(errors.New("旧 X11 / Xfce 增强方式已停用，请使用基础画面或现有 GNOME/KDE Wayland 桌面"))
		return
	}
	if v.closed {
		return
	}
	v.automatic = automatic
	v.clearPrompt()
	if v.toolbar != nil {
		v.toolbar.availability.notified = true
	}
	if !enhanced {
		// The user's Basic command also explicitly cancels any hung candidate.
		v.host.Cancel()
		if v.host.Active.BasicConnected() {
			v.setStatus("已保留基础画面")
			return
		}
	}
	if v.host.Candidate != nil {
		return
	}
	if !v.host.Begin(enhanced) {
		v.fail(errors.New("无法启动控制台渲染进程"))
		return
	}
	if v.host.Sequential {
		v.renderWindow = 0
		v.display.connected = false
	}
	v.revealHeader()
	if v.host.Sequential {
		v.setStatus("正在切换会话 · 请完成登录和证书确认；点击基础可返回基础画面")
	} else {
		v.setStatus("正在连接候选画面 · 当前画面保持打开；点击基础可取消增强连接")
	}
	v.resize()
}

func (v *viewer) pollRenderer() {
	if v.host == nil || v.closed {
		return
	}
	previous := v.host.Active
	connectingEnhanced := v.host.Candidate != nil && v.host.Candidate.Enhanced
	changed, failed := v.host.Poll(func(p renderhost.Packet) bool {
		var pid uint32
		user32.NewProc("GetWindowThreadProcessId").Call(uintptr(p.Window), uintptr(unsafe.Pointer(&pid)))
		parent, _, _ := user32.NewProc("GetParent").Call(uintptr(p.Window))
		owner, _, _ := user32.NewProc("GetWindow").Call(uintptr(p.Window), 4)
		root, _, _ := user32.NewProc("GetAncestor").Call(uintptr(p.Window), 2)
		return pid != 0 && int(pid) == p.PID && parent == 0 && owner == 0 && root == uintptr(p.Window)
	})
	if changed {
		if v.renderWindow != 0 {
			v.positionRenderer(v.renderWindow, false, 0, displaySettings{})
		}
		v.renderWindow = 0
		v.display.connected = false
		if active := v.host.Active; active != nil {
			v.renderWindow, v.enhanced = uintptr(active.Window), active.Enhanced
			v.display.connected = active.Connected
			mode := "基础控制台"
			if v.enhanced {
				mode = "增强会话"
			}
			caption := windows.StringToUTF16Ptr("VM · " + v.title + " · " + mode)
			user32.NewProc("SetWindowTextW").Call(v.hwnd, uintptr(unsafe.Pointer(caption)))
			v.setStatus("已连接 · 关闭控制台后虚拟机继续运行")
			v.focusRenderer()
		}
	}
	if failed {
		v.revealHeader()
		v.setStatus("连接失败 · 当前画面保持打开；点击基础取消候选连接，或重试增强")
		v.statusError = true
		v.renderStatus()
	}
	if failed && (renderhost.ShouldRestoreBasic(v.automatic, previous, v.host.Active) || v.host.Sequential && connectingEnhanced && v.host.Active == nil && v.host.Candidate == nil) {
		v.display.connected = false
		v.switchRenderer(false, true)
	}
	if changed || failed {
		v.resize()
	}
}

func (v *viewer) positionRenderer(window uintptr, visible bool, top int32, settings displaySettings) {
	point := [2]int32{0, top}
	user32.NewProc("ClientToScreen").Call(v.hwnd, uintptr(unsafe.Pointer(&point)))
	v.host.Position(uint64(window), point[0], point[1], settings.width, settings.height, visible)
}

func (v *viewer) resizeRenderer() {
	if v.closed || v.hwnd == 0 {
		return
	}
	v.updateHostLayer()
	var rect [4]int32
	user32.NewProc("GetClientRect").Call(v.hwnd, uintptr(unsafe.Pointer(&rect)))
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(v.hwnd)
	if dpi == 0 {
		dpi = 96
	}
	top, settings := v.contentLayout(rect[2], rect[3], dpi)
	v.layoutToolbar(dpi)
	v.layoutNotice(settings.width, top, v.toolbarHeight(dpi), dpi)
	shown, _, _ := user32.NewProc("IsWindowVisible").Call(v.hwnd)
	minimized, _, _ := user32.NewProc("IsIconic").Call(v.hwnd)
	// An inactive console remains visible like any other window; foreground changes are not minimize events.
	visible := rendererVisible(shown != 0, minimized != 0)
	if v.renderWindow != 0 {
		v.positionRenderer(v.renderWindow, visible && settings.width > 0 && settings.height > 0, top, settings)
	}
	if candidate := v.host.Candidate; candidate != nil && candidate.Window != 0 {
		if candidate.Connected {
			// Keep the old renderer until the candidate acknowledges a usable, visible layout.
			v.positionRenderer(uintptr(candidate.Window), visible && settings.width >= 200 && settings.height >= 200, top, settings)
		} else {
			point := [2]int32{settings.width / 2, top + settings.height/2}
			user32.NewProc("ClientToScreen").Call(v.hwnd, uintptr(unsafe.Pointer(&point)))
			v.host.Position(candidate.Window, point[0], point[1], 1, 1, visible)
		}
	}
}

func rendererVisible(shown, minimized bool) bool { return shown && !minimized }

func rendererPositionFlags(visible, foregroundIsHostOrRenderer bool) uintptr {
	flags := uintptr(0x10) // SWP_NOACTIVATE: moving the image must not steal keyboard focus.
	if !visible {
		return flags | 0x80 | 0x1 | 0x2 | 0x4
	}
	if !foregroundIsHostOrRenderer {
		// Keep unrelated windows and renderer-owned authentication dialogs above the image.
		flags |= 0x4
	}
	return flags | 0x40
}

// A popup cannot reliably rise above the foreground host after native move/size input.
// Use topmost only while this exact host/image pair is foreground, then explicitly demote it.
func rendererZOrder(visible, foregroundIsHostOrRenderer, wasTopmost bool) (uintptr, uintptr) {
	flags := rendererPositionFlags(visible, foregroundIsHostOrRenderer)
	if visible && foregroundIsHostOrRenderer {
		return ^uintptr(0), flags &^ 0x4 // HWND_TOPMOST
	}
	if wasTopmost {
		return ^uintptr(1), flags &^ 0x4 // HWND_NOTOPMOST; SWP_NOZORDER would prevent demotion.
	}
	return 0, flags | 0x4
}

var rendererForegroundCallback = syscall.NewCallback(func(_ uintptr, _ uint32, _ uintptr, _, _ int32, _, _ uint32) uintptr {
	if v := current; v != nil && !v.closed {
		if v.rendererMode && v.rendererLayoutKnown {
			v.updateRendererLayer()
		} else if v.host != nil {
			v.resizeRenderer()
		}
	}
	return 0
})

func windowAbove(upper, lower uintptr, next func(uintptr) uintptr) bool {
	if upper == 0 || lower == 0 || upper == lower {
		return false
	}
	for i, window := 0, next(upper); i < 512 && window != 0; i, window = i+1, next(window) {
		if window == lower {
			return true
		}
	}
	return false
}

func nextWindow(window uintptr) uintptr {
	handle, _, _ := user32.NewProc("GetWindow").Call(window, 2) // GW_HWNDNEXT
	return handle
}

func lowerBehindForeground(window, foreground uintptr) {
	if foreground == 0 || foreground == window {
		return
	}
	style, _, _ := user32.NewProc("GetWindowLongPtrW").Call(foreground, ^uintptr(19))
	if style&0x8 == 0 && windowAbove(window, foreground, nextWindow) {
		user32.NewProc("SetWindowPos").Call(window, foreground, 0, 0, 0, 0, 0x13)
	}
}

func (v *viewer) updateHostLayer() {
	if v.host == nil || v.adjustingLayer || v.closed {
		return
	}
	v.adjustingLayer = true
	defer func() { v.adjustingLayer = false }()
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	shown, _, _ := user32.NewProc("IsWindowVisible").Call(v.hwnd)
	minimized, _, _ := user32.NewProc("IsIconic").Call(v.hwnd)
	active := shown != 0 && minimized == 0 && (foreground == v.hwnd || v.renderWindow != 0 && foreground == v.renderWindow)
	if active != v.hostTopmost {
		order := ^uintptr(1)
		if active {
			order = ^uintptr(0)
		}
		if ok, _, _ := user32.NewProc("SetWindowPos").Call(v.hwnd, order, 0, 0, 0, 0, 0x13); ok != 0 {
			v.hostTopmost = active
		}
	}
	if !active && shown != 0 && minimized == 0 {
		lowerBehindForeground(v.hwnd, foreground)
	}
	if v.renderWindow == 0 {
		return
	}
	style, _, _ := user32.NewProc("GetWindowLongPtrW").Call(v.renderWindow, ^uintptr(19))
	if (style&0x8 != 0) != v.hostTopmost {
		return
	}
	if windowAbove(v.hwnd, v.renderWindow, nextWindow) {
		// Lower only our own window, never raise an inactive console above another application.
		user32.NewProc("SetWindowPos").Call(v.hwnd, v.renderWindow, 0, 0, 0, 0, 0x13)
	}
}

func (v *viewer) updateRendererLayer() {
	foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
	order, flags := rendererZOrder(v.rendererLayoutVisible, foreground == v.parent || foreground == v.hwnd, v.rendererTopmost)
	if ok, _, _ := user32.NewProc("SetWindowPos").Call(v.hwnd, order, 0, 0, 0, 0, flags|0x1|0x2); ok != 0 {
		v.rendererTopmost = order == ^uintptr(0)
	}
	if v.rendererLayoutVisible && foreground != v.parent && foreground != v.hwnd {
		lowerBehindForeground(v.hwnd, foreground)
	}
}

func (v *viewer) rendererCommands() {
	if v.renderer == nil {
		return
	}
	for command := v.renderer.Next(); command.Kind != ""; command = v.renderer.Next() {
		switch command.Kind {
		case "layout":
			v.rendererLayoutKnown, v.rendererLayoutVisible = true, command.Visible
			foreground, _, _ := user32.NewProc("GetForegroundWindow").Call()
			order, flags := rendererZOrder(command.Visible, foreground == v.parent || foreground == v.hwnd, v.rendererTopmost)
			ok, _, err := user32.NewProc("SetWindowPos").Call(v.hwnd, order, uintptr(command.X), uintptr(command.Y), uintptr(command.Width), uintptr(command.Height), flags)
			if ok == 0 {
				v.connectionFailed(fmt.Errorf("同步渲染窗口位置失败: %w", err))
				continue
			}
			v.rendererTopmost = order == ^uintptr(0)
			if command.Visible && foreground != v.parent && foreground != v.hwnd {
				lowerBehindForeground(v.hwnd, foreground)
			}
			v.resize()
			if !v.reportedLayout && !v.reportedFailure && v.reportedConnected && command.Visible && command.Width >= 200 && command.Height >= 200 && v.layout.width == command.Width && v.layout.height == command.Height {
				v.reportedLayout = true
				v.renderer.ReportLayout(v.hwnd, v.layout.width, v.layout.height)
			}
		case "close":
			user32.NewProc("PostMessageW").Call(v.hwnd, 0x10, 0, 0)
		case "keys":
			if err := v.sendControlAltDelete(); err != nil {
				v.fail(err)
			}
		case "release-focus":
			v.releaseGuestFocus()
		case "focus":
			if !v.validParent() || !rendererCanFocus(v.parent, v.hwnd) {
				continue
			}
			if ok, _, _ := user32.NewProc("SetForegroundWindow").Call(v.hwnd); ok != 0 {
				if foreground, _, _ := user32.NewProc("GetForegroundWindow").Call(); foreground == v.hwnd {
					user32.NewProc("SetFocus").Call(v.child)
				}
			}
		case "forget":
			v.forgetLogin()
		}
	}
}
