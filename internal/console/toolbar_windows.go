//go:build windows

package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"my-vm/internal/console/chrome"
	"my-vm/internal/control"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	toolbarBasic = 200 + iota
	toolbarEnhanced
	toolbarKeys
	toolbarFullscreen
	toolbarPause
	toolbarShutdown
	toolbarConfirm
	toolbarCancel
)

type toolbarPrompt uint8

const (
	promptNone toolbarPrompt = iota
	promptEnhanced
	promptEnhancedTry
	promptShutdown
)

type toolbarButton struct {
	id     uintptr
	label  string
	width  int32
	handle uintptr
}

type consoleToolbar struct {
	buttons                     []toolbarButton
	painter                     *chrome.Painter
	confirmButton, cancelButton uintptr
	prompt                      toolbarPrompt
	promptStatus                string
	promptError                 bool
	noticeError                 bool
	ctx                         context.Context
	cancel                      context.CancelFunc
	results                     chan toolbarResult
	availability                enhancedAvailability
	busy, fullscreen            bool
	notice                      string
	bounds                      [4]int32
	style                       uintptr
}

type toolbarResult struct {
	status string
	err    error
}

func machineIDFromPipe(pipe string) string {
	id, mode, ok := strings.Cut(strings.TrimPrefix(pipe, `\\.\pipe\my-vm-`), ".")
	if !strings.HasPrefix(pipe, `\\.\pipe\my-vm-`) || !ok || len(id) != 32 || (mode != "basic" && mode != "enhanced" && !strings.HasPrefix(mode, "enhanced.linux-")) {
		return ""
	}
	for _, ch := range id {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return ""
		}
	}
	return id
}

func (v *viewer) createToolbar() error {
	ctx, cancel := context.WithCancel(context.Background())
	v.toolbar = &consoleToolbar{painter: &chrome.Painter{}, ctx: ctx, cancel: cancel, results: make(chan toolbarResult, 1), buttons: []toolbarButton{
		{id: toolbarBasic, label: "基础", width: 60},
		{id: toolbarEnhanced, label: "增强", width: 60},
		{id: toolbarKeys, label: "Ctrl+Alt+Del", width: 112},
		{id: toolbarFullscreen, label: "全屏", width: 88},
		{id: toolbarPause, label: "暂停 / 继续", width: 96},
		{id: toolbarShutdown, label: "关机", width: 64},
	}}
	for i := range v.toolbar.buttons {
		button := &v.toolbar.buttons[i]
		button.handle, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("BUTTON"))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(button.label))), 0x5001000b, 0, 0, 1, 1, v.hwnd, button.id, 0, 0)
		if button.handle == 0 {
			cancel()
			return errors.New("创建客户机工具栏失败")
		}
		chrome.Track(button.handle)
	}
	for _, button := range []struct {
		id     uintptr
		target *uintptr
		text   string
	}{
		{toolbarConfirm, &v.toolbar.confirmButton, "确认"}, {toolbarCancel, &v.toolbar.cancelButton, "暂不"},
	} {
		*button.target, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("BUTTON"))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(button.text))), 0x4001000b, 0, 0, 1, 1, v.hwnd, button.id, 0, 0)
		if *button.target == 0 {
			cancel()
			return errors.New("创建消息操作按钮失败")
		}
		chrome.Track(*button.target)
	}
	chrome.Track(v.headerClose)
	return nil
}

func (v *viewer) toolbarHeight(dpi uintptr) int32 {
	if v.toolbar == nil {
		return 0
	}
	return int32(40 * dpi / 96)
}

