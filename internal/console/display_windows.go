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
