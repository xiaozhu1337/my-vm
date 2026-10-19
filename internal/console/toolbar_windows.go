//go:build windows

package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

type toolbarButton struct {
	id     uintptr
	label  string
	width  int32
	handle uintptr
}

type consoleToolbar struct {
	buttons          []toolbarButton
	ctx              context.Context
	cancel           context.CancelFunc
	results          chan toolbarResult
	availability     enhancedAvailability
	busy, fullscreen bool
	notice           string
	bounds           [4]int32
	style            uintptr
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
	v.toolbar = &consoleToolbar{ctx: ctx, cancel: cancel, results: make(chan toolbarResult, 1), buttons: []toolbarButton{
		{id: toolbarBasic, label: "基础画面", width: 82},
		{id: toolbarEnhanced, label: "增强画面", width: 82},
		{id: toolbarKeys, label: "Ctrl+Alt+Del", width: 112},
		{id: toolbarFullscreen, label: "全屏 / 还原", width: 108},
		{id: toolbarPause, label: "暂停 / 继续", width: 108},
		{id: toolbarShutdown, label: "正常关机", width: 90},
	}}
	font, _, _ := windows.NewLazySystemDLL("gdi32.dll").NewProc("GetStockObject").Call(17)
	for i := range v.toolbar.buttons {
		button := &v.toolbar.buttons[i]
		button.handle, _, _ = user32.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("BUTTON"))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(button.label))), 0x50010000, 0, 0, 1, 1, v.hwnd, button.id, 0, 0)
		if button.handle == 0 {
			cancel()
			return errors.New("创建客户机工具栏失败")
		}
		user32.NewProc("SendMessageW").Call(button.handle, 0x30, font, 1)
	}
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
	x, margin, height := int32(6*dpi/96), int32(6*dpi/96), int32(28*dpi/96)
	for _, button := range v.toolbar.buttons {
		width := button.width * int32(dpi) / 96
		user32.NewProc("MoveWindow").Call(button.handle, uintptr(x), uintptr(margin), uintptr(width), uintptr(height), 1)
		x += width + margin
		enabled := !v.toolbar.busy
		switch button.id {
		case toolbarBasic:
			enabled = enabled && v.enhanced
		case toolbarEnhanced:
			enabled = enabled && !v.enhanced
		case toolbarKeys:
			enabled = enabled && v.nonScriptable != nil
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
	}
}

func (v *viewer) toolbarCommand(id, source uintptr) bool {
	if v.toolbar == nil || v.closed {
		return false
	}
	for _, button := range v.toolbar.buttons {
		if id != button.id || source != button.handle {
			continue
		}
		if v.toolbar.busy && id != toolbarFullscreen {
			return true
		}
		switch id {
		case toolbarBasic:
			if v.enhanced {
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
			answer, _, _ := user32.NewProc("MessageBoxW").Call(v.hwnd, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("正常关闭此虚拟机？请先保存客户机中的文件。"))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("确认关机"))), 0x124)
			if answer == 6 {
				v.startToolbarAction("stop")
			}
		}
		v.layout = displaySettings{}
		v.resize()
		return true
	}
	return false
}

func controlAltDeleteKeys() ([6]int16, [6]uint32) {
	return [6]int16{0, 0, 0, -1, -1, -1}, [6]uint32{0x001d0001, 0x00380001, 0x21530001, 0xe1530001, 0xe0380001, 0xc01d0001}
}

func (v *viewer) sendControlAltDelete() error {
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
	bar.notice = "客户机操作进行中"
	v.setStatus("正在执行客户机操作…")
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
	v.fail(err)
	if v.headerHidden {
		user32.NewProc("MessageBoxW").Call(v.hwnd, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(err.Error()))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("客户机操作失败"))), 0x10)
	}
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
	return nil
}