func (v *viewer) layoutToolbar(dpi uintptr) {
	if v.toolbar == nil {
		return
	}
	v.toolbar.painter.Font(dpi)
	x, margin, height := int32(12*dpi/96), int32(4*dpi/96), int32(28*dpi/96)
	for _, button := range v.toolbar.buttons {
		if button.id == toolbarKeys || button.id == toolbarPause {
			x += int32(16 * dpi / 96)
		}
		width := button.width * int32(dpi) / 96
		user32.NewProc("MoveWindow").Call(button.handle, uintptr(x), uintptr(6*dpi/96), uintptr(width), uintptr(height), 1)
		x += width + margin
		enabled := !v.toolbar.busy
		switch button.id {
		case toolbarBasic:
			enabled = enabled && (v.enhanced || v.automatic || v.host != nil && (!v.host.Active.BasicConnected() || v.host.Candidate != nil))
		case toolbarEnhanced:
			enabled = enabled && !v.enhanced && !(v.linux && v.linuxMode == "x11") && (v.host == nil || v.host.Candidate == nil)
		case toolbarKeys:
			enabled = enabled && (v.nonScriptable != nil || v.host != nil && v.host.Active != nil && v.host.Active.Connected)
		case toolbarPause, toolbarShutdown:
			enabled = enabled && v.machineID != ""
		case toolbarFullscreen:
			enabled = true
		}
		value := uintptr(0)
		if enabled {
			value = 1
		}
		user32.NewProc("EnableWindow").Call(button.handle, value)
		user32.NewProc("InvalidateRect").Call(button.handle, 0, 0)
	}
}

func (v *viewer) toolbarCommand(id, source uintptr) bool {
	if v.toolbar == nil || v.closed {
		return false
	}
	bar := v.toolbar
	if bar.prompt != promptNone && ((id == toolbarConfirm && source == bar.confirmButton && source != 0) || (id == toolbarCancel && source == bar.cancelButton && source != 0)) {
		if !bar.busy {
			v.finishPrompt(id == toolbarConfirm)
		}
		return true
	}
	for _, button := range v.toolbar.buttons {
		if id != button.id || source != button.handle {
			continue
		}
		if v.toolbar.busy && id != toolbarFullscreen {
			return true
		}
		if id != toolbarFullscreen {
			v.clearPrompt()
		}
		switch id {
		case toolbarBasic:
			if v.host != nil || v.enhanced {
				v.switchDisplay(false, false)
			}
		case toolbarEnhanced:
			if !v.enhanced {
				v.switchDisplay(true, true)
			}
		case toolbarKeys:
			if err := v.sendControlAltDelete(); err != nil {
				v.toolbarError(err)
			}
		case toolbarFullscreen:
			if err := v.toggleFullscreen(); err != nil {
				v.toolbarError(err)
			}
		case toolbarPause:
			v.startToolbarAction("toggle-pause")
		case toolbarShutdown:
			v.showPrompt(promptShutdown)
		}
		v.layout = displaySettings{}
		v.resize()
		return true
	}
	return false
}

func (v *viewer) toolbarNavigation(msg message) bool {
	if msg.Message != 0x100 || msg.WParam != 9 || v.toolbar == nil {
		return false
	}
	// Enumerate only our controls; IsDialogMessage can synchronously query the renderer.
	controls := make([]uintptr, 0, 10)
	for _, button := range v.toolbar.buttons {
		enabled, _, _ := user32.NewProc("IsWindowEnabled").Call(button.handle)
		if enabled != 0 {
			controls = append(controls, button.handle)
		}
	}
	if v.toolbar.prompt != promptNone {
		controls = append(controls, v.toolbar.confirmButton, v.toolbar.cancelButton)
	}
	if !v.headerHidden {
		controls = append(controls, v.headerClose)
	}
	if len(controls) == 0 {
		return false
	}
	focus, _, _ := user32.NewProc("GetFocus").Call()
	index := -1
	for i, handle := range controls {
		if handle == focus {
			index = i
			break
		}
	}
	shift, _, _ := user32.NewProc("GetKeyState").Call(0x10)
	if shift&0x8000 != 0 {
		index = (index - 1 + len(controls)) % len(controls)
	} else {
		index = (index + 1) % len(controls)
	}
	user32.NewProc("SetFocus").Call(controls[index])
	return true
}

