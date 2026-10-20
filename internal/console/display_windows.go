//go:build windows

package console

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"go.yaml.in/yaml/v3"
)

const connectedNotice = "已连接 · 关闭控制台后虚拟机继续运行"

func noticePreferencePath() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "my-vm", "console.yaml"), err
}

func (v *viewer) loadNoticePreference() {
	path, err := noticePreferencePath()
	if err == nil {
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			var settings struct {
				HideConnectedNotice bool `yaml:"hide_connected_notice"`
			}
			err = yaml.Unmarshal(data, &settings)
			if err == nil {
				v.hideConnectedNotice = settings.HideConnectedNotice
			}
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) && v.log != nil {
		fmt.Fprintln(v.log, "读取控制台通知设置失败:", err)
	}
}

func (v *viewer) canHideConnectedNotice() bool {
	return v.status == connectedNotice && !v.statusError && (v.toolbar == nil || v.toolbar.prompt == promptNone && v.toolbar.notice == "")
}

func (v *viewer) suppressConnectedNotice() {
	path, err := noticePreferencePath()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0700)
	}
	if err == nil {
		err = os.WriteFile(path, []byte("# Hide the routine connected notice in future console windows.\nhide_connected_notice: true\n"), 0600)
	}
	if err != nil {
		v.toolbarError(fmt.Errorf("保存控制台通知设置失败: %w", err))
		return
	}
	v.hideConnectedNotice = true
	v.dismissHeader()
}

type displaySettings struct {
	width, height int32
	dpi           uintptr
}

func (v *viewer) initialDesktop() displaySettings {
	if v.renderer != nil {
		initial := v.renderer.InitialDisplay
		return validDesktop(displaySettings{initial.Width, initial.Height, uintptr(initial.DPI)})
	}
	var rect [4]int32
	user32.NewProc("GetClientRect").Call(v.hwnd, uintptr(unsafe.Pointer(&rect)))
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(v.hwnd)
	_, settings := v.contentLayout(rect[2], rect[3], max(dpi, 96))
	return validDesktop(settings)
}

func validDesktop(settings displaySettings) displaySettings {
	if settings.width < 200 || settings.height < 200 {
		settings.width, settings.height = 1280, 720
	}
	settings.width, settings.height = min(settings.width, 8192), min(settings.height, 8192)
	settings.dpi = min(max(settings.dpi, 96), 480)
	return settings
}

func (v *viewer) contentLayout(width, height int32, dpi uintptr) (int32, displaySettings) {
	if v.rendererMode {
		return 0, displaySettings{width: width, height: height, dpi: dpi}
	}
	top := int32(32 * dpi / 96)
	if v.headerHidden {
		top = 0
	}
	top += v.toolbarHeight(dpi)
	return top, displaySettings{width: width, height: height - top, dpi: dpi}
}

func (v *viewer) dismissHeader() {
	if v.closed || v.headerHidden {
		return
	}
	declined := v.toolbar != nil && (v.toolbar.prompt == promptEnhanced || v.toolbar.prompt == promptEnhancedTry)
	v.headerHidden = true
	v.clearPrompt()
	if declined {
		v.toolbar.availability.notified = true
	}
	if v.toolbar != nil {
		for _, handle := range []uintptr{v.toolbar.confirmButton, v.toolbar.cancelButton} {
			if handle != 0 {
				user32.NewProc("ShowWindow").Call(handle, 0)
			}
		}
	}
	for _, handle := range []uintptr{v.header, v.headerClose} {
		if handle != 0 {
			user32.NewProc("ShowWindow").Call(handle, 0)
		}
	}
	v.layout = displaySettings{}
	v.resize()
	if v.child != 0 {
		user32.NewProc("SetFocus").Call(v.child)
	}
}

type displayUpdate struct {
	connected, waiting bool
	pending, applied   displaySettings
	changed, retry     time.Time
	unavailable        time.Time
}

func (d *displayUpdate) update(connected bool, settings displaySettings, now time.Time, send func(displaySettings) error) (bool, error) {
	if !connected {
		*d = displayUpdate{}
		return false, nil
	}
	d.connected = true
	if d.pending != settings {
		d.pending, d.changed = settings, now
		d.retry = time.Time{}
	}
	if settings == d.applied {
		d.waiting = false
		d.unavailable = time.Time{}
		return false, nil
	}
	// Let interactive resizing settle; a stable desktop needs no display update.
	if now.Sub(d.changed) < 200*time.Millisecond || now.Before(d.retry) {
		return false, nil
	}
	if err := send(settings); err != nil {
		d.retry = now.Add(2 * time.Second)
		var native *ole.OleError
		// The display channel may still initialize after login; keep persistent failures visible.
		if errors.As(err, &native) && uint32(native.Code()) == 0x8000FFFF {
			if d.unavailable.IsZero() {
				d.unavailable = now
			}
			d.waiting = now.Sub(d.unavailable) < 30*time.Second
		} else {
			d.waiting, d.unavailable = false, time.Time{}
		}
		if d.waiting {
			return false, nil
		}
		return true, err
	}
	d.applied, d.waiting, d.unavailable = settings, false, time.Time{}
	return true, nil
}
