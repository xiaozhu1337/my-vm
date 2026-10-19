//go:build windows

package console

import (
	"errors"
	"time"

	ole "github.com/go-ole/go-ole"
)

type displaySettings struct {
	width, height int32
	dpi           uintptr
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