func controlAltDeleteKeys() ([6]int16, [6]uint32) {
	return [6]int16{0, 0, 0, -1, -1, -1}, [6]uint32{0x001d0001, 0x00380001, 0x21530001, 0xe1530001, 0xe0380001, 0xc01d0001}
}

func (v *viewer) sendControlAltDelete() error {
	if v.host != nil {
		if v.host.Active == nil || !v.host.Active.Connected || !v.host.Active.Send("keys") {
			return errors.New("请先连接客户机画面，再发送 Ctrl+Alt+Del")
		}
		return nil
	}
	if v.nonScriptable == nil || !v.display.connected {
		return errors.New("请先连接客户机画面，再发送 Ctrl+Alt+Del")
	}
	up, keys := controlAltDeleteKeys()
	return call(v.nonScriptable, 14, 6, uintptr(unsafe.Pointer(&up[0])), uintptr(unsafe.Pointer(&keys[0])))
}

func (v *viewer) startToolbarAction(action string) {
	bar := v.toolbar
	if bar == nil || bar.busy || v.machineID == "" {
		return
	}
	bar.busy = true
	bar.notice, bar.noticeError = "客户机操作进行中", false
	v.revealHeader()
	v.setStatus("正在执行客户机操作…")
	v.resize()
	hwnd, id := v.hwnd, v.machineID
	go func() {
		ctx, cancel := context.WithTimeout(bar.ctx, 130*time.Second)
		defer cancel()
		client, err := control.Client(ctx)
		status := ""
		if err == nil {
			defer client.CloseIdleConnections()
			status, err = toolbarPower(ctx, client, id, action)
		}
		if bar.ctx.Err() != nil {
			return
		}
		bar.results <- toolbarResult{status, err}
		user32.NewProc("PostMessageW").Call(hwnd, 0x8003, 0, 0)
	}()
}

func toolbarPower(ctx context.Context, client *http.Client, id, action string) (string, error) {
	request := func(method, action string) (*http.Response, error) {
		query := url.Values{"vm": {id}, "action": {action}}
		r, err := http.NewRequestWithContext(ctx, method, "http://manager/v1/action?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(r)
		if err != nil {
			return nil, fmt.Errorf("请求中断，操作结果尚未确认，请先查询虚拟机状态: %w", err)
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
			defer response.Body.Close()
			text, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
			return nil, fmt.Errorf("客户机操作失败: %s", strings.TrimSpace(string(text)))
		}
		return response, nil
	}
	if action == "toggle-pause" {
		response, err := request(http.MethodGet, "state")
		if err != nil {
			return "", err
		}
		var state string
		err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&state)
		response.Body.Close()
		if err != nil {
			return "", err
		}
		switch state {
		case "已暂停":
			action = "resume"
		case "运行中":
			action = "pause"
		default:
			return "", errors.New("虚拟机未运行")
		}
	}
	if action != "pause" && action != "resume" && action != "stop" {
		return "", errors.New("不支持的工具栏操作")
	}
	response, err := request(http.MethodPost, action)
	if err != nil {
		return "", err
	}
	response.Body.Close()
	return map[string]string{"pause": "虚拟机已暂停", "resume": "虚拟机已恢复运行", "stop": "已发送正常关机请求"}[action], nil
}

func (v *viewer) finishToolbarAction() {
	if v.toolbar == nil || v.closed {
		return
	}
	select {
	case result := <-v.toolbar.results:
		v.toolbar.busy = false
		v.toolbar.notice = result.status
		v.toolbar.noticeError = result.err != nil
		if result.err != nil {
			v.toolbar.notice = result.err.Error()
			v.toolbarError(result.err)
		} else {
			v.setStatus(result.status)
		}
		v.layout = displaySettings{}
		v.resize()
	default:
	}
}

func (v *viewer) toolbarError(err error) {
	v.revealHeader()
	v.fail(err)
	v.layout = displaySettings{}
	v.resize()
}

func (v *viewer) clearPrompt() {
	if v.toolbar == nil || v.toolbar.prompt == promptNone {
		return
	}
	if v.toolbar.prompt == promptEnhanced || v.toolbar.prompt == promptEnhancedTry {
		v.toolbar.availability.notified = false
		v.toolbar.availability.next = time.Now().Add(15 * time.Second)
	}
	v.toolbar.prompt = promptNone
	v.status, v.statusError = v.toolbar.promptStatus, v.toolbar.promptError
	v.toolbar.promptStatus, v.toolbar.promptError = "", false
	v.renderStatus()
	for _, handle := range []uintptr{v.toolbar.confirmButton, v.toolbar.cancelButton} {
		if handle != 0 {
			user32.NewProc("ShowWindow").Call(handle, 0)
		}
	}
	v.layout = displaySettings{}
}

func (v *viewer) revealHeader() {
	v.headerHidden = false
	for _, handle := range []uintptr{v.header, v.headerClose} {
		if handle != 0 {
			user32.NewProc("ShowWindow").Call(handle, 5)
		}
	}
	v.layout = displaySettings{}
}

func (p toolbarPrompt) labels() (string, string, string) {
	switch p {
	case promptEnhancedTry:
		return "暂未确认增强连接状态，可尝试切换；连接失败会返回基础画面。", "尝试增强", "暂不"
	case promptEnhanced:
		return "增强画面已就绪，可切换以使用更多桌面功能。", "切换增强", "暂不"
	case promptShutdown:
		return "正常关闭虚拟机？请先保存客户机中的文件。", "确认关机", "取消"
	}
	return "", "", ""
}

func (v *viewer) showPrompt(prompt toolbarPrompt) {
	if v.closed || v.toolbar == nil {
		return
	}
	if v.toolbar.prompt == promptNone {
		v.toolbar.promptStatus, v.toolbar.promptError = v.status, v.statusError
	}
	v.toolbar.prompt = prompt
	v.revealHeader()
	v.setStatus("")
	v.resize()
	if prompt == promptShutdown && v.toolbar.cancelButton != 0 {
		user32.NewProc("SetFocus").Call(v.toolbar.cancelButton)
	}
}

func (v *viewer) finishPrompt(confirmed bool) {
	if v.closed || v.toolbar == nil || v.toolbar.busy {
		return
	}
	prompt := v.toolbar.prompt
	v.dismissHeader()
	if confirmed && (prompt == promptEnhanced || prompt == promptEnhancedTry) {
		v.toolbar.availability.notified = false
	}
	if !confirmed {
		return
	}
	switch prompt {
	case promptEnhanced, promptEnhancedTry:
		if !v.enhanced {
			v.switchDisplay(true, true)
		}
	case promptShutdown:
		v.startToolbarAction("stop")
	}
}

func (v *viewer) layoutNotice(width, top, barHeight int32, dpi uintptr) {
	unit := func(value int32) int32 { return value * int32(dpi) / 96 }
	bar := v.toolbar
	show := !v.headerHidden
	x := width - unit(40)
	user32.NewProc("MoveWindow").Call(v.headerClose, uintptr(x), uintptr(barHeight+unit(4)), uintptr(unit(24)), uintptr(unit(24)), 1)
	if bar != nil {
		_, confirm, cancel := bar.prompt.labels()
		for _, button := range []struct {
			window uintptr
			text   string
		}{{bar.cancelButton, cancel}, {bar.confirmButton, confirm}} {
			visible := uintptr(0)
			if show && bar.prompt != promptNone {
				visible = 5
				x -= unit(88)
				user32.NewProc("SetWindowTextW").Call(button.window, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(button.text))))
				user32.NewProc("MoveWindow").Call(button.window, uintptr(x), uintptr(barHeight+unit(2)), uintptr(unit(84)), uintptr(unit(28)), 1)
			}
			user32.NewProc("ShowWindow").Call(button.window, visible)
			user32.NewProc("EnableWindow").Call(button.window, boolValue(!bar.busy))
		}
	}
	if show {
		user32.NewProc("MoveWindow").Call(v.header, uintptr(unit(12)), uintptr(barHeight+unit(2)), uintptr(max(x-unit(18), 1)), uintptr(top-barHeight-unit(4)), 1)
	}
}

func boolValue(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

func (v *viewer) paintControl(address uintptr) bool {
	if v.toolbar == nil || v.toolbar.painter == nil {
		return false
	}
	item, ok := chrome.ReadItem(address)
	if !ok {
		return false
	}
	bar := v.toolbar
	var style chrome.Button
	if item.Window == v.header && item.Kind == 5 {
		dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(v.hwnd)
		bar.painter.Font(dpi)
		bar.painter.Banner(item, v.status, v.statusError)
		return true
	}
	if item.Kind != 4 {
		return false
	}
	switch item.Window {
	case v.headerClose:
		style = chrome.Button{Close: true}
	case bar.confirmButton:
		_, text, _ := bar.prompt.labels()
		style = chrome.Button{Text: text, Primary: true}
	case bar.cancelButton:
		_, _, text := bar.prompt.labels()
		style = chrome.Button{Text: text}
	default:
		found := false
		for _, button := range bar.buttons {
			if item.Window == button.handle {
				style = chrome.Button{Text: button.label, Selected: button.id == toolbarEnhanced && v.enhanced || button.id == toolbarBasic && !v.enhanced, Danger: button.id == toolbarShutdown}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(v.hwnd)
	bar.painter.Font(dpi)
	bar.painter.Button(item, style)
	return true
}

func (v *viewer) toggleFullscreen() error {
	bar := v.toolbar
	if bar == nil {
		return errors.New("工具栏尚未就绪")
	}
	if !bar.fullscreen {
		var monitor struct {
			size         uint32
			bounds, work [4]int32
			flags        uint32
		}
		monitor.size = uint32(unsafe.Sizeof(monitor))
		handle, _, _ := user32.NewProc("MonitorFromWindow").Call(v.hwnd, 2)
		ok, _, _ := user32.NewProc("GetMonitorInfoW").Call(handle, uintptr(unsafe.Pointer(&monitor)))
		if ok == 0 {
			return errors.New("无法读取显示器范围")
		}
		user32.NewProc("GetWindowRect").Call(v.hwnd, uintptr(unsafe.Pointer(&bar.bounds)))
		bar.style, _, _ = user32.NewProc("GetWindowLongPtrW").Call(v.hwnd, ^uintptr(15))
		user32.NewProc("SetWindowLongPtrW").Call(v.hwnd, ^uintptr(15), bar.style & ^uintptr(0x00cf0000))
		user32.NewProc("SetWindowPos").Call(v.hwnd, 0, uintptr(monitor.bounds[0]), uintptr(monitor.bounds[1]), uintptr(monitor.bounds[2]-monitor.bounds[0]), uintptr(monitor.bounds[3]-monitor.bounds[1]), 0x24)
	} else {
		user32.NewProc("SetWindowLongPtrW").Call(v.hwnd, ^uintptr(15), bar.style)
		user32.NewProc("SetWindowPos").Call(v.hwnd, 0, uintptr(bar.bounds[0]), uintptr(bar.bounds[1]), uintptr(bar.bounds[2]-bar.bounds[0]), uintptr(bar.bounds[3]-bar.bounds[1]), 0x24)
	}
	bar.fullscreen = !bar.fullscreen
	for i := range bar.buttons {
		if bar.buttons[i].id == toolbarFullscreen {
			label := "全屏"
			if bar.fullscreen {
				label = "退出全屏"
			}
			bar.buttons[i].label = label
			user32.NewProc("SetWindowTextW").Call(bar.buttons[i].handle, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(label))))
		}
	}
	return nil
}
